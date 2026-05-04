# Design — race conditions across the family

> What can go wrong in each backend, in detail. Read this if you're
> reviewing for correctness, debugging a weird bug, or trying to
> decide whether the family is right for your workload.

## TOCTOU (time-of-check-to-time-of-use)

The classic two-step lock pattern is:

```go
if !lock.IsLocked() {     // CHECK
    lock.Take()           // USE
}
```

Two processes hit `IsLocked()` in the same nanosecond, both see
"not locked," both call `Take()`, both succeed. Both think they're
the singleton.

**The family deliberately doesn't expose `IsLocked()`** — it
collapses to a single atomic call:

- `filelock` / `flock`: atomic `O_CREATE|O_EXCL` → either you
  win or you see ErrLocked. No check-then-take.
- `redislock`: atomic `SET NX EX` in one round-trip.
- `pglock`: `pg_try_advisory_lock()` is atomic.
- `etcdlock`: `concurrency.Mutex.TryLock` is atomic.

If you've migrated from a library that has `IsLocked()`, the
porting guide is in [`docs/migration.md`](../migration.md).

## NFS / shared filesystem races

`O_CREATE|O_EXCL` is atomic on **local** filesystems. On NFSv2/v3
with certain server configurations, it's not — two clients can
both see "file does not exist" and both create it.

NFSv4 fixed this for most configurations, but corner cases remain:

- Some Linux distros disable NFSv4 by default.
- macOS NFS support is partial.
- Windows network shares (SMB) do their own thing.

**Recommendation**: don't use `filelock` or `flock` on NFS / SMB.
Use a distributed backend (`redislock`, `pglock`, `etcdlock`).
Filelock's `StaleStrategyTimeOnly` mode helps if you must use NFS,
because it skips the PID probe (which is host-local and meaningless
on shared filesystems anyway).

## PID reuse

The kernel recycles PID numbers. After process A (PID 12345)
crashes:

```
T+0:    PID 12345 = process A (the lock holder)
T+5s:   PID 12345 process A crashes
T+30s:  PID 12345 = process B (unrelated, just spawned)
T+31s:  Acquire reads marker; pid=12345; probes "alive" via kill(0)
        → false conclusion: lock is held by the holder
        → ErrLocked, even though the original holder is gone
```

**Mitigation**: `filelock` stores `pid_start` (the kernel's
recorded process-start-time) in the marker. On takeover we
re-read PID 12345's current start time and compare. Mismatch →
PID was reused → take over.

Currently implemented for **Linux only** via `/proc/<pid>/stat`
field 22. macOS / Windows return zero start time, which the
probe interprets as "can't check; trust the alive answer." On
those platforms, PID reuse can cause spurious ErrLocked. The
`WithStaleAfter` time fallback eventually clears it.

## Sweep races

`Factory.Sweep(ctx)` walks `<dir>/*.lock` and removes stale
markers. Race: between Sweep's "is this stale?" check and the
`os.Remove`, another process could rename a fresh marker into
place. Sweep would delete it.

**Mitigation**: Sweep does a **double read** — read once, run
the staleness check, read again, only `os.Remove` if the
second read shows the same PID and acquired time. This catches
the obvious race.

It's not perfectly race-free without `flock(2)` advisory locking
on the sweep operation itself (which we'd add via filelock's
own `flock` integration in a future version). The bounded
mistakes Sweep can make are:

- Remove a fresh marker → next Acquire creates a new one. No
  correctness loss.
- Skip a stale marker → next Sweep gets it. Eventually consistent.

Both are acceptable; we explicitly accept the tiny window.

## Redis SET-NX failover races

If the Redis primary dies between `SET NX EX` returning success
and the replication of that write to the replica:

```
T+0:    Process A: SET key value NX EX 30 → OK on primary
T+0.001: Primary dies before replicating to replica
T+0.005: Replica promoted to new primary; doesn't have key
T+0.010: Process B: SET key value NX EX 30 → OK on new primary
        → A AND B both think they hold the lock
```

This is fundamental to async-replicated Redis (AP).
`go-redsync/redsync`'s Redlock algorithm tries to mitigate by
requiring a quorum across N independent primaries; we
deliberately don't (see [`non-goals.md`](../non-goals.md)).

