// Package filelock is a marker-file based mutex for cooperating processes
// on the same filesystem.
//
// It is *advisory*: every participant must agree to call Acquire before
// touching the protected resource. There is no kernel-enforced fencing —
// for that, reach for [github.com/ubgo/lock/flock] (planned) or syscall.Flock
// directly.
//
// The lock works by creating a sentinel file with O_CREATE|O_EXCL. If the
// file already exists Acquire returns [ErrLocked]. Release deletes the
// file. The atomicity of O_EXCL across local filesystems makes this safe
// for the "is anything running this job right now?" pattern.
//
// Filesystems where O_EXCL is not atomic (notably some NFS configurations
// and FAT32) may exhibit races. If you need to lock across machines, use
// a real distributed lock — Redis, etcd, or your database — not this.
//
// # Two ways to use it
//
// For one-off locks (CLI tools, tests) use [New] directly:
//
//	fl := filelock.New("oneoff", filelock.WithDir("/tmp"))
//	holder, err := fl.Acquire(ctx)
//	if errors.Is(err, filelock.ErrLocked) { return }
//	defer holder.Release()
//
// For services that lock many different names with shared config (the
// "28 cron singletons sharing one TempDir" case) use a [Factory]:
//
//	locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))
//	err := locks.WithLock(ctx, "syncjob", syncFn)
//
// # Interface compatibility
//
// Both [Lock] and [Factory] expose AsLocker which returns the shared
// [github.com/ubgo/lock.Locker] interface. Use it when you want to
// swap backends (filelock, flock, redislock, etc.) without changing
// caller code. If you only ever use filelock, ignore the interface
// and call methods on the concrete types directly — the package is
// usable standalone.
package filelock

import "errors"

// ErrLocked is returned by Acquire / WithLock when the lock is held by
// another live process. Callers typically check this with [errors.Is]
// and skip the work, rather than treating it as a hard error.
var ErrLocked = errors.New("filelock: already locked")
