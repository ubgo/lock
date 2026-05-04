package redislock_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ubgo/lock"
	"github.com/ubgo/lock/redislock"
)

// startMini spins up a miniredis instance for one test. Returns the
// go-redis client and a cleanup func.
func startMini(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

func TestAcquireRelease(t *testing.T) {
	rdb, _ := startMini(t)
	l := redislock.New(rdb, "job")

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Token() == 0 {
		t.Errorf("Token = 0; want monotonic > 0")
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireConflict(t *testing.T) {
	rdb, _ := startMini(t)
	a := redislock.New(rdb, "dup")
	b := redislock.New(rdb, "dup")

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ha.Release() }()

	if _, err := b.Acquire(context.Background()); !errors.Is(err, redislock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestReleaseAfterTTLReturnsLockLost(t *testing.T) {
	rdb, mr := startMini(t)
	l := redislock.New(rdb, "ttl", redislock.WithTTL(time.Second))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Fast-forward miniredis past the TTL.
	mr.FastForward(2 * time.Second)

	err = h.Release()
	if !errors.Is(err, redislock.ErrLockLost) {
		t.Fatalf("got %v, want ErrLockLost", err)
	}
}

func TestReleaseDoesNotStompSuccessor(t *testing.T) {
	rdb, mr := startMini(t)
	a := redislock.New(rdb, "swap", redislock.WithTTL(time.Second))
	b := redislock.New(rdb, "swap", redislock.WithTTL(time.Second))

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// A's TTL expires; B takes over.
	mr.FastForward(2 * time.Second)
	hb, err := b.Acquire(context.Background())
	if err != nil {
		t.Fatalf("b.Acquire: %v", err)
	}

	// Now A's late Release must NOT delete B's key. The Lua script
	// guards against this — Release returns ErrLockLost.
	if err := ha.Release(); !errors.Is(err, redislock.ErrLockLost) {
		t.Fatalf("a.Release after takeover = %v, want ErrLockLost", err)
	}

	// B's lock must still be intact — Release returns nil.
	if err := hb.Release(); err != nil {
		t.Fatalf("b.Release: %v", err)
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	rdb, _ := startMini(t)
	l := redislock.New(rdb, "reuse")

	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	h2, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	_ = h2.Release()
}

func TestTokenIsMonotonic(t *testing.T) {
	rdb, _ := startMini(t)
	l := redislock.New(rdb, "tok")

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

func TestExtendBumpsTTL(t *testing.T) {
	rdb, mr := startMini(t)
	l := redislock.New(rdb, "ext", redislock.WithTTL(time.Second))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	// Just before TTL would expire, Extend.
	mr.FastForward(800 * time.Millisecond)
	if err := h.Extend(context.Background()); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	// 800ms more would have expired the original (cumulative 1.6s),
	// but we extended → still ours.
	mr.FastForward(800 * time.Millisecond)
	if err := h.Extend(context.Background()); err != nil {
		t.Fatalf("Extend after extension: %v", err)
	}
}

func TestExtendAfterLossReturnsErrLockLost(t *testing.T) {
	rdb, mr := startMini(t)
	l := redislock.New(rdb, "extlost", redislock.WithTTL(time.Second))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// TTL expires; somebody else takes the lock.
	mr.FastForward(2 * time.Second)
	other := redislock.New(rdb, "extlost", redislock.WithTTL(time.Second))
	ho, _ := other.Acquire(context.Background())
	defer func() { _ = ho.Release() }()

	if err := h.Extend(context.Background()); !errors.Is(err, redislock.ErrLockLost) {
		t.Fatalf("Extend after loss = %v, want ErrLockLost", err)
	}
}

// Factory tests --------------------------------------------------------

func TestFactoryAcquire(t *testing.T) {
	rdb, _ := startMini(t)
	locks := redislock.NewFactory(rdb)
	h, err := locks.Acquire(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
}

func TestFactoryWithLock(t *testing.T) {
	rdb, _ := startMini(t)
	locks := redislock.NewFactory(rdb)
	called := false
	err := locks.WithLock(context.Background(), "wl", func(_ context.Context) error {
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
	rdb, _ := startMini(t)
	locks := redislock.NewFactory(rdb)
	want := errors.New("boom")
	got := locks.WithLock(context.Background(), "err", func(_ context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFactoryWithLockSkipsFnOnContended(t *testing.T) {
	rdb, _ := startMini(t)
	locks := redislock.NewFactory(rdb)

	h, _ := locks.Acquire(context.Background(), "busy")
	defer func() { _ = h.Release() }()

	called := false
	err := locks.WithLock(context.Background(), "busy", func(_ context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, redislock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if called {
		t.Fatal("fn must not run when Acquire fails")
	}
}

func TestKeyPrefixNamespaces(t *testing.T) {
	rdb, mr := startMini(t)
	locks := redislock.NewFactory(rdb, redislock.WithKeyPrefix("svcA"))

	h, err := locks.Acquire(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	// Key should have the custom prefix.
	if !mr.Exists("svcA:job") {
		t.Fatal("expected key svcA:job")
	}
	if mr.Exists("redislock:job") {
		t.Fatal("default prefix should not be used when override set")
	}
}

// AsLocker tests -------------------------------------------------------

func TestFactoryAsLocker(t *testing.T) {
	rdb, _ := startMini(t)
	locks := redislock.NewFactory(rdb)
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
	rdb, _ := startMini(t)
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	winners := make(chan *redislock.Holder, n)
	for range n {
		go func() {
			defer wg.Done()
			l := redislock.New(rdb, "race")
			h, err := l.Acquire(context.Background())
			if err == nil {
				winners <- h
			}
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	var last *redislock.Holder
	for h := range winners {
		count++
		last = h
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 winner; got %d", count)
	}
	_ = last.Release()
}

// Soak test — confirms the implementation tolerates many cycles.
func TestRepeatedAcquireRelease(t *testing.T) {
	rdb, _ := startMini(t)
	const N = 100
	var ok atomic.Int32
	for i := range N {
		l := redislock.New(rdb, "soak")
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
	rdb, _ := startMini(t)
	l := redislock.New(rdb, "ctx")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
