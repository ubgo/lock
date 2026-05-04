# redislock — guide

> Redis-backed distributed advisory lock. Atomic SET NX EX +
> Lua-guarded release. Fencing tokens via INCR. Single-master scope
> (Sentinel-friendly); not Redlock.

## When to use redislock

**Pick redislock when:**

- Multiple hosts need to coordinate — locks must work across
  machines.
- You **already run Redis** and don't want to add new infra.
- Your workload tolerates AP semantics (occasional split-brain
  during Redis failover is acceptable, idempotent, or rare).
- You need **fencing tokens** (`Holder.Token()` returns a per-name
  monotonic uint64) for downstream "reject stale writer"
  defenses.

**Don't pick redislock when:**

- You need rigorous correctness — use `etcdlock` (Raft-strong CP).
- You already run Postgres and want simpler ops — use `pglock`.
- You need quorum-correct multi-master Redis (Redlock) —
  deliberately not implemented; use `go-redsync/redsync` or
  switch to `etcdlock`.
- You don't have Redis — use `flock` (single-host) or `pglock`.

## Quickstart

```go
import (
    "context"
    "errors"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/ubgo/lock/redislock"
)

func main() {
    rdb := redis.NewClient(&redis.Options{Addr: "redis:6379"})
    locks := redislock.NewFactory(rdb,
        redislock.WithTTL(2 * time.Minute),
        redislock.WithKeyPrefix("myservice"),
    )

    ctx := context.Background()
    err := locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, redislock.ErrLocked) {
        return // another worker has the lock
    }
    if err != nil { panic(err) }
}
```

## How it works

Two Redis primitives, combined:

1. **Acquire**: `SET <key> <random> NX EX <ttl>`. Atomic in one
   round-trip. Server rejects the second caller's SET, so only
   one client wins. The `<random>` value is a 128-bit hex string —
   identifies THIS holder.
2. **Release**: a Lua script that runs `if GET key == myvalue then DEL`.
   Without this guard, a delayed Release from a holder whose TTL
   already expired could DELETE a successor's key — silently
   breaking the mutex. Lua makes the read+delete atomic on the
   server.
3. **Extend**: a Lua script that runs `if GET key == myvalue then
   PEXPIRE`. Same guard, for periodic lease renewal.
4. **Token**: a separate `INCR <key>:fence` after Acquire — gives
   you a monotonic uint64 per lock name.

### Crash recovery

If the holder process crashes (or pauses past its TTL), the Redis
key auto-expires. The next caller's `SET NX EX` succeeds. **No
PID probe, no Sweep — Redis's TTL is the recovery mechanism.**

## API reference

```go
// Construction
fl := redislock.New(rdb, "name", redislock.WithTTL(time.Minute))
locks := redislock.NewFactory(rdb, redislock.WithTTL(time.Minute))

// Acquire
holder, err := fl.Acquire(ctx)
holder, err := locks.Acquire(ctx, "name")
defer holder.Release()

// WithLock
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

// Holder methods
holder.Release()                        // Lua-guarded delete
holder.ReleaseContext(ctx)              // Like Release with explicit ctx
err := holder.Extend(ctx)               // Bump TTL back to full duration
token := holder.Token()                 // Per-name fencing token
key := holder.Key()                     // Underlying Redis key

// Backend-agnostic
var l lock.Locker = locks.AsLocker()
```

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithTTL(d time.Duration)` | 30s | Lock auto-expiry on Redis. |
| `WithKeyPrefix(s string)` | `redislock` | Namespace keys: `<prefix>:<name>`. |

### Errors

| Error | When |
|---|---|
| `ErrLocked` | Lock held by another holder. |
| `ErrLockLost` | Release/Extend found the key no longer holds our value (TTL expired or stolen). |

## TTL sizing — the central decision

TTL is the trade-off:

- **Too short** → healthy long-running jobs lose their lock to
  timeout, two holders can run in parallel.
- **Too long** → crash recovery takes that long.

Three patterns:

### 1. Conservative TTL (no extension)

Set TTL = 3× p99 healthy run time. Simplest; works for short jobs:

```go
locks := redislock.NewFactory(rdb, redislock.WithTTL(15*time.Minute))
// Healthy job runs in ~5min; TTL=15min is generous.
```

### 2. Tight TTL with auto-extend

For longer jobs, set TTL short and renew while the job runs:

```go
holder, _ := locks.Acquire(ctx, "long-export", redislock.WithTTL(2*time.Minute))
defer holder.Release()

