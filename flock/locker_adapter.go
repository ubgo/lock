package flock

import (
	"context"

	"github.com/ubgo/lock"
)

// AsLocker returns f as a [lock.Locker]. Use this when caller code
// wants to be backend-agnostic — accept a [lock.Locker] in your
// service constructor and the same code works against flock, filelock,
// redislock, etcdlock, or memlock.
//
// Per-call options cannot be passed through the [lock.Locker]
// interface (it has no opts argument by design). To use per-call
// options, hold on to the concrete *Factory and call its methods.
func (f *Factory) AsLocker() lock.Locker {
	return factoryAdapter{f: f}
}

// AsLocker returns this single-name lock as a [lock.Locker] whose
// Acquire ignores the supplied name (it always uses l.Name()).
// Useful when an API expects a Locker but you only have one specific
// lock to hand it.
func (l *Lock) AsLocker() lock.Locker {
	return lockAdapter{l: l}
}

type factoryAdapter struct {
	f *Factory
}

func (a factoryAdapter) Acquire(ctx context.Context, name string) (lock.Holder, error) {
	h, err := a.f.Acquire(ctx, name)
	if err != nil {
		if err == ErrLocked { //nolint:errorlint // exact sentinel match
			return nil, lock.ErrLocked
		}
		return nil, err
	}
	return holderAdapter{h: h}, nil
}

type lockAdapter struct {
	l *Lock
}

func (a lockAdapter) Acquire(ctx context.Context, _ string) (lock.Holder, error) {
	h, err := a.l.Acquire(ctx)
	if err != nil {
		if err == ErrLocked { //nolint:errorlint // exact sentinel match
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
