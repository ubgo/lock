// Package gocronlock adapts a [filelock.Factory] (or any
// [github.com/ubgo/lock.Locker]) into the [gocron.Locker]
// interface that [github.com/go-co-op/gocron/v2] expects on its
// scheduler's WithDistributedLocker option.
//
// # Why a separate contrib module
//
// The gocron module is a substantial dep tree (cron parser, clockwork,
// uuid). Most filelock users are not running gocron, so dragging that
// graph into every consumer would be antisocial. This adapter lives
// in its own nested Go module under `contrib/` — users that need it
// opt in by `go get`ing this module path; the core
// `github.com/ubgo/lock/filelock` go.mod stays minimal (just stdlib +
// `github.com/ubgo/lock`).
//
// # How to use it
//
//	import (
//	    "github.com/go-co-op/gocron/v2"
//	    "github.com/ubgo/lock/filelock"
//	    "github.com/ubgo/lock/contrib/gocronlock"
//	)
//
//	locks := filelock.NewFactory(filelock.WithDir("/var/run/myservice"))
//
//	scheduler, _ := gocron.NewScheduler(
//	    gocron.WithDistributedLocker(gocronlock.New(locks)),
//	)
//	scheduler.NewJob(
//	    gocron.DurationJob(time.Minute),
//	    gocron.NewTask(syncJob),
//	    gocron.WithName("sync-data"),
//	)
//	// gocron auto-locks each job by name — no manual filelock.WithLock
//	// needed at the job site.
//
// # What gocron expects
//
// gocron's Locker interface is similar to ubgo/lock.Locker but uses
// "Lock"/"Unlock" naming and threads ctx through Unlock as well. We
// adapt by accepting any [filelock.Factory] (or anything implementing
// [github.com/ubgo/lock.Locker]) and wrapping its Acquire / Release.
//
// gocron's contract: when Lock returns an error the job is skipped.
// We translate filelock.ErrLocked / lock.ErrLocked into the same
// error return — gocron treats any error as "skip this run".
package gocronlock

import (
	"context"

	"github.com/go-co-op/gocron/v2"
	"github.com/ubgo/lock"
)

// New returns a [gocron.Locker] backed by the given [lock.Locker].
// Pass [filelock.Factory.AsLocker]() — or any other backend's
// equivalent (memlock, redislock, etc.) — and gocron will see a
// uniform interface.
func New(l lock.Locker) gocron.Locker {
	return adapter{l: l}
}

type adapter struct {
	l lock.Locker
}

func (a adapter) Lock(ctx context.Context, key string) (gocron.Lock, error) {
	h, err := a.l.Acquire(ctx, key)
	if err != nil {
		return nil, err
	}
	return holderAdapter{h: h}, nil
}

type holderAdapter struct {
	h lock.Holder
}

// Unlock satisfies gocron.Lock. The ctx is ignored — lock.Holder.Release
// has no concept of cancellation; the underlying release is fast and
// local. If a future Holder implementation needs ctx (e.g. distributed
// release), this adapter would update accordingly.
func (h holderAdapter) Unlock(_ context.Context) error {
	return h.h.Release()
}
