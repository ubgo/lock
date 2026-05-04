package pglock

import (
	"context"

	"github.com/ubgo/lock"
)

// AsLocker returns f as a [lock.Locker] for backend-agnostic code.
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

type holderAdapter struct {
	h   *Holder
	ctx context.Context
}

func (a holderAdapter) Release() error {
	return a.h.ReleaseContext(a.ctx)
}