**Mitigation**: fencing tokens. Even if A and B both believe
they hold the lock, only one of their writes succeeds at the
fencing-aware downstream consumer.

## Etcd lease races

If your process is partitioned from etcd long enough for the
lease to expire:

```
T+0:     Acquire — lease 0xABCD with TTL=30s
T+5:     Network partition cuts client off from etcd
T+35:    Lease expires; etcd deletes the lock key
T+40:    Process B acquires — fresh lease
T+60:    Network heals; client A reconnects
         → Client A still believes it holds the lock
         → Any write A performs is unprotected
```

Stronger than the Redis case (Raft prevents flapping during the
partition), but not eliminated. **Mitigation**: fencing tokens
via `mod_revision`. A's writes use the OLD mod_revision, B's
use a NEW one; downstream rejects A.

## Postgres connection-drop races

Similar to etcd's lease-expiry race, but Postgres-flavoured:

```
T+0:     Acquire — pg_try_advisory_lock(K) → true
T+5:     TCP connection silently dies (carrier dropped)
T+35:    TCP keepalive detects death; Postgres releases the
         advisory lock (session ended)
T+40:    Process B acquires K
T+60:    Process A's code keeps running, thinks it holds the lock
```

Postgres releases the lock when the SESSION ends. A's local
view is stale until A's code happens to run a query that
returns an error.

**Mitigation**: shorter `tcp_keepalives_idle` so failed
connections are detected fast. Plus fencing tokens (planned;
currently not exposed by pglock).

## flock per-fd vs per-process semantics

- **Linux** — flock is per-fd. Two open file descriptors in the
  same process can independently hold the lock.
- **BSD / macOS** — flock is per-process. Two fds in the same
  process see each other.
- **Windows LockFileEx EXCLUSIVE** — per-handle (like Linux).

If you're locking from multiple goroutines in the same process:

```go
// On Linux:
fd1 := openFD()
fd2 := openFD()  // same file
flock(fd1, LOCK_EX|LOCK_NB)  // succeeds
flock(fd2, LOCK_EX|LOCK_NB)  // FAILS (per-fd) — even same process

// On macOS/BSD:
flock(fd2, LOCK_EX|LOCK_NB)  // SUCCEEDS (per-process)
```

We don't paper over this. Use `sync.Mutex` for goroutine-level
coordination; use `flock` strictly for cross-process.

## sync.Mutex vs lock.Locker

`sync.Mutex` and `sync/atomic` work within a single Go process.
`lock.Locker` is for cross-process coordination. They're not
substitutes:

- Goroutines in the same process → `sync.Mutex`.
- Multiple processes on the same host → `flock` or `filelock`.
- Multiple hosts → `redislock`, `pglock`, or `etcdlock`.

A common mistake: using `lock.Locker` to coordinate goroutines
within one process. The library will work, but it's ~1000× slower
than `sync.Mutex` and adds infra dependencies for no benefit.

## "Lost lock" races (general distributed pattern)

Across all distributed backends — `redislock`, `etcdlock`,
`pglock` — there's a window between "lock state on the server
changes" and "client realizes it." During that window, the
client thinks it holds the lock when it doesn't.

The library can't close this window — only the consumer of the
write can. **Use fencing tokens** if your work has any
correctness sensitivity. Without downstream fencing, no
distributed lock library can give you transactional
all-or-nothing semantics.

This is the central insight of Kleppmann's
"How to do distributed locking" critique. It's not a flaw of
this family or any specific implementation — it's a property
of distributed systems.

## Closing thought

If you're paranoid about races, the conservative stack is:

1. `etcdlock` for the lock (Raft is the strongest CP we can give
   you).
2. Fencing tokens via `Holder.Token()` (mod_revision).
3. Downstream consumer that rejects writes with stale tokens.

This combination makes the lock library's failure modes
(partitions, lease expiry, GC pauses) into consumer-side no-ops
rather than corruption. Anything less than this stack inherits
the trade-offs of the layer you skipped.

For 95% of cron-singleton / queue-dedup workloads, the
trade-offs are fine and you don't need the conservative stack.
We document them so you can make an informed choice.
