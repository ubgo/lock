// Package flock is a kernel-fenced advisory lock for cooperating
// processes on the same machine.
//
// Where [github.com/ubgo/lock/filelock] uses a marker file with O_EXCL
// semantics (advisory at the userspace level — a crashed holder leaves
// a stale marker until something cleans it up), flock uses the OS's
// native advisory locking primitive: flock(2) on Unix,
// LockFileEx on Windows. The kernel tracks the lock per file
// descriptor, and **automatically releases it when the process exits**
// — crashed or otherwise. No PID probe, no stale window, no Sweep:
// the kernel does the bookkeeping for you.
//
// # When to choose flock vs filelock
//
//	┌──────────────────────────┬─────────────────┬─────────────────────┐
//	│ Concern                  │ ubgo/flock      │ ubgo/filelock        │
//	├──────────────────────────┼─────────────────┼─────────────────────┤
//	│ Crash safety             │ kernel          │ PID probe + Sweep   │
//	│ Cross-host (NFS)         │ unreliable      │ explicit modes      │
//	│ Operator visibility      │ no marker body  │ rich marker fields  │
//	│ Semaphore (N holders)    │ planned         │ ✅                  │
//	│ Fencing tokens           │ planned         │ ✅                  │
//	│ Native API across family │ ✅ same shape   │ ✅ same shape       │
//	└──────────────────────────┴─────────────────┴─────────────────────┘
//
// Use flock when your highest priority is "the lock is gone the
// instant my process dies, no manual cleanup, no operator pages at
// 3am". Use filelock when you want operators to inspect a marker file
// and see who held the lock and when.
//
// # Two ways to use it
//
// For one-off locks (CLI tools, tests):
//
//	fl := flock.New("nightly-import", flock.WithDir("/var/run"))
//	holder, err := fl.Acquire(ctx)
//	if errors.Is(err, flock.ErrLocked) { return }
//	defer holder.Release()
//
// For services that lock many names with shared config, use a Factory:
//
//	locks := flock.NewFactory(flock.WithDir(cfg.LockDir))
//	err := locks.WithLock(ctx, "import-orders", importOrders)
//
// # Interface compatibility
//
// Both [Lock] and [Factory] expose AsLocker which returns the shared
// [github.com/ubgo/lock.Locker] interface. Use it when you want to
// swap backends (filelock, flock, redislock, etc.) without changing
// caller code.
package flock

import "errors"

// ErrLocked is returned by Acquire / WithLock when the lock is held
// by another process. Callers typically check this with [errors.Is]
// and skip their work, rather than treating it as a hard error.
var ErrLocked = errors.New("flock: already locked")
