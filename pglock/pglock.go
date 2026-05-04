// Package pglock is a Postgres advisory-lock-backed distributed
// advisory lock for cooperating processes that share a Postgres
// instance.
//
// Mechanism: pg_try_advisory_lock(key bigint). The Postgres server
// records the lock against the connection's session — when the
// session disconnects (process crash, network drop, server restart),
// Postgres releases the lock automatically. That makes pglock the
// "distributed flock": kernel-level crash safety, but for any process
// that can reach a shared Postgres instance.
//
// # When to choose pglock vs the rest of the family
//
//	┌──────────────────────────┬────────────────────────────────────────┐
//	│ ubgo/pglock              │ Multi-host. Already running Postgres.  │
//	│                          │ Free crash safety from session close.  │
//	│ ubgo/redislock           │ Multi-host. AP semantics OK.           │
//	│ ubgo/etcdlock (planned)  │ Multi-host. Strongest consistency.     │
//	│ ubgo/flock               │ Single-host. Kernel-fenced.            │
//	│ ubgo/filelock            │ Single-host. Operator-readable marker. │
//	└──────────────────────────┴────────────────────────────────────────┘
//
// # Reentrancy — the family-wide exception
//
// Postgres advisory locks are reentrant by default: the same session
// can call pg_advisory_lock(K) twice and the lock is held with a
// counter of 2. We don't fight this. If you call Acquire twice for
// the same name on the same session/Holder, both will succeed and
// you must call Release twice to fully drop the lock.
//
// Other modules in the ubgo/lock-* family (filelock, flock,
// redislock, etcdlock) are deliberately NOT reentrant — see
// MISSION §6 for the design discussion. pglock inherits Postgres'
// behaviour rather than emulating non-reentrancy on top.
//
// # Lock keys
//
// pg_advisory_lock takes a bigint (or two int4s). Arbitrary string
// names are hashed via FNV-1a into an int64 — collision-proof at
// realistic lock-name counts. If two different names hash to the
// same bigint, callers will see ErrLocked when they shouldn't; the
// 64-bit hash makes that effectively impossible for human-chosen
// names.
//
// Set [WithKeyOffset] to namespace pglock's keyspace from any other
// pg_advisory_lock users in your database — values like
// `0x1000000000000000` keep your lock IDs distinct from anyone else's.
//
// # Two ways to use it
//
//	pool, _ := pgxpool.New(ctx, dsn)
//
//	// Standalone:
//	fl := pglock.New(pool, "nightly-import")
//	holder, err := fl.Acquire(ctx)
//
//	// Or via a Factory for many lock names:
//	locks := pglock.NewFactory(pool)
//	err := locks.WithLock(ctx, "import-orders", importOrders)
package pglock

import "errors"

// ErrLocked is returned when the lock is held by another session.
// Callers typically check this with [errors.Is] and skip their work.
var ErrLocked = errors.New("pglock: already locked")
