# pglock — guide

> Postgres-backed distributed advisory lock. Session-tied — the
> kernel-equivalent crash safety, distributed across any process
> that can reach a shared Postgres.

## When to use pglock

**Pick pglock when:**

- Multiple hosts need to coordinate.
- You **already run Postgres** for your application data.
- You want the simplest crash-recovery story in the family —
  Postgres releases the lock when the session disconnects, with
  no TTL to size, no Sweep to run, no PID probe.
- Strong-consistency single-primary semantics are enough (you're
  not running multi-primary Postgres).

**Don't pick pglock when:**

- You don't already run Postgres — adding it just for locking is
  rarely justified.
- You need **fencing tokens** (currently not implemented; planned).
- You need **semaphore mode** (N-holders) — `pg_advisory_lock` is
  binary; you'd need a custom approach.
- You need lockless multi-region distribution — Postgres is
  primary-bottlenecked.

## Quickstart

```go
import (
    "context"
    "errors"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/ubgo/lock/pglock"
)

func nightlyImport(ctx context.Context, pool *pgxpool.Pool) error {
    locks := pglock.NewFactory(pool)

    err := locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, pglock.ErrLocked) {
        return nil // another process holds it; skip
    }
    return err
}
```

## How it works

`pg_try_advisory_lock(key bigint)` is Postgres' native non-blocking
advisory lock. Every successful Acquire:

1. **Pulls a dedicated connection out of the pool** (`pool.Acquire(ctx)`).
2. Runs `SELECT pg_try_advisory_lock($1)` on that connection.
3. **Holds the connection** for the lifetime of the lock.
4. On Release, runs `pg_advisory_unlock($1)` and returns the
   connection to the pool.

The lock is recorded against the **session** (i.e. the
connection). When the connection drops — clean Release, process
crash, network drop, server restart — Postgres releases the
advisory lock automatically.

**That's the killer feature**: distributed crash safety with no
TTL to tune, no Sweep, no PID probe.

### String name → int64 key

`pg_advisory_lock` takes a bigint. Arbitrary string names are
hashed via FNV-1a into an int64 — collision-proof at any
realistic lock-name count (~1.8e19 keyspace).

### Reentrancy — the family-wide exception

Postgres advisory locks are **reentrant by default** — same
session can call `pg_advisory_lock(K)` twice and the lock is held
with a counter of 2. We don't fight Postgres; we document the
asymmetry instead.

Code that depends on reentrancy will silently break when ported
to `flock`/`redislock`/`etcdlock`. **If you might swap backends,
don't rely on pglock's reentrancy.**

## API reference

```go
// Construction
fl := pglock.New(pool, "name")
locks := pglock.NewFactory(pool)

// Acquire
holder, err := fl.Acquire(ctx)
holder, err := locks.Acquire(ctx, "name")
defer holder.Release()

// WithLock
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

// Holder methods
holder.Release()                // Idempotent
holder.ReleaseContext(ctx)      // With explicit ctx
holder.Key() int64              // Internal Postgres key (for pg_locks queries)

// Backend-agnostic
var l lock.Locker = locks.AsLocker()
```

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithKeyOffset(off int64)` | 0 | Add an offset to every name's hashed key. Use to namespace pglock from other `pg_advisory_lock` users in the same database. |

That's it. No TTL (sessions handle expiry), no key prefix string
(integer keyspace; use offset).

## Use cases

### 1. Cron-singleton across N replicas

```go
locks := pglock.NewFactory(pool)

err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, pglock.ErrLocked) {
    return nil // another replica owns it
}
return err
```

### 2. Inspecting a held lock from SQL

`pg_locks` shows everyone holding advisory locks:

```sql
SELECT pid, granted, objid
FROM pg_locks
WHERE locktype = 'advisory' AND objid = $1; -- holder.Key()
```

This is great for "is the cron actually running?" debugging — no
marker file to read, just SQL.

### 3. Namespacing keyspace

If your app already uses `pg_advisory_lock` for other purposes:

```go
const pglockKeyspace = 0x1000_0000_0000_0000
locks := pglock.NewFactory(pool, pglock.WithKeyOffset(pglockKeyspace))
```

All callers of the same lock name MUST pass the same offset.
Treat it as a deploy-time constant.

### 4. Backend-agnostic — accept lock.Locker

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire pglock for prod:
//   svc := &Service{locks: pglock.NewFactory(pool).AsLocker()}
//
// Wire memlock for tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

## Operational notes

### Connection pool sizing

Each `Holder` keeps a connection out of the pool for the lifetime
of the lock. If you hold many concurrent locks, you need a
correspondingly large pool. Math:

```
MaxConns ≥ max simultaneous Holders + headroom for normal queries
```

For most apps the existing pool is plenty. For services with
hundreds of simultaneous lock holders, increase `MaxConns`.

### Inspecting all advisory locks

```sql
SELECT pid, mode, locktype, classid, objid
FROM pg_locks
WHERE locktype = 'advisory';
```

Cross-reference with `pg_stat_activity`:

```sql
SELECT s.pid, s.application_name, s.state, l.objid
FROM pg_locks l
JOIN pg_stat_activity s USING (pid)
WHERE l.locktype = 'advisory';
```

### Force-releasing

You can run `SELECT pg_advisory_unlock_all()` to release all
advisory locks held by your session — but only YOUR session.
For administrators wanting to release someone else's lock,
terminate the holder's session:

```sql
SELECT pg_terminate_backend(<pid>);
```

The advisory lock releases automatically when the session ends.

### Debugging "stuck" locks

A "stuck" pglock usually means a connection that didn't close
cleanly. Check `pg_stat_activity` for connections that have been
idle for a long time. The session may be alive (holding the
lock) but doing nothing — your application has lost track of it.

Mitigation: set `pgxpool.Config.MaxConnIdleTime` to bound how
long idle connections stay open.

## Flaws

See [`docs/flaws.md` §pglock](../flaws.md#pglock) for the full
list. Highlights:

- **Reentrancy** — the family-wide exception. Don't depend on it
  if you might swap backends.
- **One connection per held lock** — pool sizing matters.
- **Postgres connection drops release the lock** silently — your
  code may keep running thinking it holds the lock. Mitigation:
  fencing tokens (currently not exposed; planned via `txid_current()`).
- **String → int64 hash collision** — possible but vanishingly
  rare (3e-8 at 1M names).
- **No fencing token currently exposed.**
- **No semaphore mode.**

## Migration

From raw `pg_advisory_lock` SQL or `nikolaydubina/go-pglock` —
see [`docs/migration.md`](../migration.md).
