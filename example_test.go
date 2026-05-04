package lock_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ubgo/lock"
)

// memLocker is a tiny in-process Locker used for the runnable
// example. Real production code wires in a concrete backend such
// as filelock, flock, redislock, pglock, or etcdlock.
type memLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

func newMemLocker() *memLocker { return &memLocker{held: map[string]bool{}} }

func (m *memLocker) Acquire(_ context.Context, name string) (lock.Holder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[name] {
		return nil, lock.ErrLocked
	}
	m.held[name] = true
	return &memHolder{m: m, name: name}, nil
}

type memHolder struct {
	m    *memLocker
	name string
	done bool
}

func (h *memHolder) Release() error {
	if h.done {
		return nil
	}
	h.done = true
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	delete(h.m.held, h.name)
	return nil
}

// Example shows the minimal lock.Locker contract: Acquire returns
// a Holder; Release frees the lock; ErrLocked signals contention.
func Example() {
	ctx := context.Background()
	locks := newMemLocker()

	h, err := locks.Acquire(ctx, "nightly-import")
	if err != nil {
		fmt.Println("acquire failed:", err)
		return
	}
	defer func() { _ = h.Release() }()

	// Second Acquire with the lock held returns ErrLocked.
	if _, err := locks.Acquire(ctx, "nightly-import"); errors.Is(err, lock.ErrLocked) {
		fmt.Println("already running; skip")
	}

	// Output:
	// already running; skip
}

// ExampleLocker_swappableBackend shows the family's headline
// pattern: business code accepts lock.Locker; wiring at startup
// picks the concrete backend without touching call sites.
func ExampleLocker_swappableBackend() {
	type Service struct{ locks lock.Locker }

	doExport := func(s *Service) error {
		ctx := context.Background()
		h, err := s.locks.Acquire(ctx, "daily-export")
		if errors.Is(err, lock.ErrLocked) {
			return nil // another worker has it; skip
		}
		if err != nil {
			return err
		}
		defer func() { _ = h.Release() }()
		// ... real export work ...
		return nil
	}

	// Wire any Locker — flock for dev, redislock for prod, memlock
	// for tests. None of doExport changes.
	svc := &Service{locks: newMemLocker()}
	_ = doExport(svc)

	fmt.Println("export ran with no panic")

	// Output:
	// export ran with no panic
}
