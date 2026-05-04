package filelock_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ubgo/lock/filelock"
)

// Example shows the basic Acquire / Release flow with a Factory.
func Example() {
	dir, _ := os.MkdirTemp("", "filelock-example")
	defer func() { _ = os.RemoveAll(dir) }()

	locks := filelock.NewFactory(filelock.WithDir(dir))

	holder, err := locks.Acquire(context.Background(), "job")
	if err != nil {
		fmt.Println("acquire failed:", err)
		return
	}
	defer func() { _ = holder.Release() }()

	// Second Acquire while the lock is held returns ErrLocked.
	if _, err := locks.Acquire(context.Background(), "job"); errors.Is(err, filelock.ErrLocked) {
		fmt.Println("already locked")
	}

	// Output:
	// already locked
}

// ExampleFactory_WithLock shows the WithLock helper — Acquire,
// run fn, Release — collapsed into one call. Releases on panic.
func ExampleFactory_WithLock() {
	dir, _ := os.MkdirTemp("", "filelock-withlock")
	defer func() { _ = os.RemoveAll(dir) }()

	locks := filelock.NewFactory(filelock.WithDir(dir))

	err := locks.WithLock(context.Background(), "nightly-import",
		func(_ context.Context) error {
			fmt.Println("running protected work")
			return nil
		},
	)
	if err != nil {
		fmt.Println("error:", err)
	}

	// Output:
	// running protected work
}

// ExampleWithMaxConcurrent shows semaphore mode — up to N holders
// for the same name simultaneously.
func ExampleWithMaxConcurrent() {
	dir, _ := os.MkdirTemp("", "filelock-semaphore")
	defer func() { _ = os.RemoveAll(dir) }()

	locks := filelock.NewFactory(filelock.WithDir(dir))

	// Three slots: holders 0, 1, 2 succeed.
	var holders []*filelock.Holder
	for i := 0; i < 3; i++ {
		h, err := locks.Acquire(context.Background(), "indexer", filelock.WithMaxConcurrent(3))
		if err != nil {
			fmt.Println("unexpected:", err)
			return
		}
		holders = append(holders, h)
	}
	// Fourth acquire fails — all 3 slots taken.
	if _, err := locks.Acquire(context.Background(), "indexer", filelock.WithMaxConcurrent(3)); errors.Is(err, filelock.ErrLocked) {
		fmt.Println("4th caller skipped")
	}
	for _, h := range holders {
		_ = h.Release()
	}

	// Output:
	// 4th caller skipped
}

// ExampleWithStaleAfter shows the time-based stale-takeover window.
// If a holder crashes and the PID probe is inconclusive (cross-host,
// permission-denied), the next Acquire takes over after this window.
func ExampleWithStaleAfter() {
	dir, _ := os.MkdirTemp("", "filelock-stale")
	defer func() { _ = os.RemoveAll(dir) }()

	locks := filelock.NewFactory(filelock.WithDir(dir))
	h, err := locks.Acquire(context.Background(), "long-job",
		filelock.WithStaleAfter(2*time.Hour),
	)
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = h.Release() }()

	fmt.Println("holder path:", filepath.Base(h.Path()))

	// Output:
	// holder path: long-job.lock
}

// ExampleHolder_Token shows fencing tokens — a monotonic uint64
// that downstream consumers use to reject stale-holder writes.
func ExampleHolder_Token() {
	dir, _ := os.MkdirTemp("", "filelock-fence")
	defer func() { _ = os.RemoveAll(dir) }()

	locks := filelock.NewFactory(filelock.WithDir(dir))

	h1, _ := locks.Acquire(context.Background(), "payments")
	t1 := h1.Token()
	_ = h1.Release()

	h2, _ := locks.Acquire(context.Background(), "payments")
	t2 := h2.Token()
	_ = h2.Release()

	// Tokens are strictly monotonic across acquires.
	if t2 > t1 {
		fmt.Println("monotonic")
	}

	// Output:
	// monotonic
}
