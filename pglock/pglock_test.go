package pglock_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ubgo/lock"
	"github.com/ubgo/lock/pglock"
)

// dsn returns the Postgres connection string for tests, falling back
// to skipping the test if PGLOCK_TEST_DSN is not set. CI sets this
// via a GitHub Actions postgres service; locally, run with
// `PGLOCK_TEST_DSN='postgres://user:pass@localhost/db?sslmode=disable' go test`.
func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("PGLOCK_TEST_DSN")
	if v == "" {
		t.Skip("PGLOCK_TEST_DSN not set; skipping integration test")
	}
	return v
}

// pool builds a small pool for one test (5 conns is plenty for our
// concurrent-acquire tests), and tears it down on cleanup.
func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn(t))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 8
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestAcquireRelease(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "job")
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireConflict(t *testing.T) {
	p := pool(t)
	a := pglock.New(p, "dup-conflict")
	b := pglock.New(p, "dup-conflict")

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ha.Release() }()

	if _, err := b.Acquire(context.Background()); !errors.Is(err, pglock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "reuse")

	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	h2, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	_ = h2.Release()
}

func TestReleaseIdempotent(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "idem")
	h, _ := l.Acquire(context.Background())
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestKeyOffsetNamespacesKeyspace(t *testing.T) {
	p := pool(t)
	// Same name, different offset → different keys → both can hold.
	a := pglock.New(p, "ns", pglock.WithKeyOffset(0))
	b := pglock.New(p, "ns", pglock.WithKeyOffset(0x100000000))

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ha.Release() }()

	hb, err := b.Acquire(context.Background())
	if err != nil {
		t.Fatalf("different offset must allow concurrent hold: %v", err)
	}
	defer func() { _ = hb.Release() }()

	if a.Key() == b.Key() {
		t.Fatal("expected different keys for different offsets")
	}
}

// Factory tests --------------------------------------------------------

func TestFactoryAcquire(t *testing.T) {
	p := pool(t)
	locks := pglock.NewFactory(p)
	h, err := locks.Acquire(context.Background(), "factory-job")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
}

func TestFactoryWithLock(t *testing.T) {
	p := pool(t)
	locks := pglock.NewFactory(p)
	called := false
	err := locks.WithLock(context.Background(), "fwl", func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
}

func TestFactoryWithLockSurfacesFnError(t *testing.T) {
	p := pool(t)
	locks := pglock.NewFactory(p)
	want := errors.New("boom")
	got := locks.WithLock(context.Background(), "ferr", func(_ context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFactoryWithLockSkipsFnOnContended(t *testing.T) {
	p := pool(t)
	locks := pglock.NewFactory(p)
	h, _ := locks.Acquire(context.Background(), "fbusy")
	defer func() { _ = h.Release() }()

	called := false
	err := locks.WithLock(context.Background(), "fbusy", func(_ context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, pglock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if called {
		t.Fatal("fn must not run")
	}
}

// AsLocker -------------------------------------------------------------

func TestFactoryAsLocker(t *testing.T) {
	p := pool(t)
	locks := pglock.NewFactory(p)
	l := locks.AsLocker()
	h, err := l.Acquire(context.Background(), "iface")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
	if _, err := l.Acquire(context.Background(), "iface"); !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("got %v, want lock.ErrLocked", err)
	}
}

// Concurrent contention -----------------------------------------------

func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
	p := pool(t)
	const n = 8 // bounded by MaxConns
	var wg sync.WaitGroup
	wg.Add(n)
	winners := make(chan *pglock.Holder, n)
	for range n {
		go func() {
			defer wg.Done()
			l := pglock.New(p, "race-pg")
			h, err := l.Acquire(context.Background())
			if err == nil {
				winners <- h
			}
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	var winner *pglock.Holder
	for h := range winners {
		count++
		winner = h
	}
	if count != 1 {
		t.Fatalf("expected 1 winner, got %d", count)
	}
	_ = winner.Release()
}

func TestRepeatedAcquireRelease(t *testing.T) {
	p := pool(t)
	const N = 30
	var ok atomic.Int32
	for range N {
		l := pglock.New(p, "soak-pg")
		h, err := l.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = h.Release()
		ok.Add(1)
	}
	if ok.Load() != N {
		t.Fatalf("only %d/%d cycles", ok.Load(), N)
	}
}

func TestAcquireRespectsCancelledContext(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "ctx-pg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
