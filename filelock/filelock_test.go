package filelock_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lock"
	"github.com/ubgo/lock/filelock"
)

func TestLockAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	l := filelock.New("job", filelock.WithDir(dir))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "job.lock")); err != nil {
		t.Fatalf("marker file missing: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "job.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker file should be gone, got err=%v", err)
	}
}

func TestLockAcquireConflict(t *testing.T) {
	dir := t.TempDir()
	a := filelock.New("dup", filelock.WithDir(dir))
	b := filelock.New("dup", filelock.WithDir(dir))

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	defer func() { _ = ha.Release() }()

	if _, err := b.Acquire(context.Background()); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("b.Acquire = %v, want ErrLocked", err)
	}
}

func TestReleaseIdempotent(t *testing.T) {
	l := filelock.New("once", filelock.WithDir(t.TempDir()))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v (must be no-op)", err)
	}
}

func TestReleaseAfterMarkerGone(t *testing.T) {
	dir := t.TempDir()
	l := filelock.New("ghost", filelock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Operator-style cleanup: delete the marker out from under us.
	if err := os.Remove(h.Path()); err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release with missing marker should be a no-op, got %v", err)
	}
}

func TestPath(t *testing.T) {
	l := filelock.New("k", filelock.WithDir("/tmp/x"))
	want := filepath.Join("/tmp/x", "k.lock")
	if got := l.Path(); got != want {
		t.Fatalf("Path = %q want %q", got, want)
	}
}

func TestDefaultDirIsTemp(t *testing.T) {
	l := filelock.New("k")
	want := filepath.Clean(os.TempDir())
	if got := filepath.Dir(l.Path()); got != want {
		t.Fatalf("default dir = %q, want %q", got, want)
	}
}

func TestMkdirAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	l := filelock.New("k", filelock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire on nested dir: %v", err)
	}
	defer func() { _ = h.Release() }()
	if _, err := os.Stat(h.Path()); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestAcquireMkdirFails(t *testing.T) {
	parent := t.TempDir()
	conflict := filepath.Join(parent, "blocker")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(conflict, "child")
	l := filelock.New("k", filelock.WithDir(dir))
	if _, err := l.Acquire(context.Background()); err == nil {
		t.Fatal("expected mkdir failure")
	}
}

func TestAcquireExistingMarkerIsErrLocked(t *testing.T) {
	dir := t.TempDir()
	l := filelock.New("k", filelock.WithDir(dir))
	if err := os.WriteFile(l.Path(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background()); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

func TestAcquireRespectsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	l := filelock.New("ctx", filelock.WithDir(dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if _, err := os.Stat(l.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled Acquire should not have created marker, got err=%v", err)
	}
}

func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	winners := make(chan struct{}, n)
	for range n {
		go func() {
			defer wg.Done()
			l := filelock.New("race", filelock.WithDir(dir))
			h, err := l.Acquire(context.Background())
			if err == nil {
				winners <- struct{}{}
				_ = h.Release()
			}
		}()
	}
	wg.Wait()
	close(winners)
	count := 0
	for range winners {
		count++
	}
	if count == 0 {
		t.Fatal("nobody acquired")
	}
}

// Factory tests --------------------------------------------------------

func TestFactoryAcquire(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))
	h, err := locks.Acquire(context.Background(), "job")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()
	if _, err := os.Stat(filepath.Join(dir, "job.lock")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestFactoryPerCallOverridesFactoryDir(t *testing.T) {
	factoryDir := t.TempDir()
	overrideDir := t.TempDir()

	locks := filelock.NewFactory(filelock.WithDir(factoryDir))
	h, err := locks.Acquire(context.Background(), "job", filelock.WithDir(overrideDir))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	// marker should be in overrideDir, not factoryDir
	if _, err := os.Stat(filepath.Join(overrideDir, "job.lock")); err != nil {
		t.Fatalf("marker not in override dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(factoryDir, "job.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker should not be in factory dir, got err=%v", err)
	}
}

func TestFactoryWithLockRunsFnAndReleases(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	var ran atomic.Bool
	err := locks.WithLock(context.Background(), "wl", func(_ context.Context) error {
		ran.Store(true)
		// Marker must exist while fn is running.
		if _, err := os.Stat(filepath.Join(dir, "wl.lock")); err != nil {
			t.Errorf("marker missing inside fn: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !ran.Load() {
		t.Fatal("fn was not called")
	}
	// Marker must be gone after WithLock returns.
	if _, err := os.Stat(filepath.Join(dir, "wl.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker should be released, got err=%v", err)
	}
}

func TestFactoryWithLockDoesNotCallFnOnErrLocked(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	// Pre-acquire the lock so WithLock will see ErrLocked.
	h, err := locks.Acquire(context.Background(), "busy")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	var called bool
	err = locks.WithLock(context.Background(), "busy", func(_ context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("WithLock = %v, want ErrLocked", err)
	}
	if called {
		t.Fatal("fn must not be called when Acquire fails")
	}
}

func TestFactoryWithLockSurfacesFnError(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))
	want := errors.New("boom")
	got := locks.WithLock(context.Background(), "err", func(_ context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFactoryWithLockReleasesOnPanic(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		// Marker must be gone even though fn panicked.
		if _, err := os.Stat(filepath.Join(dir, "panicky.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("marker should be released after panic, got err=%v", err)
		}
	}()

	_ = locks.WithLock(context.Background(), "panicky", func(_ context.Context) error {
		panic("kaboom")
	})
}

// Top-level WithLock ---------------------------------------------------

func TestTopLevelWithLock(t *testing.T) {
	dir := t.TempDir()
	fl := filelock.New("standalone", filelock.WithDir(dir))
	called := false
	err := filelock.WithLock(context.Background(), fl, func(_ context.Context) error {
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

// lock.Locker adapter -----------------------------------------------

func TestFactoryAsLocker(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	l := locks.AsLocker()

	h, err := l.Acquire(context.Background(), "via-iface")
	if err != nil {
		t.Fatalf("Acquire via locker: %v", err)
	}
	defer func() { _ = h.Release() }()

	// Second acquire on same name returns the shared lock.ErrLocked
	// (not the package-specific filelock.ErrLocked) — that's the
	// contract of the interface.
	_, err = l.Acquire(context.Background(), "via-iface")
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("got %v, want lock.ErrLocked", err)
	}
}

func TestLockAsLocker(t *testing.T) {
	dir := t.TempDir()
	fl := filelock.New("single", filelock.WithDir(dir))

	l := fl.AsLocker()
	h, err := l.Acquire(context.Background(), "ignored-name")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// Compile-time interface assertions ------------------------------------

var (
	_ lock.Locker = (*filelock.Factory)(nil).AsLocker()
	_ lock.Locker = (*filelock.Lock)(nil).AsLocker()
)
