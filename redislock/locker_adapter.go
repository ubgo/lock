package redislock

import (
	"context"

	"github.com/ubgo/lock"
)

// AsLocker returns f as a [lock.Locker]. Use this when caller code
// wants to be backend-agnostic — accept a [lock.Locker] in your
// service constructor and the same code works against redislock,
// flock, filelock, etcdlock, or memlock.
func (f *Factory) AsLocker() lock.Locker {
	return factoryAdapter{f: f}
}

// AsLocker returns this single-name lock as a [lock.Locker] whose
// Acquire ignores the supplied name (it always uses l.Name()).
func (l *Lock) AsLocker() lock.Locker {
	return lockAdapter{l: l}
}

type factoryAdapter struct {
	f *Factory
}

func (a factoryAdapter) Acquire(ctx context.Context, name string) (lock.Holder, error) {
	h, err := a.f.Acquire(ctx, name)
	if err != nil {
		if err == ErrLocked { //nolint:errorlint // exact sentinel
			return nil, lock.ErrLocked
		}
		return nil, err
	}
	return holderAdapter{h: h, ctx: ctx}, nil
}

type lockAdapter struct {
	l *Lock
}

func (a lockAdapter) Acquire(ctx context.Context, _ string) (lock.Holder, error) {
	h, err := a.l.Acquire(ctx)
	if err != nil {
		if err == ErrLocked { //nolint:errorlint // exact sentinel
			return nil, lock.ErrLocked
		}
		return nil, err
	}
	return holderAdapter{h: h, ctx: ctx}, nil
}

// holderAdapter bridges *Holder.Release into the simpler
// lock.Holder.Release() signature. The original ctx from Acquire
// is captured so Release uses it for the cleanup Redis call —
// callers that want a different ctx for cleanup should hold the
// concrete *Holder and call ReleaseContext directly.
type holderAdapter struct {
	h   *Holder
	ctx context.Context
}

func (a holderAdapter) Release() error {
	return a.h.ReleaseContext(a.ctx)
}
