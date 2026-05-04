package flock_test

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ubgo/lock/flock"
)

// Example shows the kernel-fenced acquire / release flow. The
// kernel releases the lock automatically if the process exits
// without calling Release.
func Example() {
	dir, _ := os.MkdirTemp("", "flock-example")
	defer func() { _ = os.RemoveAll(dir) }()

	fl := flock.New("nightly-import", flock.WithDir(dir))

	holder, err := fl.Acquire(context.Background())
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = holder.Release() }()

	// Second Acquire on the same name returns ErrLocked even
	// from within the same process — flock(2) on Linux is
	// per-fd; each Acquire opens a fresh fd.
	other := flock.New("nightly-import", flock.WithDir(dir))
	if _, err := other.Acquire(context.Background()); errors.Is(err, flock.ErrLocked) {
		fmt.Println("already locked")
	}

	// Output:
	// already locked
}

// ExampleWithMaxConcurrent shows semaphore mode — up to N holders
// for the same name. Each slot is its own kernel-locked file.
func ExampleWithMaxConcurrent() {
	dir, _ := os.MkdirTemp("", "flock-semaphore")
	defer func() { _ = os.RemoveAll(dir) }()

	const n = 2
	a := flock.New("worker", flock.WithDir(dir))
	b := flock.New("worker", flock.WithDir(dir))
	c := flock.New("worker", flock.WithDir(dir))

	h1, _ := a.Acquire(context.Background(), flock.WithMaxConcurrent(n))
	h2, _ := b.Acquire(context.Background(), flock.WithMaxConcurrent(n))

	// Third caller: all 2 slots are taken.
	if _, err := c.Acquire(context.Background(), flock.WithMaxConcurrent(n)); errors.Is(err, flock.ErrLocked) {
		fmt.Println("third caller skipped")
	}

	_ = h1.Release()
	_ = h2.Release()

	// Output:
	// third caller skipped
}
