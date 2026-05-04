# Non-goals

Explicit list of things `ubgo/lock` deliberately does NOT do, and
why. If you need any of these, this isn't the library for you —
follow the pointers below.

## Reentrant locks

> Same holder acquires the same name twice without deadlocking.

We follow Go's `sync.Mutex` stance: locks are non-reentrant by
design. Russ Cox's framing: *"Locks should be associated with
invariants, not blocks of code."* Reentrancy hides design
problems — when you have a function-calls-function-needs-same-lock
pattern, the right fix is to refactor into private `xxxLocked()`
helpers that document "caller must hold the lock", not to paper
over it with library magic.

**Exception**: `pglock` is reentrant by default because Postgres
advisory locks are natively reentrant at the protocol level. We
don't fight Postgres; we document the asymmetry instead.

If you need reentrancy across the family, this isn't the library.

## Wait/block APIs

> `Acquire` waits until the lock is free instead of returning ErrLocked.

Every backend's `Acquire` returns `ErrLocked` immediately on
contention. There is no `WaitForLock` variant. Reasoning:

1. The dominant use case is "skip if already running" (cron
   singleton). Blocking is the wrong default.
2. If you DO want a deadline, wrap with
   `context.WithTimeout` — but a marker lock isn't the right
   tool for "serialize long work"; reach for a queue.
3. Wait-for-lock semantics turn correctness questions into
   timing questions, which is harder to reason about.

If you need blocking acquire with a wait queue, use
`go.etcd.io/etcd/client/v3/concurrency.Mutex.Lock` directly —
it's what `etcdlock` wraps with `TryLock` for the family API.

## Redlock multi-master

> Multi-master Redis quorum locking ([Redlock algorithm](https://redis.io/docs/latest/develop/use-cases/distributed-locks/)).

`redislock` targets a single Redis primary (Sentinel-friendly).
Reasons we don't ship Redlock:

1. Operationally complex — N independent Redis primaries.
2. Safety claims [contested by Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html).
   Clock-skew issues can produce two holders.
3. For workloads that genuinely need quorum-correct locking,
   `etcdlock` gives the same guarantees with simpler reasoning
   (Raft gives you total order on revisions; mod_revision is
   a globally monotonic fence).

If you need Redlock specifically, use [`go-redsync/redsync`](https://github.com/go-redsync/redsync).

## Distributed deadlock detection

> Detect cycles across multiple lock names held by different processes.

This is Redisson's `MultiLock` territory — graph-walking across
processes to detect A-waits-on-B-waits-on-A scenarios. Out of
scope for v1. The cron-singleton workloads we target use a single
lock name per work item.

If you genuinely need multi-resource distributed locking with
deadlock detection, you're in territory where you should be
asking whether a queue + idempotent processing is a better model.

## Lock-server-as-a-service

> A separate HTTP service that arbitrates locks for clients.

That's [werf/lockgate](https://github.com/werf/lockgate)'s
pattern. Operationally adds a coordinator service — if you need
one, you're better off with `etcdlock` (etcd IS your coordinator;
no extra service to run).

## Auto-renewal goroutines spawned by the library

> The library starts a goroutine that auto-extends your lease.

We don't bake goroutines into the lock library. Reasons:

1. Lifecycle questions: when does the goroutine stop? Tied to
   ctx? To the holder? To the process?
2. Backpressure: what if the renewal blocks?
3. Errors: how does the goroutine signal failure to your code?

Instead, `Holder.Extend(ctx)` is exposed and you choose when to
call it. See [`snippets.md` §5](./snippets.md#5-long-running-job-that-auto-extends-its-lease)
for the recommended pattern.

## Replication / multi-region locks

> A lock that's coordinated across geo-distributed regions.

Not a distinct feature — the right tool depends on your
existing distributed system:

- Redis with cross-region replication: `redislock` works (with
  caveats about replication lag).
- Postgres with a single primary across regions: `pglock` works.
- etcd cluster across regions: `etcdlock` is the canonical fit;
  Raft handles the consistency.

We don't add a separate "geo lock" abstraction.

## Cross-language interop

> Locks visible from Go AND Python AND Node.

Each backend's lock IS visible to other languages because the
mechanism is the underlying store, not the language wrapper:

- `redislock` keys are plain Redis keys readable from any client.
- `pglock` advisory locks are visible in `pg_locks` from any client.
- `etcdlock` keys are plain etcd keys.

But we don't ship Python/Node/Rust client libraries. If you want a
Go-acquires + Python-respects flow, use the same key conventions
in both.

## Lease-renewal-on-Acquire (sliding TTL)

> Every Acquire bumps the TTL on the existing lock if you already hold it.

Locks are non-reentrant (see top of doc), so this case doesn't
arise — if you call `Acquire` while holding the lock, you get
`ErrLocked`. To keep a long-running holder's lock fresh, use
`Holder.Extend(ctx)` instead.

## Locks with rich metadata payloads

> The marker stores arbitrary user-defined fields.

`filelock` markers have a fixed identity + debug schema; we don't
let users add arbitrary key-value pairs. Reasons:

1. Forward compat: every reader (sweep, takeover, operator) has
   to know how to handle unknown fields.
2. Most "metadata" desires are met by `WithTraceIDExtractor` (puts
   the active OTel TraceID in the marker) or by encoding meaning
   into the lock NAME.

If your name needs structure (e.g. "tenant-42:job-export"), use a
naming scheme.

## A "best" backend

> A single recommended backend for all use cases.

There isn't one. The whole point of the family is that infra
constraints decide. Single-host with kernel-fenced crash safety →
`flock`. Multi-host on Postgres → `pglock`. Strong consistency
→ `etcdlock`. The decision tree in the [root README](../README.md)
covers the choice in 30 seconds.

## Removing the `<backend>lock` package suffix

We picked `lock/redislock` (path) + `package redislock` (name)
deliberately rather than `lock/redis` + `package redis` because
the latter clashes with `github.com/redis/go-redis/v9`'s
`package redis`. The suffix preserves the family pattern AND
avoids the collision. Same reasoning for `pglock` (which doesn't
strictly need the suffix; consistency wins).
