# Within-family comparison

Side-by-side comparison of every backend in `ubgo/lock`. If
[`comparison.md`](./comparison.md) tells you why to pick the family
over other libraries, this page tells you which backend within the
family fits your situation.

## Capability matrix

| Capability | filelock | flock | memlock | redislock | pglock | etcdlock |
|---|---|---|---|---|---|---|
| **Scope** | single host | single host | single process | multi-host | multi-host | multi-host |
| **Crash recovery** | PID probe + stale window + Sweep | Kernel — instant | n/a (process dies = state gone) | TTL expiry | Session disconnect | Lease expiry |
| **Recovery latency** | instant (probe) or up to stale window | **immediate** | n/a | up to TTL | **immediate** | up to TTL |
| **Need extra infra** | none (filesystem) | none (filesystem) | none | Redis | Postgres | etcd cluster |
| **Cross-platform** | linux/macos/windows | linux/macos/bsd/windows | all | all | all | all |
| **NFS / shared FS safe** | ⚠️ depends on NFS version | ⚠️ depends on NFS version | n/a | yes | yes | yes |
| **Strong consistency (CP)** | local-fs only | local-fs only | n/a | ❌ AP | ✅ single primary | ✅ Raft |
| **TTL to tune** | optional (`WithStaleAfter`) | none | none | yes | none | yes |
| **Auto-renewal needed** | no | no | no | yes (long jobs) | no | no (auto-keepalive) |
| **Wait/block** | ❌ non-blocking | ❌ non-blocking | ❌ non-blocking | ❌ non-blocking | ❌ non-blocking | ❌ non-blocking (TryLock) |
| **Reentrant** | ❌ | ❌ | ❌ | ❌ | ✅ (PG native) | ❌ |
| **Fencing tokens** | ✅ per-name sidecar | ✅ per-name sidecar | ✅ per-name atomic | ✅ per-name INCR | ✅ per-session txid | ✅ **mod_revision (global)** |
| **Token scope** | per-name | per-name | per-name | per-name | per-Postgres-instance | **global cluster** |
| **Token strict monotonic** | ✅ singleton, ⚠️ semaphore | ✅ singleton, ⚠️ semaphore | ✅ | ✅ | ✅ | ✅ |
| **Semaphore (N holders)** | ✅ `WithMaxConcurrent` | ✅ `WithMaxConcurrent` | ✅ | ✅ `WithMaxConcurrent` | ✅ `WithMaxConcurrent` | ✅ `WithMaxConcurrent` |
| **Sweep / cleanup** | ✅ `Factory.Sweep` | n/a (kernel) | n/a | n/a (TTL) | n/a (session) | n/a (lease) |
| **Operator-readable** | ✅ rich marker fields | ❌ empty file | n/a | `redis-cli GET <key>` | `pg_locks` view | `etcdctl get <key>` |
| **TraceID propagation** | ✅ marker debug field | ✅ slog field | n/a | ✅ embedded in SET value | ✅ via `application_name` | ✅ in lock key value |
| **Observability hooks (slog/Prom/OTel)** | ✅ `WithLogger`/`WithMetrics`/`WithSpanStarter` | ✅ same | n/a | ✅ same | ✅ same | ✅ same |
| **gocron compatibility** | ✅ via `contrib/gocronlock` | ✅ same | ✅ same | ✅ same | ✅ same | ✅ same |
| **`lock.Locker` interface** | ✅ `AsLocker()` | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Module size (transitive deps)** | small (1) | small (1) | tiny (1) | medium (~5) | medium (~7) | **large (~50)** |
| **License** | Apache-2.0 | Apache-2.0 | Apache-2.0 | Apache-2.0 | Apache-2.0 | Apache-2.0 |

Legend: ✅ supported · ❌ not supported · ⚠️ caveats · n/a not applicable

## API surface — what's the same, what differs

### What's identical across every backend

```go
// Construction:
locks := <backend>.NewFactory(<deps>, <options>...)

// Acquire:
holder, err := locks.Acquire(ctx, "name", <opts>...)

// WithLock helper:
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
}, <opts>...)

// Release:
holder.Release()

// Standalone Lock:
fl := <backend>.New(<deps>, "name", <opts>...)
holder, err := fl.Acquire(ctx)

// Backend-agnostic:
var l lock.Locker = locks.AsLocker()
```

The only differences across backends are:

1. **What `<deps>` you pass** — `filelock`/`flock`/`memlock`: nothing.
   `redislock`: `*redis.Client`. `pglock`: `*pgxpool.Pool`. `etcdlock`:
   `*clientv3.Client`.
2. **Which options exist** — `filelock` has the most (`WithStaleAfter`,
   `WithMaxConcurrent`, observability hooks); `flock` has only
   `WithDir`; `redislock` has `WithTTL` + `WithKeyPrefix`; etc.
3. **Backend-specific holder methods** — `redislock.Holder` has
   `Extend(ctx)`, `etcdlock.Holder.Token()` returns mod_revision, etc.

### Same code, different backends

The same business logic with different backends. Note the
`<backend>` is the only part that changes:

