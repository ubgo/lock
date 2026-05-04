// Package memlock is an in-memory implementation of the locking
// surface exposed by ubgo/filelock — same Factory shape, same Holder
// shape, satisfying the same [github.com/ubgo/lock.Locker]
// interface, but with all state held in process memory. No files,
// no network, no external dependencies.
//
// # Why this exists
//
// Drop-in replacement for unit tests. Production code accepts a
// [github.com/ubgo/lock.Locker] (or wires [filelock.Factory] directly);
// tests substitute [Factory] without touching disk or running through
// the real PID / staleness machinery. Result: tests run in
// milliseconds with zero filesystem flakiness — and the only thing
// they don't exercise is the marker-file format itself, which has
// dedicated tests in the filelock package.
//
// Inspired by MEDIGO/go-dlm (the only Go locking library we surveyed
// that ships an in-memory backend); ubgo/lock-* makes it standard
// across every backend in the family.
//
// # What's implemented
//
//   - Singleton mutual exclusion per lock name
//   - Semaphore mode via [WithMaxConcurrent]
//   - Monotonic [Holder.Token] per lock name
//   - The [github.com/ubgo/lock.Locker] interface adapter
//
// # What's NOT implemented
//
//   - Stale-detection / takeover: there is no "crashed process",
//     so the concept doesn't apply. A holder Release()ing is the
//     only way a slot is freed.
//   - Cross-process sharing: state is per-process. Use the real
//     filelock.Factory (or a sister module) when multiple
//     processes need to coordinate.
package memlock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/ubgo/lock"
)

// ErrLocked mirrors filelock.ErrLocked — returned when no slot is
// available. Use [errors.Is] to check. Adapter to
// [lock.ErrLocked] is provided by [Factory.AsLocker].
var ErrLocked = errors.New("memlock: locked")

// Factory holds the in-memory lock state. Construct one per test
// (or per test suite); state is not shared across factories.
type Factory struct {
	mu     sync.Mutex
	state  map[string]*lockState
	tokens map[string]*atomic.Uint64
}

// NewFactory returns an empty Factory. Options are accepted for API
// parity with [github.com/ubgo/lock/filelock.NewFactory] but currently
// none of them affect behaviour — the in-memory backend has no dir
// or stale window to configure. Future options that DO matter (e.g.
// observability hooks) will be plumbed through here.
func NewFactory(_ ...Option) *Factory {
	return &Factory{
		state:  make(map[string]*lockState),
		tokens: make(map[string]*atomic.Uint64),
	}
}

// Option is currently a no-op extension point. Mirroring the filelock
// API surface so tests can swap factories without changing options
// passed by production code.
type Option func(*config)

type config struct {
	maxConcurrent int
}

// WithMaxConcurrent enables semaphore mode for a per-call Acquire.
// Default 1 == singleton.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}

// Acquire takes a slot for name, returning a [Holder] on success or
// [ErrLocked] when no slot is free. ctx is honoured at entry — a
// cancelled context returns ctx.Err() before any state is changed.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := config{maxConcurrent: 1}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	f.mu.Lock()
	st, ok := f.state[name]
	if !ok {
		st = &lockState{}
		f.state[name] = st
	}
	tk, ok := f.tokens[name]
	if !ok {
		tk = new(atomic.Uint64)
		f.tokens[name] = tk
	}
	if st.held >= cfg.maxConcurrent {
		f.mu.Unlock()
		return nil, ErrLocked
	}
	st.held++
	f.mu.Unlock()

	return &Holder{
		f:     f,
		name:  name,
		token: tk.Add(1),
	}, nil
}

// WithLock acquires the lock for name, runs fn, and releases. Errors
// from fn are returned; release errors are returned only when fn
// returned nil (mirroring [filelock.Factory.WithLock]).
func (f *Factory) WithLock(ctx context.Context, name string, fn func(context.Context) error, opts ...Option) (err error) {
	h, acqErr := f.Acquire(ctx, name, opts...)
	if acqErr != nil {
		return acqErr
	}
	defer func() {
		relErr := h.Release()
		if err == nil {
			err = relErr
		}
	}()
	return fn(ctx)
}

// AsLocker exposes f as a [lock.Locker] for backend-agnostic code.
// The Acquire path translates [ErrLocked] into [lock.ErrLocked].
func (f *Factory) AsLocker() lock.Locker {
	return adapter{f: f}
}

type adapter struct {
	f *Factory
}

func (a adapter) Acquire(ctx context.Context, name string) (lock.Holder, error) {
	h, err := a.f.Acquire(ctx, name)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return nil, lock.ErrLocked
		}
		return nil, err
	}
	return holderAdapter{h: h}, nil
}

type holderAdapter struct {
	h *Holder
}

func (a holderAdapter) Release() error {
	return a.h.Release()
}

type lockState struct {
	held int
}

// Holder is the in-memory equivalent of filelock.Holder. Same shape:
// idempotent Release, monotonic Token.
type Holder struct {
	f        *Factory
	name     string
	token    uint64
	released atomic.Bool
}

// Token returns the monotonic fencing token assigned at Acquire.
// Zero is never returned for a successful acquire — token sequence
// starts at 1.
func (h *Holder) Token() uint64 {
	return h.token
}

// Release frees the slot. Subsequent calls are no-ops.
func (h *Holder) Release() error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	st, ok := h.f.state[h.name]
	if !ok {
		return nil
	}
	st.held--
	if st.held <= 0 {
		delete(h.f.state, h.name)
	}
	return nil
}
