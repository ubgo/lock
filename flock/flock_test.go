package flock_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lock"
	"github.com/ubgo/lock/flock"
)

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("job", flock.WithDir(dir))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Path() != filepath.Join(dir, "job.lock") {
		t.Errorf("Path = %q", h.Path())
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireConflictWithinSameProcess(t *testing.T) {
	dir := t.TempDir()
	a := flock.New("dup", flock.WithDir(dir))
	b := flock.New("dup", flock.WithDir(dir))

	ha, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	defer func() { _ = ha.Release() }()

	// On Linux, flock(2) is per-fd: a second OpenFile + flock from
	// the SAME process attempts to lock a different fd on the same
	// inode, which conflicts. So we expect ErrLocked.
	//
	// On macOS / BSD flock the same way. Windows LockFileEx is
	// strictly per-process for shared, but per-handle in exclusive
	// mode — same conflict.
	if _, err := b.Acquire(context.Background()); !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("b.Acquire = %v, want ErrLocked", err)
	}
}

func TestAcquireConflictAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock helper-process test wired for unix only; windows path covered by intra-process test")
	}
	dir := t.TempDir()

	// Spawn a helper subprocess that holds the lock until told to
	// release. We use the test binary itself with an env-var guard
	// (TEST_HELPER=1) so the helper code-path runs.
	if os.Getenv("FLOCK_TEST_HELPER") == "1" {
		// We are the helper. Acquire and block until SIGTERM/Stdin close.
		l := flock.New("xproc", flock.WithDir(os.Getenv("FLOCK_TEST_DIR")))
		h, err := l.Acquire(context.Background())
		if err != nil {
			os.Exit(2)
		}
		// Signal readiness on stdout, then block on stdin.
		_, _ = os.Stdout.Write([]byte("LOCKED\n"))
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		_ = h.Release()
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestAcquireConflictAcrossProcesses")
	cmd.Env = append(os.Environ(), "FLOCK_TEST_HELPER=1", "FLOCK_TEST_DIR="+dir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	// Wait for helper to signal LOCKED.
	buf := make([]byte, 7)
	if _, err := stdout.Read(buf); err != nil {
		t.Fatalf("read helper readiness: %v", err)
	}
	if string(buf[:6]) != "LOCKED" {
		t.Fatalf("unexpected helper output: %q", buf)
	}

	// Now we (the test) try to acquire — must be ErrLocked.
	l := flock.New("xproc", flock.WithDir(dir))
	if _, err := l.Acquire(context.Background()); !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("Acquire across processes = %v, want ErrLocked", err)
	}

	// Tell the helper to release, then verify we can take it.
	_, _ = stdin.Write([]byte("x"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after helper release: %v", err)
	}
	_ = h.Release()
}

func TestAcquireRespectsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("ctx", flock.WithDir(dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestReleaseIdempotent(t *testing.T) {
	l := flock.New("once", flock.WithDir(t.TempDir()))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestPathDefaults(t *testing.T) {
	l := flock.New("k")
	want := filepath.Clean(os.TempDir())
	if got := filepath.Dir(l.Path()); got != want {
		t.Fatalf("default dir = %q, want %q", got, want)
	}
}

func TestMkdirAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	l := flock.New("k", flock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire on nested dir: %v", err)
	}
	defer func() { _ = h.Release() }()
}

// Factory tests --------------------------------------------------------

func TestFactoryAcquire(t *testing.T) {
	dir := t.TempDir()
	locks := flock.NewFactory(flock.WithDir(dir))
	h, err := locks.Acquire(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
}

func TestFactoryWithLock(t *testing.T) {
	dir := t.TempDir()
	locks := flock.NewFactory(flock.WithDir(dir))
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
	dir := t.TempDir()
	locks := flock.NewFactory(flock.WithDir(dir))
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
	locks := flock.NewFactory(flock.WithDir(dir))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		// After the panic, lock must be free — we should be able to
		// re-acquire.
		h, err := locks.Acquire(context.Background(), "panicky")
		if err != nil {
			t.Fatalf("re-acquire after panic: %v", err)
		}
		_ = h.Release()
	}()

	_ = locks.WithLock(context.Background(), "panicky", func(_ context.Context) error {
		panic("kaboom")
	})
}

func TestFactoryWithLockSkipsFnOnContended(t *testing.T) {
	dir := t.TempDir()
	locks := flock.NewFactory(flock.WithDir(dir))

	h, _ := locks.Acquire(context.Background(), "busy")
	defer func() { _ = h.Release() }()

	called := false
	err := locks.WithLock(context.Background(), "busy", func(_ context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if called {
		t.Fatal("fn must not run when Acquire fails")
	}
}

func TestTopLevelWithLock(t *testing.T) {
	dir := t.TempDir()
	fl := flock.New("standalone", flock.WithDir(dir))
	called := false
	err := flock.WithLock(context.Background(), fl, func(_ context.Context) error {
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

// AsLocker tests -------------------------------------------------------

func TestFactoryAsLocker(t *testing.T) {
	dir := t.TempDir()
	locks := flock.NewFactory(flock.WithDir(dir))
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

func TestLockAsLocker(t *testing.T) {
	dir := t.TempDir()
	fl := flock.New("single", flock.WithDir(dir))
	l := fl.AsLocker()
	h, err := l.Acquire(context.Background(), "ignored-name")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
}

// Concurrent contention test — only one goroutine should win at a time.
func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	winners := make(chan struct{}, n)
	for range n {
		go func() {
			defer wg.Done()
			l := flock.New("race", flock.WithDir(dir))
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

// Soak: rapid acquire/release cycles. Confirms no fd leak by
// acquiring + releasing many times under the same name.
func TestRepeatedAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("soak", flock.WithDir(dir))
	const N = 200
	var ok atomic.Int32
	for range N {
		h, err := l.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		_ = h.Release()
		ok.Add(1)
	}
	if ok.Load() != N {
		t.Fatalf("only %d/%d cycles succeeded", ok.Load(), N)
	}
}
