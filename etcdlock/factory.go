package etcdlock

import (
	"context"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Factory holds the etcd client + shared options for many lock names.
type Factory struct {
	cli      *clientv3.Client
	defaults config
}

// NewFactory returns a [Factory] backed by cli.
func NewFactory(cli *clientv3.Client, opts ...Option) *Factory {
	return &Factory{
		cli:      cli,
		defaults: applyOptions(defaultConfig(), opts),
	}
}

// Acquire takes an etcd-backed distributed lock on name.
func (f *Factory) Acquire(ctx context.Context, name string, opts ...Option) (*Holder, error) {
	cfg := applyOptions(f.defaults, opts)
	return acquire(ctx, f.cli, name, cfg)
}

// WithLock acquires the lock for name, runs fn, releases.
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

// WithLock acquires fl, runs fn, releases. Standalone form.
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
