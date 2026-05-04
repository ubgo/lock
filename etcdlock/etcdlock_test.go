package etcdlock_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/ubgo/lock"
	"github.com/ubgo/lock/etcdlock"
)

// endpoints returns the etcd cluster endpoints from the env or
// skips the test. CI sets ETCDLOCK_TEST_ENDPOINTS via a service
// container; locally, run with
// `ETCDLOCK_TEST_ENDPOINTS=localhost:2379 go test`.
func endpoints(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ETCDLOCK_TEST_ENDPOINTS")
	if v == "" {
		t.Skip("ETCDLOCK_TEST_ENDPOINTS not set; skipping integration test")
	}
	return strings.Split(v, ",")
}

func client(t *testing.T) *clientv3.Client {
	t.Helper()
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints(t),
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestAcquireRelease(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "job", etcdlock.WithTTL(10*time.Second))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Token() == 0 {
		t.Errorf("Token = 0; want non-zero mod_revision")
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireConflict(t *testing.T) {
	cli := client(t)
	a := etcdlock.New(cli, "dup", etcdlock.WithTTL(10*time.Second))
	b := etcdlock.New(cli, "dup", etcdlock.WithTTL(10*time.Second))

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ha.Release() }()

	if _, err := b.Acquire(context.Background()); !errors.Is(err, etcdlock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "reuse", etcdlock.WithTTL(10*time.Second))

	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	h2, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = h2.Release()
}

func TestReleaseIdempotent(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "idem", etcdlock.WithTTL(10*time.Second))
	h, _ := l.Acquire(context.Background())
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestTokenIsMonotonicAcrossAcquires(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "tok", etcdlock.WithTTL(10*time.Second))

	var prev uint64
	for range 5 {
		h, _ := l.Acquire(context.Background())
		if h.Token() <= prev {
			t.Fatalf("token regressed: prev=%d got=%d", prev, h.Token())
		}
		prev = h.Token()
		_ = h.Release()
	}
}

func TestKeyPrefix(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "kp",
		etcdlock.WithTTL(10*time.Second),
		etcdlock.WithKeyPrefix("/test/etcdlock"),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
	if !strings.HasPrefix(h.Key(), "/test/etcdlock/kp/") {
		t.Fatalf("Key = %q; want prefix /test/etcdlock/kp/", h.Key())
	}
}

// Factory tests --------------------------------------------------------

func TestFactoryWithLock(t *testing.T) {
	cli := client(t)
	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(10*time.Second))
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
	cli := client(t)
	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(10*time.Second))
	want := errors.New("boom")
	got := locks.WithLock(context.Background(), "ferr", func(_ context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFactoryWithLockSkipsFnOnContended(t *testing.T) {
	cli := client(t)
	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(10*time.Second))

	h, _ := locks.Acquire(context.Background(), "fbusy")
	defer func() { _ = h.Release() }()

	called := false
	err := locks.WithLock(context.Background(), "fbusy", func(_ context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, etcdlock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if called {
		t.Fatal("fn must not run")
	}
}

// AsLocker -------------------------------------------------------------

func TestFactoryAsLocker(t *testing.T) {
	cli := client(t)
	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(10*time.Second))
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

// Concurrent contention ------------------------------------------------

func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
	cli := client(t)
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	winners := make(chan *etcdlock.Holder, n)
	for range n {
		go func() {
			defer wg.Done()
			l := etcdlock.New(cli, "race-etcd", etcdlock.WithTTL(10*time.Second))
			h, err := l.Acquire(context.Background())
			if err == nil {
				winners <- h
			}
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	var winner *etcdlock.Holder
	for h := range winners {
		count++
		winner = h
	}
	if count != 1 {
		t.Fatalf("expected 1 winner; got %d", count)
	}
	_ = winner.Release()
}

func TestRepeatedAcquireRelease(t *testing.T) {
	cli := client(t)
	const N = 10
	var ok atomic.Int32
	for i := range N {
		l := etcdlock.New(cli, "soak-etcd", etcdlock.WithTTL(10*time.Second))
		h, err := l.Acquire(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		_ = h.Release()
		ok.Add(1)
	}
	if ok.Load() != N {
		t.Fatalf("only %d/%d cycles", ok.Load(), N)
	}
}

func TestAcquireRespectsCancelledContext(t *testing.T) {
	cli := client(t)
	l := etcdlock.New(cli, "ctx-etcd", etcdlock.WithTTL(10*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
