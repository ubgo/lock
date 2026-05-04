package filelock_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lock/filelock"
)

func TestSemaphoreSingletonLayoutUnchanged(t *testing.T) {
	dir := t.TempDir()
	l := filelock.New("singleton", filelock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	// Default n=1 must keep the v0.1 layout — no .0 suffix.
	if _, err := os.Stat(filepath.Join(dir, "singleton.lock")); err != nil {
		t.Fatalf("expected singleton.lock at v0.1 path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "singleton.0.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("singleton mode should not create .0 file, got err=%v", err)
	}
}

func TestSemaphoreLayoutWhenN3(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))
	h, err := locks.Acquire(context.Background(), "throughput", filelock.WithMaxConcurrent(3))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	// First slot wins → throughput.0.lock should exist; .lock (singleton) should not.
	if _, err := os.Stat(filepath.Join(dir, "throughput.0.lock")); err != nil {
		t.Fatalf("expected throughput.0.lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "throughput.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("semaphore mode should not create singleton path, got err=%v", err)
	}
}

func TestSemaphoreAllowsNConcurrent(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))
	const n = 3

	holders := make([]any, 0, n)
	for range n {
		h, err := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n))
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		holders = append(holders, h)
	}
	defer func() {
		for _, h := range holders {
			_ = h.(*filelock.Holder).Release()
		}
	}()

	// (n+1)th acquire must fail — all slots occupied.
	if _, err := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n)); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all %d slots are held", err, n)
	}
}

func TestSemaphoreReleasingFreesSlot(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))
	const n = 2

	h1, _ := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n))
	h2, _ := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n))

	// Both slots held — third must fail.
	if _, err := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n)); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("3rd acquire should fail: got %v", err)
	}

	// Release one → next acquire must succeed.
	_ = h1.Release()
	h3, err := locks.Acquire(context.Background(), "sem", filelock.WithMaxConcurrent(n))
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	_ = h2.Release()
	_ = h3.Release()
}

func TestSemaphoreConcurrentAcquireBoundedByN(t *testing.T) {
	dir := t.TempDir()
	const n = 4
	locks := filelock.NewFactory(filelock.WithDir(dir))

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var winners atomic.Int32
	hCh := make(chan *filelock.Holder, goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			h, err := locks.Acquire(context.Background(), "race", filelock.WithMaxConcurrent(n))
			if err == nil {
				winners.Add(1)
				hCh <- h
			}
		}()
	}
	wg.Wait()
	close(hCh)

	if got := winners.Load(); got != n {
		t.Fatalf("expected exactly %d winners, got %d", n, got)
	}
	for h := range hCh {
		_ = h.Release()
	}
}

func TestSemaphoreSlotDebugFieldWritten(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	h0, err := locks.Acquire(context.Background(), "dbg", filelock.WithMaxConcurrent(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h0.Release() }()

	h1, err := locks.Acquire(context.Background(), "dbg", filelock.WithMaxConcurrent(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h1.Release() }()

	// Two markers, slots 0 and 1 — verified by file existence.
	if _, err := os.Stat(filepath.Join(dir, "dbg.0.lock")); err != nil {
		t.Errorf("dbg.0.lock missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dbg.1.lock")); err != nil {
		t.Errorf("dbg.1.lock missing: %v", err)
	}
}

func TestWithMaxConcurrentZeroIsClampedTo1(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir))

	// n=0 should be clamped to singleton mode — file at v0.1 path.
	h, err := locks.Acquire(context.Background(), "clamp", filelock.WithMaxConcurrent(0))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()
	if _, err := os.Stat(filepath.Join(dir, "clamp.lock")); err != nil {
		t.Fatalf("expected singleton path on clamp: %v", err)
	}
}
