package memlock_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lock"
	"github.com/ubgo/lock/memlock"
)

func TestAcquireRelease(t *testing.T) {
	f := memlock.NewFactory()
	h, err := f.Acquire(context.Background(), "job")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireConflict(t *testing.T) {
	f := memlock.NewFactory()
	h, err := f.Acquire(context.Background(), "dup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	if _, err := f.Acquire(context.Background(), "dup"); !errors.Is(err, memlock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	f := memlock.NewFactory()
	h, _ := f.Acquire(context.Background(), "reuse")
	_ = h.Release()
	h2, err := f.Acquire(context.Background(), "reuse")
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	_ = h2.Release()
}

func TestSemaphore(t *testing.T) {
	f := memlock.NewFactory()
	const n = 3

	holders := make([]*memlock.Holder, 0, n)
	for range n {
		h, err := f.Acquire(context.Background(), "sem", memlock.WithMaxConcurrent(n))
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		holders = append(holders, h)
	}
	if _, err := f.Acquire(context.Background(), "sem", memlock.WithMaxConcurrent(n)); !errors.Is(err, memlock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all slots held", err)
	}
	for _, h := range holders {
		_ = h.Release()
	}
}

func TestTokenMonotonic(t *testing.T) {
	f := memlock.NewFactory()
	var prev uint64
	for range 5 {
		h, _ := f.Acquire(context.Background(), "t")
		if h.Token() <= prev {
			t.Fatalf("token did not advance: prev=%d, now=%d", prev, h.Token())
		}
		prev = h.Token()
		_ = h.Release()
	}
}

func TestConcurrentAcquire(t *testing.T) {
	f := memlock.NewFactory()
	const goroutines = 64
	var winners atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			h, err := f.Acquire(context.Background(), "race")
			if err == nil {
				winners.Add(1)
				_ = h.Release()
			}
		}()
	}
	wg.Wait()
	if winners.Load() == 0 {
		t.Fatal("nobody won the race")
	}
}

func TestWithLock(t *testing.T) {
	f := memlock.NewFactory()
	called := false
	err := f.WithLock(context.Background(), "wl", func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
}

func TestWithLockSurfacesFnError(t *testing.T) {
	f := memlock.NewFactory()
	want := errors.New("boom")
	got := f.WithLock(context.Background(), "err", func(_ context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAsLockerSatisfiesInterface(t *testing.T) {
	f := memlock.NewFactory()
	l := f.AsLocker()
	h, err := l.Acquire(context.Background(), "iface")
	if err != nil {
		t.Fatalf("via interface: %v", err)
	}
	defer func() { _ = h.Release() }()

	if _, err := l.Acquire(context.Background(), "iface"); !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("got %v, want lock.ErrLocked", err)
	}
}

func TestCancelledContext(t *testing.T) {
	f := memlock.NewFactory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Acquire(ctx, "ctx"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestReleaseIdempotent(t *testing.T) {
	f := memlock.NewFactory()
	h, _ := f.Acquire(context.Background(), "rel")
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}
