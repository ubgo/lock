// Package redislock is a Redis-backed distributed advisory lock for
// cooperating processes that may live on different machines.
//
// Mechanism: SET key value NX EX <ttl>. The atomic Redis primitive
// gives "set only if not exists, with TTL" in one round-trip — a
// crashed holder's lock is auto-reclaimed when the TTL expires.
// Release is a Lua script that deletes the key only if the value
// still matches the holder's token, so a delayed Release from a
// crashed (and TTL-reaped) holder cannot accidentally release a
// successor's lock.
//
// # Single-master scope
//
// This package targets a single Redis primary (or a Sentinel-managed
// primary). It deliberately does NOT implement Redlock-style multi-
// master quorum locking, which is operationally complex and whose
// safety claims are contested (Kleppmann's "How to do distributed
// locking" critique applies). For workloads that genuinely need
// multi-master correctness, reach for [github.com/ubgo/lock/etcdlock] —
// etcd's Raft-backed leases give the consistency guarantees that
// Redlock approximates.
//
// # When to choose redislock vs the rest of the family
//
//	┌──────────────────────────┬────────────────────────────────────────┐
//	│ ubgo/redislock           │ Multi-host. Already running Redis. AP. │
//	│ ubgo/etcdlock (planned)  │ Multi-host. Need strong consistency.   │
//	│ ubgo/pglock (planned)    │ Multi-host. Already running Postgres.  │
//	│ ubgo/flock               │ Single-host. Kernel-fenced.            │
//	│ ubgo/filelock            │ Single-host. Operator-readable marker. │
//	└──────────────────────────┴────────────────────────────────────────┘
//
// # Two ways to use it
//
//	rdb := redis.NewClient(&redis.Options{Addr: "redis:6379"})
//
//	// Standalone:
//	fl := redislock.New(rdb, "nightly-import", redislock.WithTTL(2*time.Minute))
//	holder, err := fl.Acquire(ctx)
//
//	// Or via a Factory for many lock names:
//	locks := redislock.NewFactory(rdb, redislock.WithTTL(2*time.Minute))
//	err := locks.WithLock(ctx, "import-orders", importOrders)
//
// # Fencing tokens
//
// Every successful Acquire returns a Holder whose Token is a
// monotonically increasing uint64 — backed by a Redis INCR on a
// per-name counter key. Use it downstream to reject writes from
// stale holders (the GC-pause-then-resume scenario).
package redislock

import "errors"

// ErrLocked is returned when the lock is currently held by another
// process. Callers typically check this with [errors.Is] and skip
// their work, rather than treating it as a hard error.
var ErrLocked = errors.New("redislock: already locked")

// ErrLockLost is returned by Holder.Release / Holder.Extend when the
// underlying Redis key no longer holds this holder's token — meaning
// the TTL expired (or some other process force-deleted the key) and
// somebody else may now be holding the lock under the same name. The
// caller should treat the protected work as "no longer guaranteed
// exclusive" and act accordingly (rollback, log, alert).
var ErrLockLost = errors.New("redislock: lock lost (TTL expired or stolen)")
