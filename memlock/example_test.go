package memlock_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/ubgo/lock/memlock"
)

// Example shows the in-memory drop-in for unit tests. Same
// shape as production backends (Acquire / Release / WithLock /
// AsLocker), but state lives in a per-Factory map — no file
// I/O, no network, no infrastructure required.
func Example() {
	locks := memlock.NewFactory()
	ctx := context.Background()

	h, err := locks.Acquire(ctx, "test-job")
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = h.Release() }()

	if _, err := locks.Acquire(ctx, "test-job"); errors.Is(err, memlock.ErrLocked) {
		fmt.Println("already locked")
	}

	// Output:
	// already locked
}

// ExampleFactory_AsLocker shows the typical test-substitution
// pattern: production wires a real backend, tests wire memlock,
// the service code accepts lock.Locker either way.
func ExampleFactory_AsLocker() {
	locks := memlock.NewFactory()
	l := locks.AsLocker()

	ctx := context.Background()
	h, _ := l.Acquire(ctx, "test")
	defer func() { _ = h.Release() }()
	fmt.Println("memlock satisfies lock.Locker")

	// Output:
	// memlock satisfies lock.Locker
}
