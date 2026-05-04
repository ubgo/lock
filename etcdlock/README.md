# etcdlock

> etcd-backed distributed advisory lock with Raft-strong consistency.
> Lease-based crash safety, mod_revision fencing tokens, FIFO fairness
> via etcd's `concurrency.Mutex`.

```bash
go get github.com/ubgo/lock/etcdlock
```

Part of the [`ubgo/lock-*` family](#sister-modules).

## Quick start

```go
import (
    "context"
    "errors"
    "time"

    clientv3 "go.etcd.io/etcd/client/v3"
    "github.com/ubgo/lock/etcdlock"
)

func main() {
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   []string{"etcd-1:2379", "etcd-2:2379", "etcd-3:2379"},
        DialTimeout: 5 * time.Second,
    })
    if err != nil { panic(err) }
    defer cli.Close()

    locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(60*time.Second))

    err = locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, etcdlock.ErrLocked) {
        return // another holder; skip
    }
}
```

## How it works

Built on top of `go.etcd.io/etcd/client/v3/concurrency`:

1. **Acquire** creates a new etcd Session (== lease + auto-keepalive
   goroutine) with the configured TTL.
2. Constructs a `concurrency.Mutex` over a per-name prefix.
3. Calls `Mutex.TryLock(ctx)` — non-blocking; returns
   `concurrency.ErrLocked` if somebody else owns the prefix.
4. Translates that into our family's `etcdlock.ErrLocked`.
5. Captures the response's `mod_revision` as the fencing token.

The session's lease is auto-keep-alived by the etcd client while the
holder lives. If the holder process crashes (or partitions away from
the etcd cluster long enough), the lease expires after the TTL and
etcd deletes the holder's key automatically — the next Acquire wins.

## Why etcdlock vs the rest of the family?

| | This module | redislock | pglock |
|---|---|---|---|
| Consistency | ✅ Raft (CP) | weakly (AP) | ACID via single primary |
| Fencing tokens | mod_revision (globally monotonic) | Per-name INCR | n/a (session-tied) |
| Need extra infra | etcd cluster | Redis | Postgres |
| TTL to tune | yes | yes | none (session) |
| FIFO fairness | ✅ via concurrency.Mutex | ❌ | ❌ |

**Pick `etcdlock` when** correctness across a multi-host cluster is
the primary requirement — you'd rather an Acquire fail clean than
accidentally let two holders run. Raft makes the safety story
unambiguous.

## API at a glance

| Symbol | Purpose |
|---|---|
| `New(cli, name, opts...) *Lock` | Construct a single-name lock. |
| `NewFactory(cli, opts...) *Factory` | Multi-name factory. |
| `Lock.Acquire(ctx, opts...) (*Holder, error)` | Take the lock. |
| `Factory.Acquire(ctx, name, opts...) (*Holder, error)` | Same, factory-scoped. |
| `Factory.WithLock(ctx, name, fn, opts...) error` | Acquire → fn → Release. |
| `WithLock(ctx, fl, fn, opts...) error` | Standalone form. |
| `Factory.AsLocker() lock.Locker` | Backend-agnostic. |
| `Holder.Release() error` | Idempotent. Lease also expires on TTL. |
| `Holder.Token() uint64` | etcd mod_revision (globally monotonic fence). |
| `Holder.Key() string` | Underlying etcd key. |
| `WithTTL(d)` | Lease TTL. Default 30s. Min 5s. |
| `WithKeyPrefix(s)` | Default `/ubgo/etcdlock`. |

## Examples

### Cron-singleton across replicas

```go
locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(30*time.Second))

err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, etcdlock.ErrLocked) {
    return nil // another replica owns it
}
return err
```

### Globally-monotonic fencing

etcd's `mod_revision` is monotonic across the entire etcd cluster
— a stronger guarantee than per-name INCR. Useful for downstream
"reject stale write" patterns where multiple lock names need to be
totally ordered:

```go
holder, _ := locks.Acquire(ctx, "payment-export-1")
defer holder.Release()
fence1 := holder.Token() // e.g. 4172

holder2, _ := locks.Acquire(ctx, "payment-export-2")
defer holder2.Release()
fence2 := holder2.Token() // > 4172, even though it's a different lock name
```

### Backend-agnostic — accept `lock.Locker`

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire etcdlock for prod:
//   svc := &Service{locks: etcdlock.NewFactory(cli).AsLocker()}
//
// Wire memlock for tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

## Testing your code that uses etcdlock

Tests in this repo gate on `ETCDLOCK_TEST_ENDPOINTS`:

```bash
ETCDLOCK_TEST_ENDPOINTS="localhost:2379" go test ./...
```

CI runs against an etcd:v3.5 service container.

## Sister modules

| Module | When to pick |
|---|---|
| **`ubgo/etcdlock`** *(this)* | Multi-host. Strong consistency. Can run an etcd cluster. |
| `ubgo/redislock` | Multi-host. AP semantics OK. |
| `ubgo/pglock` | Multi-host. Already running Postgres. |
| `ubgo/flock` | Single-host. Kernel-fenced. |
| `ubgo/filelock` | Single-host. Operator-readable marker. |
| `ubgo/locker` | Shared interface. Backend-agnostic code. |

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
