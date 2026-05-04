# pglock

> Postgres-backed distributed advisory lock. Session-tied —
> the kernel-equivalent crash safety, distributed across any process
> that can reach a shared Postgres.

```bash
go get github.com/ubgo/lock/pglock
```

Part of the [`ubgo/lock-*` family](#sister-modules).

## Quick start

```go
import (
    "context"
    "errors"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/ubgo/lock/pglock"
)

func main() {
    pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
    if err != nil { panic(err) }
    defer pool.Close()

    locks := pglock.NewFactory(pool)

    err = locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, pglock.ErrLocked) {
        // another process holds it; skip
        return
    }
}
```

## How it works

`pg_try_advisory_lock(key bigint)` is Postgres' built-in non-blocking
advisory lock. Every successful Acquire:

1. Pulls a dedicated connection out of the pool.
2. Runs `SELECT pg_try_advisory_lock($1)`.
3. Holds the connection for the lifetime of the lock.
4. On Release, runs `pg_advisory_unlock($1)` and returns the
   connection.

The lock is recorded against the **session** (i.e. the connection),
so when the connection drops — whether from a clean Release, a
Postgres restart, or the process crashing without saying goodbye —
Postgres releases the lock automatically. That's the killer feature:
distributed crash safety with no TTL to tune, no Sweep to run, no
PID probe.

## How does pglock compare?

| | This module | redislock | flock | filelock |
|---|---|---|---|---|
| Multi-host | ✅ | ✅ | ❌ | ❌ |
| Crash safety | Session close | TTL expiry | Kernel fd close | PID probe + Sweep |
| Need extra infra | Postgres | Redis | none | none |
| TTL to tune | none | yes | none | optional |
| Reentrant | ✅ (Postgres native) | ❌ | ❌ | ❌ |
| Strong consistency | ✅ (single primary) | weakly | n/a | n/a |

**Pick `pglock` when** you already run Postgres and want distributed
locking with the simplest possible failure model. The session-close
guarantee makes the operational story very short: nothing to clean
up, nothing to size, nothing to monitor.

## Reentrancy — the family-wide exception

Postgres advisory locks are reentrant by default: the same session
can acquire the same key twice and the lock is held with a counter
of 2. We don't fight this. If you call `Acquire` twice on the same
Holder's session, both succeed and you must Release twice.

Other modules in the family (filelock, flock, redislock, etcdlock)
are deliberately NOT reentrant — see filelock's MISSION §6 for why
(reentrancy hides design problems; the right fix is to refactor into
`xxxLocked()` private helpers, not to rely on library magic). pglock
inherits Postgres' behaviour rather than emulating non-reentrancy
on top.

## API at a glance

| Symbol | Purpose |
|---|---|
| `New(pool, name, opts...) *Lock` | Construct a single-name lock. |
| `NewFactory(pool, opts...) *Factory` | Multi-name factory. |
| `Lock.Acquire(ctx, opts...) (*Holder, error)` | Take the lock. |
| `Factory.Acquire(ctx, name, opts...) (*Holder, error)` | Same, factory-scoped. |
| `Factory.WithLock(ctx, name, fn, opts...) error` | Acquire → fn → Release. |
| `WithLock(ctx, fl, fn, opts...) error` | Standalone form. |
| `Factory.AsLocker() lock.Locker` | Backend-agnostic. |
| `Holder.Release() error` | Idempotent. Postgres also releases on session close. |
| `Holder.Key() int64` | Internal pg_advisory key (for `pg_locks` queries). |
| `WithKeyOffset(off)` | Namespace pglock's keyspace. |

## Examples

### Cron-singleton across N replicas of a service

```go
locks := pglock.NewFactory(pool)

// Each replica calls this. Only one wins; others skip.
err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, pglock.ErrLocked) {
    log.Info("already in progress on another replica")
    return nil
}
return err
```

### Inspecting a held lock

The Postgres `pg_locks` view tells you who's holding what:

```sql
SELECT pid, granted, objid
FROM pg_locks
WHERE locktype = 'advisory' AND objid = $1; -- holder.Key()
```

This is great for "is the cron actually running, or did somebody
forget to release?" debugging — no marker file to read, just SQL.

### Namespacing keyspace

If your app already uses `pg_advisory_lock` for other purposes, use
`WithKeyOffset` to keep pglock's keys distinct:

```go
// Reserve the upper 4 bits of the key space for ubgo/pglock.
const pglockKeyspace = 0x1000_0000_0000_0000
locks := pglock.NewFactory(pool, pglock.WithKeyOffset(pglockKeyspace))
```

### Backend-agnostic — accept `lock.Locker`

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

## Testing your code that uses pglock

Tests in this repo gate on `PGLOCK_TEST_DSN`:

```bash
PGLOCK_TEST_DSN="postgres://user:pass@localhost/db?sslmode=disable" go test ./...
```

CI runs the test suite against a postgres:16 service container.

## Sister modules

| Module | When to pick |
|---|---|
| **`ubgo/pglock`** *(this)* | Multi-host. Already running Postgres. |
| `ubgo/redislock` | Multi-host. AP semantics OK. |
| `ubgo/etcdlock` *(planned)* | Multi-host. Strongest consistency. |
| `ubgo/flock` | Single-host. Kernel-fenced. |
| `ubgo/filelock` | Single-host. Operator-readable marker. |
| `ubgo/locker` | Shared interface. Backend-agnostic code. |

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
