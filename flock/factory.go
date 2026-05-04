package flock

import "context"

// Factory holds shared configuration for many lock names. Construct
// one at app boot with the cross-cutting options (typically
// [WithDir]); each call site then names its lock.
//
// A Factory is safe for concurrent use.
type Factory struct {
	defaults config
}

// NewFactory returns a [Factory] with the given default options.
// Per-call options on each Acquire / WithLock override these defaults.
func NewFactory(opts ...Option) *Factory {
	return &Factory{
		defaults: applyOptions(defaultConfig(), opts),
	}
}

// Acquire opens the lock file for name and takes a kernel-fenced
// advisory lock. Returns a [Holder] on success, [ErrLocked] when the
// lock is held by another process, or any other error from the
// underlying syscalls.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	cfg := applyOptions(f.defaults, opts)
	return acquire(ctx, name, cfg)
}

// WithLock acquires the lock for name, runs fn, and releases. If the
// lock is already held it returns [ErrLocked] without calling fn.
// Errors from fn are returned; release errors only when fn returned
// nil (so a failing job's error isn't shadowed by a release problem).
//
// The deferred Release means a panic in fn doesn't strand the lock —
// Release runs, the panic propagates, and the kernel would release
// the lock anyway when the process exited.
func (f *Factory) WithLock(ctx context.Context, name string, fn func(context.Context) error, opts ...Option) (err error) {
	holder, acqErr := f.Acquire(ctx, name, opts...)
	if acqErr != nil {
		return acqErr
	}
	defer func() {
		relErr := holder.Release()
		if err == nil {
			err = relErr
		}
	}()
	return fn(ctx)
}

// WithLock acquires fl, runs fn, and releases. Standalone form for
// callers that don't have a [Factory].
func WithLock(ctx context.Context, fl *Lock, fn func(context.Context) error, opts ...Option) (err error) {
	holder, acqErr := fl.Acquire(ctx, opts...)
	if acqErr != nil {
		return acqErr
	}
	defer func() {
		relErr := holder.Release()
		if err == nil {
			err = relErr
		}
	}()
	return fn(ctx)
}
