// Package lock is the shared interface for the ubgo/lock family — a
// suite of named-mutex implementations spanning single-host
// (filelock, flock) and distributed (redislock, pglock, etcdlock)
// backends.
//
// Each concrete backend lives at its own subpath under
// github.com/ubgo/lock/<name>, and ships a typed Factory with the
// rich API for the mechanism it implements. This root package defines
// a tiny minimum-common-denominator interface so consumers who want
// polymorphism can take "any lock" — same shape as `database/sql`'s
// driver interface.
//
// # Direct backend use (the dominant case)
//
//	import "github.com/ubgo/lock/filelock"
//	locks := filelock.NewFactory(filelock.WithDir("/tmp"))
//	locks.WithLock(ctx, "job", fn, filelock.WithStaleAfter(5*time.Minute))
//
// # Polymorphic use (library authors)
//
//	import "github.com/ubgo/lock"
//
//	type TaskRunner struct {
//	    locks lock.Locker
//	}
//
//	func (r *TaskRunner) Run(ctx context.Context, name string, fn func() error) error {
//	    h, err := r.locks.Acquire(ctx, name)
//	    if errors.Is(err, lock.ErrLocked) {
//	        return nil // already running; skip
//	    }
//	    if err != nil {
//	        return err
//	    }
//	    defer h.Release()
//	    return fn()
//	}
//
// Wiring is at startup — your consumers decide which backend goes
// behind the [Locker] interface.
//
// The interface deliberately stays minimal — `Acquire(ctx, name)`
// returning a `Holder` with `Release()`. Anything richer (options,
// fencing tokens, auto-renewal, max-concurrent, sweep) lives on the
// concrete backend's typed surface where callers can use it without
// abstraction loss.
package lock

import (
	"context"
	"errors"
)

// Locker takes a named lock. Implementations live under
// github.com/ubgo/lock/<backend> — and any user-defined type that
// satisfies the contract.
type Locker interface {
	// Acquire takes the named lock. Returns a Holder on success or an
	// error otherwise. Implementations return ErrLocked (this package's
	// sentinel) when the lock is currently held by someone else.
	//
	// The context bounds how long Acquire may block on backend I/O —
	// for backends that don't block (marker file, flock, in-memory) the
	// context is honored only for cancellation.
	Acquire(ctx context.Context, name string) (Holder, error)
}

// Holder is the typed handle returned by a successful Acquire. The
// minimal contract is Release; concrete implementations return a
// richer type (e.g. *filelock.Holder with Token() and Path()) that
// satisfies this interface.
type Holder interface {
	// Release frees the lock. Idempotent — calling Release more than
	// once on the same Holder returns nil after the first call.
	Release() error
}

// ErrLocked is the canonical sentinel returned when a lock is held
// by someone else. Every backend in the family returns this exact
// value (compare via errors.Is). Mirroring the same sentinel across
// backends lets polymorphic consumers branch without per-backend
// type switches:
//
//	h, err := locks.Acquire(ctx, "job")
//	if errors.Is(err, lock.ErrLocked) {
//	    // someone else has it — skip this run
//	}
var ErrLocked = errors.New("lock: locked")