go func() {
    t := time.NewTicker(time.Minute)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if err := holder.Extend(ctx); err != nil {
                cancel() // lock lost — abort
                return
            }
        }
    }
}()

return runLongExport(ctx)
```

Crash recovery is fast (~2min), but the renewal goroutine keeps
healthy jobs alive indefinitely.

### 3. TTL + fencing tokens

Fencing tokens are the defense-in-depth against the
TTL-expires-mid-job scenario. Even if redislock loses the lock
mid-job, the downstream consumer rejects the write because its
fence token is stale:

```go
holder, _ := locks.Acquire(ctx, "payment-export")
defer holder.Release()
fence := holder.Token()
s3.PutWithFence(ctx, "payments.csv", data, fence)
// Wrapper rejects writes with fence < highest seen.
```

## Use cases

### 1. Cron-singleton across N replicas

```go
locks := redislock.NewFactory(rdb, redislock.WithTTL(10*time.Minute))

err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, redislock.ErrLocked) {
    return nil // another replica is on it
}
return err
```

### 2. Long-running export with auto-extend

See [TTL sizing pattern 2](#2-tight-ttl-with-auto-extend) above.

### 3. Fencing — defend against GC pause

```go
holder, _ := locks.Acquire(ctx, "payment-export")
defer holder.Release()
fence := holder.Token()
return s3.PutWithFence(ctx, "payments/today.csv", data, fence)
```

### 4. Backend-agnostic — accept lock.Locker

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire redislock at startup:
//   svc := &Service{locks: redislock.NewFactory(rdb).AsLocker()}
```

## Operational notes

### Inspecting a held lock

```sh
$ redis-cli GET myservice:nightly-import
"4bd9...ab12"   # the holder's random identity
$ redis-cli TTL myservice:nightly-import
(integer) 87    # seconds remaining
$ redis-cli GET myservice:nightly-import:fence
"42"            # current fence counter
```

### Force-releasing a lock

```sh
$ redis-cli DEL myservice:nightly-import
```

The next holder's Release will return `ErrLockLost` because the
key is gone. That's correct — the original holder's "lock" was
stolen.

### Monitoring

Add `redislock.WithMetrics(...)` (planned for v0.2) to emit
acquire/release metrics. Currently you can observe by tracking
`ErrLockLost` in your application logs — it's a signal that
either TTL expiry or admin DEL is happening.

### Why not Redlock?

[Redlock](https://redis.io/docs/latest/develop/use-cases/distributed-locks/)
asks you to acquire on a quorum of independent Redis primaries.
Operationally complex, and its safety claims are
[contested by Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html).

For workloads that genuinely need quorum-correct distributed
locking, use `etcdlock` — Raft gives the consistency guarantees
Redlock approximates, with much simpler reasoning. For
cron-singleton / queue-dedup workloads (where the AP failure mode
is acceptable), single-master `redislock` is enough.

## Flaws

See [`docs/flaws.md` §redislock](../flaws.md#redislock) for the
full list. Highlights:

- **TTL trade-off is unavoidable** (mitigations: Extend, fencing).
- **Redis cluster failover can produce two holders** (fundamental
  to AP-Redis; use etcdlock for CP).
- **Lua-guarded Release still has a tiny window** between TTL
  expiry and Release where another holder could acquire — fence
  tokens close this gap downstream.
- **No Redlock multi-master** support — by design.

## Migration

From `bsm/redislock`, `go-redsync/redsync`, or other Redis lock
libraries — see [`docs/migration.md`](../migration.md).
