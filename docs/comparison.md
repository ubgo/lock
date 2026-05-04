# Comparison vs other Go locking libraries

This page benchmarks `ubgo/lock` against the most-starred Go locking
libraries on GitHub (as of 2026). The pitch isn't that any one
ubgo backend is unique — it's that **the family ships all five
mechanisms behind one consistent interface, with crash safety,
fencing, and observability across the board**.

## Field comparison

Columns are libraries; rows are features. ✅ = fully supported;
🟡 = partial / opt-in; ❌ = not supported.

| Feature | ubgo/lock family | gofrs/flock | bsm/redislock | go-redsync/redsync | nikolaydubina/go-pglock | etcd concurrency.Mutex |
|---|---|---|---|---|---|---|
| Marker-file backend | ✅ filelock | ❌ | ❌ | ❌ | ❌ | ❌ |
| Kernel-fenced flock | ✅ flock | ✅ | ❌ | ❌ | ❌ | ❌ |
| Redis backend | ✅ redislock | ❌ | ✅ | ✅ (Redlock) | ❌ | ❌ |
| Postgres backend | ✅ pglock | ❌ | ❌ | ❌ | ✅ | ❌ |
| etcd backend | ✅ etcdlock | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Shared interface across backends** | ✅ `lock.Locker` | ❌ | ❌ | ❌ | ❌ | ❌ |
| **In-memory test backend** | ✅ memlock | ❌ | ❌ | ❌ | ❌ | ❌ |
| Non-blocking acquire (`ErrLocked`) | ✅ everywhere | 🟡 (TryLock) | ✅ | ✅ | 🟡 | ✅ TryLock |
| Context support | ✅ everywhere | 🟡 | ✅ | ✅ | ✅ | ✅ |
| Crash recovery | ✅ each backend native | ✅ kernel | TTL | TTL | session | lease |
| Fencing tokens | ✅ filelock, redislock, etcdlock | ❌ | ❌ | ❌ | ❌ | ✅ mod_revision |
| Stale-marker sweep | ✅ filelock | ❌ | n/a | n/a | n/a | n/a |
| Semaphore (N holders) | ✅ filelock | ❌ | ❌ | ❌ | ❌ | ❌ |
| TraceID propagation in markers | ✅ filelock | ❌ | ❌ | ❌ | ❌ | ❌ |
| slog hooks | ✅ filelock | ❌ | ❌ | ❌ | ❌ | ❌ |
| gocron adapter | ✅ contrib/gocronlock | ❌ | ❌ | ❌ | ❌ | ❌ |
| Cross-platform (linux/macOS/win) | ✅ filelock, flock, memlock | ✅ | ✅ | ✅ | ✅ | ✅ |
| Apache-2.0 | ✅ | ✅ Apache-2.0 | MIT | Apache-2.0 | MIT | Apache-2.0 |

## When you'd pick each library

| Scenario | Pick |
|---|---|
| You only need flock(2) on a single host | `gofrs/flock` is fine; `ubgo/lock/flock` is identical scope with a Factory pattern. |
| Single Redis primary, one project | `bsm/redislock` is fine. `ubgo/lock/redislock` adds the shared interface. |
| Multi-master Redis with quorum | `go-redsync/redsync` (Redlock). `ubgo/lock` deliberately does NOT do Redlock. |
| Already running etcd cluster | `concurrency.Mutex` directly OR `ubgo/lock/etcdlock` for the Factory wrapper + family interface. |
| **Need to swap backends across environments** | `ubgo/lock` (the only family with a shared interface). |
| **Want Postgres-advisory + Redis + flock + etc. without 4 dep trees in your service** | `ubgo/lock` (each backend is its own Go module — only deps you import are pulled). |
| **Want crash-recovery + fencing + observability out of the box** | `ubgo/lock/filelock` for single-host; the rest for distributed. |
| **Want unit tests that don't touch infra** | `ubgo/lock/memlock` is the drop-in. |

## Subtle correctness — what we got right

### 1. `ErrLocked` is the same sentinel everywhere

Every backend translates its native "already held" error into the
same `lock.ErrLocked` (or its package-local `<backend>.ErrLocked`,
which `errors.Is` against `lock.ErrLocked` via the AsLocker adapter).
You write `errors.Is(err, lock.ErrLocked)` once and it works
against any backend.

### 2. Lua-guarded Redis release

`bsm/redislock` does this; `redsync/redsync` does this; some
others don't. We do — without the Lua guard, a delayed Release
from a TTL-expired holder can DEL a successor's key, silently
breaking the mutex.

### 3. Postgres session-tied means no TTL

`pg_advisory_lock` is held against the connection. We hold the
connection for the lifetime of the lock; `Holder.Release` returns
it to the pool. If the process dies, Postgres notices the session
gone and releases. **No TTL to tune** — most other Postgres lock
libraries get this right too.

### 4. etcd `mod_revision` as fencing token

etcd's `concurrency.Mutex` sets the lock key's `mod_revision` —
which is monotonic across the whole cluster, not per-name. We
expose it as `Holder.Token() uint64`. A stronger fence than
per-name INCR.

### 5. Marker file with PID + start_time

PID alone is broken under PID reuse. We store `pid_start` from
`/proc/<pid>/stat` (Linux) and compare on takeover. Other
marker-file libraries (incl. the popular `lace/filelock`) skip this
and have known stale-lock-after-PID-reuse bugs.

### 6. Operator-readable markers

`cat /var/run/myservice/job.lock` shows pid, host, acquired-at,
strategy, stale_after, slot, and (when configured) the OTel TraceID.
No other Go marker-file library exposes this much.

## What's NOT comparable

We don't claim the family is the right tool for every locking
problem. Things we deliberately don't do:

- **Reentrant locks** (except pglock's native reentrancy)
- **Wait/block APIs** (`Acquire` always returns immediately)
- **Redlock multi-master Redis**
- **Distributed deadlock detection across multiple lock names**
  (Redisson MultiLock territory)
- **Lock-server-as-a-service** (HTTP-fronted lock coordinator;
  werf/lockgate's pattern)

If you need any of the above, this isn't the library. See
[`non-goals.md`](./non-goals.md) for the reasoning.
