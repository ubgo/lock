# etcdlock — guide

> etcd-backed distributed advisory lock with Raft-strong
> consistency. Lease-based crash safety, mod_revision fencing
> tokens, FIFO fairness via etcd's `concurrency.Mutex`.

## When to use etcdlock

**Pick etcdlock when:**

- You need rigorous correctness across multiple hosts. You'd
  rather an Acquire fail clean than risk two holders.
- You **already run etcd** (e.g. Kubernetes etcd, service
  discovery, leader election).
- You want **globally-monotonic fencing tokens** — etcd's
  `mod_revision` is monotonic across the entire cluster, not
  just per-name.
- You want FIFO fairness — etcd's `concurrency.Mutex` queues
  waiters in insertion order (we use TryLock to preserve
  non-blocking semantics, but the underlying primitive supports
  it).

**Don't pick etcdlock when:**

- You don't already run etcd. The dep tree is heavy (50+
  transitive deps for grpc + protobuf + etcd-client). For
  most workloads you can start with `flock` / `filelock` /
  `pglock` / `redislock` and only graduate to etcdlock when
  you have an etcd cluster for other reasons.
- You want minimal infra. Operating etcd correctly (≥3 nodes,
  monitoring, snapshots, version upgrades) is a real cost.
- AP semantics are fine — `redislock` is simpler.

## Quickstart

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

1. **Acquire** creates a new etcd Session (= lease + auto-keepalive
   goroutine) with the configured TTL.
2. Constructs a `concurrency.Mutex` over a per-name prefix.
3. Calls `Mutex.TryLock(ctx)` — non-blocking; returns
   `concurrency.ErrLocked` if somebody else owns the prefix.
4. Translates that into the family's `etcdlock.ErrLocked`.
5. Captures the response's `mod_revision` as the fencing token.

The session's lease is **auto-keep-alived** by the etcd client
while the holder lives. Healthy holders never lose the lock.
If the holder process crashes (or partitions away from etcd
long enough), the lease expires after the TTL and etcd deletes
the key automatically — the next Acquire wins.

### mod_revision as fencing token

etcd's `mod_revision` is the global revision number of the
write that created your lock key. **Monotonic across the entire
etcd cluster** — not just per-name. This is a stronger fencing
guarantee than per-name INCR (Redis) or per-marker counter
(filelock).

Use case: if you have multiple lock names whose downstream
writes need to be totally ordered, etcdlock's mod_revision
gives you that ordering for free.

## API reference

```go
// Construction
fl := etcdlock.New(cli, "name")
locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(30*time.Second))

// Acquire
holder, err := fl.Acquire(ctx)
holder, err := locks.Acquire(ctx, "name")
defer holder.Release()

// WithLock
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

// Holder methods
holder.Release()                // Unlock + close session
holder.ReleaseContext(ctx)      // With explicit ctx
token := holder.Token()         // mod_revision (uint64, globally monotonic)
key := holder.Key()             // Underlying etcd key

// Backend-agnostic
var l lock.Locker = locks.AsLocker()
```

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithTTL(d time.Duration)` | 30s (min 5s) | Lease TTL. Auto-keep-alived during healthy operation. |
| `WithKeyPrefix(s string)` | `/ubgo/etcdlock` | Namespace etcd keys. |

## Use cases

### 1. Cron-singleton across replicas

```go
locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(30*time.Second))

err := locks.WithLock(ctx, "midnight-billing", processBilling)
if errors.Is(err, etcdlock.ErrLocked) {
    return nil
}
return err
```

### 2. Globally-monotonic fencing across multiple lock names

```go
holder, _ := locks.Acquire(ctx, "payment-export-1")
defer holder.Release()
fence1 := holder.Token() // e.g. 4172

holder2, _ := locks.Acquire(ctx, "payment-export-2")
defer holder2.Release()
fence2 := holder2.Token() // > 4172, even though it's a different lock name
```

This is **not possible** with redislock (per-name INCR) or
filelock (per-name sidecar). Use etcdlock when you need a
total ordering across multiple resources.

### 3. Backend-agnostic — accept lock.Locker

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire etcdlock for prod (CP):
//   svc := &Service{locks: etcdlock.NewFactory(cli).AsLocker()}
//
// Wire memlock for tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

## Operational notes

### Inspecting a held lock

```sh
$ etcdctl get /ubgo/etcdlock/nightly-import/ --prefix
/ubgo/etcdlock/nightly-import/694d8c... 4bd9...ab12
$ etcdctl lease list
694d8c4f06b5e7c1   ttl 24
```

Each lock has a unique sub-key under the per-name prefix; the
key value is the holder's session ID. The associated lease ID
shows TTL.

### Force-releasing a lock

```sh
$ etcdctl lease revoke <lease-id>
```

The session ends; etcd deletes the key. Next Acquire wins.

### TTL sizing

Same trade-off as redislock:

- Too short → healthy holders lose locks during transient
  partitions or GC pauses.
- Too long → slow crash recovery.

The auto-keepalive in `concurrency.Session` mitigates this for
healthy holders — typical TTL of 30–60s is plenty. If your
holder is partitioned from etcd for longer than the TTL,
the lock is reclaimed (and the holder's `Holder.Token()`
becomes stale, defended downstream by fencing).

### Network partitions

If your process is partitioned from the etcd cluster long
enough for the lease to expire, etcd removes your key. The
next Acquire elsewhere wins. When your network heals, your
code still thinks it holds the lock — identical to the
redislock case but **stronger guarantees during the partition**:
Raft prevents a flapping primary from losing your lock as long
as you're connected to a majority.

`Holder.Token()` (mod_revision) is the defense. The downstream
consumer rejects writes with `token < highest seen` — including
writes from a partitioned-and-recovered holder.

### Dep tree footprint

Importing etcdlock pulls grpc, protobuf, golang.org/x/net,
golang.org/x/sys, golang.org/x/text, google.golang.org/genproto,
go.uber.org/zap, and a handful of others — about 50 transitive
deps total. By far the largest in the family. Don't add etcdlock
to a service that doesn't already use etcd.

## Flaws

See [`docs/flaws.md` §etcdlock](../flaws.md#etcdlock) for the
full list. Highlights:

- **Heavy dependency footprint** — 50+ transitive deps.
- **TTL trade-off still applies** despite auto-keepalive (network
  partitions can outlast TTL).
- **Network partitions can cause `ErrLockLost`-like situations** —
  defense is fencing tokens (which etcdlock has built-in).
- **Lock keys persist on TTL cleanup but mod_revision keeps
  growing globally** — fencing remains correct across crashes.
- **Etcd cluster operations matter** — running etcd well is its
  own discipline. Don't add this lib if you don't already run
  etcd.

## Migration

From `concurrency.Mutex` directly — see
[`docs/migration.md`](../migration.md). The wrapper is mostly
ergonomics: a Factory pattern, a `WithLock(ctx, name, fn)` helper,
and the family's `ErrLocked` sentinel.
