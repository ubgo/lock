# redislock

> Redis-backed distributed advisory lock. Atomic SET NX EX +
> Lua-guarded release. Fencing tokens via INCR. Single-master scope
> (Sentinel-friendly); not Redlock.

```bash
go get github.com/ubgo/lock/redislock
```

Part of the [`ubgo/lock-*` family](#sister-modules).

## Quick start

```go
import (
    "context"
    "errors"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/ubgo/lock/redislock"
)

func main() {
    rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    locks := redislock.NewFactory(rdb,
        redislock.WithTTL(2 * time.Minute),
        redislock.WithKeyPrefix("myservice"),
    )

    ctx := context.Background()
    err := locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, redislock.ErrLocked) {
        // another worker has the lock; skip
        return
    }
    if err != nil {
        panic(err)
    }
}
```

## How it works

Redis offers two primitives that, combined, give a correct distributed
lock:

1. **`SET key value NX EX ttl`** — atomic "set only if absent, with
   expiry" in one round-trip. The server rejects the second caller's
   SET, so only one client wins per name.
2. **A Lua script for release** — `if GET key == myvalue then DEL`.
   Without this guard, a delayed Release from a holder whose TTL
   already expired could blow away a successor's lock. The Lua
   script makes the read+delete atomic on the server.

Each Holder gets a 128-bit random `value` written into the key, so two
holders are never confused. Fencing tokens come from a sibling key
incremented via `INCR` — monotonic across all holders of a given name.

## Crash recovery

If the holder process crashes (or pauses past its TTL), the Redis key
expires automatically. The next caller's `SET NX EX` succeeds. No PID
probe, no Sweep — Redis's TTL is the recovery mechanism.

The trade-off: TTL must be longer than the longest healthy run of the
protected work, otherwise the work outlives the lock and a successor
starts in parallel. For long-running jobs, call `Holder.Extend(ctx)`
periodically (or run a background renewal goroutine — planned for
v0.2).

## Why not Redlock?

[Redlock](https://redis.io/docs/latest/develop/use-cases/distributed-locks/)
asks you to acquire on a quorum of independent Redis primaries. It
adds significant operational complexity and its safety claims are
[contested by Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html).

For workloads that genuinely need quorum-correct distributed locking,
use `ubgo/etcdlock` — Raft gives the consistency guarantees Redlock
approximates, with much simpler reasoning. For workloads where AP
single-master is fine (most cron-singleton, queue-worker, and
deduplication cases), this package is enough.

## API at a glance

| Symbol | Purpose |
|---|---|
| `New(rdb, name, opts...) *Lock` | Construct a single-name lock. |
| `NewFactory(rdb, opts...) *Factory` | Construct a multi-name factory. |
| `Lock.Acquire(ctx, opts...) (*Holder, error)` | Take the lock. |
| `Factory.Acquire(ctx, name, opts...) (*Holder, error)` | Same, factory-scoped. |
| `Factory.WithLock(ctx, name, fn, opts...) error` | Acquire → fn → Release. |
| `WithLock(ctx, fl, fn, opts...) error` | Standalone form for `*Lock`. |
| `Factory.AsLocker() lock.Locker` | Backend-agnostic interface. |
| `Holder.Release() error` | Lua-guarded delete; returns `ErrLockLost` if TTL expired. |
| `Holder.ReleaseContext(ctx) error` | Like Release with explicit ctx. |
| `Holder.Extend(ctx) error` | Bump the TTL back to its full duration. |
| `Holder.Token() uint64` | Monotonic fencing token (Redis INCR). |
| `Holder.Key() string` | Underlying Redis key. |
| `WithTTL(d)` | Default 30s. |
| `WithKeyPrefix(s)` | Default `"redislock"`. |

## Examples

### Cron-singleton across N replicas

Three replicas of the same service all run the cron at midnight; only
one should actually do the work:

```go
locks := redislock.NewFactory(rdb, redislock.WithTTL(10*time.Minute))

// Each replica does this. Only one wins; others skip.
err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, redislock.ErrLocked) {
    log.Info("billing run already in progress on another replica")
    return nil
}
return err
```

### Long-running job — extend the lease periodically

If your job legitimately runs for hours and you can't size TTL up that
much (because crash recovery needs to be quick), use `Extend`:

```go
holder, err := locks.Acquire(ctx, "long-export", redislock.WithTTL(2*time.Minute))
if err != nil { return err }
defer holder.Release()

// Renew every minute (well within the 2-minute TTL).
go func() {
    t := time.NewTicker(time.Minute)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if err := holder.Extend(ctx); err != nil {
                // Lock lost — abort the protected work.
                cancel()
                return
            }
        }
    }
}()

return runLongExport(ctx) // honours ctx cancellation
```

### Fencing tokens

The Kleppmann scenario: process A acquires, gets paused for a long
time (GC, swap, ctrl-Z), TTL expires, B acquires, A wakes up and
tries to write with stale data. Fencing tokens stop A's write
downstream:

```go
holder, _ := locks.Acquire(ctx, "payment-export")
defer holder.Release()
fence := holder.Token()

if err := s3.PutWithFence(ctx, "payments/today.csv", data, fence); err != nil {
    return err
}
// downstream wrapper rejects writes with token <= highest seen
```

### Backend-agnostic — accept `lock.Locker`

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire in main.go:
//   svc := &Service{locks: redislock.NewFactory(rdb).AsLocker()}
//
// Wire in tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

## Sister modules

| Module | When to pick |
|---|---|
| **`ubgo/redislock`** *(this module)* | Multi-host. Already running Redis. AP semantics OK. |
| `ubgo/etcdlock` *(planned)* | Multi-host. Need strong consistency. |
| `ubgo/pglock` *(planned)* | Multi-host. Already running Postgres. |
| `ubgo/flock` | Single-host. Kernel-fenced. |
| `ubgo/filelock` | Single-host. Operator-readable marker. |
| `ubgo/locker` | Shared interface. Import to write backend-agnostic code. |

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
