package pglock

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Factory holds the pool + shared options for many lock names.
type Factory struct {
	pool     *pgxpool.Pool
	defaults config
}

// NewFactory returns a [Factory] backed by pool. Per-call options on
// each Acquire / WithLock override the factory defaults.
func NewFactory(pool *pgxpool.Pool, opts ...Option) *Factory {
	return &Factory{
		pool:     pool,
		defaults: applyOptions(defaultConfig(), opts),
	}
}

// Acquire takes a Postgres advisory lock on the given name.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	cfg := applyOptions(f.defaults, opts)
	return acquire(ctx, f.pool, name, cfg)
}

// WithLock acquires the lock for name, runs fn, releases. fn errors
// are returned; release errors only when fn returned nil.
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