```go
// filelock
locks := filelock.NewFactory(filelock.WithDir("/var/run/myservice"))
err := locks.WithLock(ctx, "nightly-import", runImport)

// flock
locks := flock.NewFactory(flock.WithDir("/var/run/myservice"))
err := locks.WithLock(ctx, "nightly-import", runImport)

// memlock
locks := memlock.NewFactory()
err := locks.WithLock(ctx, "nightly-import", runImport)

// redislock
locks := redislock.NewFactory(rdb, redislock.WithTTL(10*time.Minute))
err := locks.WithLock(ctx, "nightly-import", runImport)

// pglock
locks := pglock.NewFactory(pool)
err := locks.WithLock(ctx, "nightly-import", runImport)

// etcdlock
locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(60*time.Second))
err := locks.WithLock(ctx, "nightly-import", runImport)
```

**The check after `WithLock` is identical too**:

```go
if errors.Is(err, <backend>.ErrLocked) {
    return nil // skip
}
return err
```

If you write your service against `lock.Locker` and `lock.ErrLocked`,
**you swap backends with one line** — the wiring at startup. Business
code doesn't change.

## Decision matrix — which backend for which constraint?

Read the rows like a checklist. If a row matches your situation, the
✅ columns are good fits.

| Constraint | filelock | flock | memlock | redislock | pglock | etcdlock |
|---|---|---|---|---|---|---|
| Single host, no infra | ✅ | ✅ | — | ❌ | ❌ | ❌ |
| Multi-host | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ |
| You already run Redis | maybe | maybe | — | ✅✅ | maybe | maybe |
| You already run Postgres | maybe | maybe | — | maybe | ✅✅ | maybe |
| You already run etcd | maybe | maybe | — | maybe | maybe | ✅✅ |
| Strong consistency (CP) required | n/a | n/a | n/a | ❌ | ✅ | ✅✅ |
| Operator inspection of lock state needed | ✅✅ | ❌ | ❌ | ✅ (via redis-cli) | ✅ (via pg_locks) | ✅ (via etcdctl) |
| Need fencing tokens NOW | ✅ | ❌ planned | ✅ | ✅ | ❌ planned | ✅✅ globally monotonic |
| Need semaphore (N holders) | ✅✅ | ❌ planned | ✅ | ❌ planned | ❌ planned | ❌ planned |
| Heavy dep tree is a problem | ✅ tiny | ✅ tiny | ✅ tiny | medium | medium | ❌ ~50 deps |
| Long-running jobs (hours) | ✅ no TTL needed | ✅ no TTL needed | ✅ | ⚠️ tune TTL + Extend | ✅✅ no TTL needed | ⚠️ tune TTL |
| Cron crashes mid-run frequently | ✅ Sweep cleans up | ✅ kernel cleans up | n/a | ✅ TTL expiry | ✅ session releases | ✅ lease expires |
| Tests need to run without infra | flaky on shared CI | flaky on shared CI | ✅✅ drop-in | ❌ needs Redis | ❌ needs Postgres | ❌ needs etcd |

## Performance characteristics

Order of magnitude latencies for `Acquire`. All are single-digit ms
or less except where noted.

| Backend | Local Acquire | Cross-host Acquire |
|---|---|---|
| `memlock` | ~µs | n/a |
| `filelock` | ~ms (filesystem ops) | n/a |
| `flock` | ~ms (filesystem ops + syscall) | n/a |
| `pglock` | ~ms (one DB round-trip) | ~10ms |
| `redislock` | ~ms (one Redis round-trip) | ~5ms |
| `etcdlock` | ~ms (one Raft round-trip in best case) | ~10–20ms |

Locks are coordination, not throughput primitives. If you're
acquiring at >10K/sec the lock is probably the wrong tool — use a
queue, partition the workload, or rethink the contention point.

## Failure-mode comparison

What happens if X fails?

| Failure | filelock | flock | memlock | redislock | pglock | etcdlock |
|---|---|---|---|---|---|---|
| Holder process killed (SIGKILL) | PID probe / Sweep reclaims | Kernel releases | n/a | TTL expires | Session releases | Lease expires |
| Holder OOM | same as SIGKILL | same | n/a | same | same | same |
| Holder GC pauses past TTL | n/a (no TTL by default) | n/a | n/a | **lock lost** (mitigation: Extend + fencing) | n/a | **lock lost** if pause > TTL (mitigation: fencing) |
| Network partition (holder ↔ backend) | n/a | n/a | n/a | TTL expires; both sides may believe they hold | TCP keepalive eventually drops session | Lease expires; both may believe |
| Backend primary dies (failover) | n/a | n/a | n/a | **AP — may lose lock state** | new primary is a single-primary topology mismatch | Raft elects new leader, no loss |
| Filesystem read-only / full | acquire fails | acquire fails | n/a | n/a | n/a | n/a |
| Database connection pool exhausted | n/a | n/a | n/a | n/a | acquire fails | n/a |
| Etcd quorum lost | n/a | n/a | n/a | n/a | n/a | acquire fails |

## Summary one-liners

- **`filelock`** — best single-host backend if you want operator visibility and rich features.
- **`flock`** — best single-host backend if you want kernel-fenced crash safety with minimal API.
- **`memlock`** — required for unit tests; never use in production.
- **`redislock`** — best when you have Redis and tolerate AP semantics.
- **`pglock`** — simplest distributed lock if you have Postgres.
- **`etcdlock`** — strongest consistency, globally-monotonic fencing, but heaviest deps.

For the cross-family comparison (vs `gofrs/flock`, `bsm/redislock`,
`redsync`, etc.), see [`comparison.md`](./comparison.md).
