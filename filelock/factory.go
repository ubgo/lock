package filelock

import "context"

// Factory holds shared configuration for many lock names. Construct one
// at app boot with the cross-cutting options (typically [WithDir]); each
// call site then names its lock and supplies any per-call overrides.
//
// A Factory is safe for concurrent use. The construction-time options
// are immutable after [NewFactory] returns; per-call options never
// mutate the factory's defaults.
type Factory struct {
	defaults config
}

// NewFactory returns a [Factory] with the given default options applied.
// Every Acquire / WithLock call made through this factory will see those
// options as its starting configuration; per-call options override them.
//
// Resolution order:
//
//	library default → factory default → per-call option
//
// Pass nil opts safely — they are skipped.
func NewFactory(opts ...Option) *Factory {
	return &Factory{
		defaults: applyOptions(defaultConfig(), opts),
	}
}

// Acquire creates the marker file for name and returns a [Holder] on
// success. Returns [ErrLocked] if the lock is already held; any other
// error indicates a filesystem problem.
//
// Per-call opts override the factory's defaults for this Acquire only.
// The factory itself is unchanged.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	cfg := applyOptions(f.defaults, opts)
	return acquire(ctx, name, cfg)
}

// WithLock acquires the lock for name, runs fn, and releases the lock.
// The release happens via defer so it runs even if fn panics; the panic
// is re-raised after Release.
//
// If the lock is already held WithLock returns [ErrLocked] without
// calling fn. Any other Acquire error is also returned without calling
// fn. Errors from fn are returned to the caller; release errors are
// surfaced only when fn returned nil (so a successful job doesn't lose
// information about cleanup failure, but a failing job's error is not
// shadowed by a release problem).
//
// Per-call opts apply to the underlying Acquire.
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

// WithLock acquires fl, runs fn, and releases. See [Factory.WithLock]
// for the error-merging semantics; this is the standalone-Lock form for
// callers that do not have a [Factory].
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
