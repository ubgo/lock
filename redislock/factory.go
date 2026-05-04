package redislock

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Factory holds the Redis client + shared options for many lock
// names. Construct one at app boot; each call site then names its
// lock and supplies any per-call overrides.
//
// A Factory is safe for concurrent use.
type Factory struct {
	rdb      redis.Scripter
	defaults config
}

// NewFactory returns a [Factory] with the given default options.
// Per-call options on each Acquire / WithLock override these defaults.
func NewFactory(rdb redis.Scripter, opts ...Option) *Factory {
	return &Factory{
		rdb:      rdb,
		defaults: applyOptions(defaultConfig(), opts),
	}
}

// Acquire takes a distributed lock on the given name. Returns a
// [Holder] on success, [ErrLocked] when the lock is held by another
// process, or any error from the Redis client.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	cfg := applyOptions(f.defaults, opts)
	return acquire(ctx, f.rdb, name, cfg)
}

// WithLock acquires the lock for name, runs fn, and releases. If
// the lock is already held it returns [ErrLocked] without calling
// fn. fn errors are returned; release errors only when fn returned
// nil.
func (f *Factory) WithLock(ctx context.Context, name string, fn func(context.Context) error, opts ...Option) (err error) {
	holder, acqErr := f.Acquire(ctx, name, opts...)
	if acqErr != nil {
		return acqErr
	}
	defer func() {
		relErr := holder.ReleaseContext(ctx)
		if err == nil {
			err = relErr
		}
	}()
	return fn(ctx)
}

// WithLock acquires fl, runs fn, releases. Standalone form for
// callers that don't have a [Factory].
func WithLock(ctx context.Context, fl *Lock, fn func(context.Context) error, opts ...Option) (err error) {
	holder, acqErr := fl.Acquire(ctx, opts...)
	if acqErr != nil {
		return acqErr
	}
	defer func() {
		relErr := holder.ReleaseContext(ctx)
		if err == nil {
			err = relErr
		}
	}()
	return fn(ctx)
}
