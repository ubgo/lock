# Known flaws & limitations

The honest list of things `ubgo/lock` doesn't do well, can't do at
all, or does with caveats. Read this before adopting — it's better
to discover a hard limit before integrating than after.

## Cross-cutting limitations

### 1. No backend has unconditional safety guarantees

Locks are coordination primitives — they only work when **all
participants follow the protocol**. None of the backends prevent a
process that doesn't call `Acquire` from clobbering the protected
resource. This is true for every advisory lock library in any
language; we mention it because operators often expect "kernel
flock means kernel-protected access," which is wrong.

If you need mandatory access control, you need filesystem
permissions, OS capabilities, or a higher-level abstraction
(transactional database, object-versioning storage). A lock
library is not the right tool.

### 2. `Acquire` is non-blocking — every backend, no exceptions

Wait-for-lock semantics are deliberately not implemented. If you
need a deadline, wrap with `context.WithTimeout` and retry yourself.
The library will never queue you. See
[`non-goals.md`](./non-goals.md) for the reasoning.

### 3. The `lock.Locker` interface is a lowest-common-denominator

Backend-specific features (fencing tokens, semaphore mode, sweep,
TraceID propagation) are NOT on the interface. Code written
against `lock.Locker` can `Acquire`/`Release` only. To use
fencing or semaphore, you need the concrete `*<backend>.Factory`.
This is intentional — see the design rationale in
[`comparison.md`](./comparison.md) — but it means polymorphic
consumers must give up the rich features.

### 4. The interface module's tag scheme is unusual

Each subpath has its own tag (`filelock/v0.1.0`, `pglock/v0.1.0`,
…) and the root has plain `v0.1.0`. The Go module proxy can lag
when indexing path-prefixed tags — fresh tags may take a few
minutes before `go get` resolves them via the default proxy.
Falling back to `GOPROXY=direct` always works.

## filelock

### Stale markers are visible until reclaimed

If a process crashes, its marker file stays on disk until either
(a) the next `Acquire` for the same name runs through the
PID-probe-and-takeover path, or (b) `Sweep` runs. Operators may
see stale `.lock` files and think something's wrong. They aren't,
but the noise is real. Mitigation: schedule `Sweep` periodically
(see [`snippets.md` §9](./snippets.md#9-periodic-stale-marker-sweep-filelock)).

### NFS / shared filesystems have known races

`O_CREATE|O_EXCL` is atomic on local filesystems but historically
non-atomic on NFSv2/v3 with certain server configurations. NFSv4
fixed this for most cases, but corner cases remain. We don't
recommend filelock on shared filesystems — use a distributed
backend instead. See [`design/races.md`](./design/races.md).

### PID-reuse detection only works on Linux (currently)

`processStartTime()` is implemented on Linux via `/proc/<pid>/stat`.
On macOS and Windows, it returns zero, which the probe layer
interprets as "PID-reuse check unavailable, trust the alive
answer." The result: on macOS/Windows, an unrelated process
that happens to get the recycled PID of the original holder
will be misidentified as alive, and the lock will appear held.
This is rare but possible. Mitigation: set
`WithStaleAfter(d)` so the time fallback eventually reclaims.

### `WithStaleAfter` is easy to misuse

Beginners read it as "max runtime for this job." It's not — it's
"how long after the holder appears dead can the next acquire
take over." If you set it shorter than your job's healthy
runtime under `StaleStrategyTimeOnly`, you get parallel
execution. See the foot-gun walkthrough in MISSION §4.9.

### Semaphore mode requires the `n` to agree across callers

If process A acquires with `WithMaxConcurrent(3)` and process B
runs the same name with `WithMaxConcurrent(2)`, B only probes
slots 0–1 and may miss A's slot 2 — both think there's a free
slot when really 4 are running. We don't persist `n` in
metadata; deploy callers together with matching `WithMaxConcurrent`.

### Fencing tokens are best-effort in semaphore mode

Multiple slots concurrently incrementing the per-name fence file
can race; two holders may get the same token. Singleton mode
(`n=1`) is strictly monotonic. Document this caveat for any
downstream consumer that depends on strict monotonicity.

## flock

### NFS support is platform-dependent

`flock(2)` on Linux NFS is implemented in newer kernels (4.0+)
but may silently fall back to local-only on older kernels or
non-Linux NFS clients. Don't rely on `flock` for cross-host
coordination. Use a distributed backend.

### Linux flock is per-fd; BSD/macOS is per-process

This is a kernel-level difference, not a `ubgo/flock` issue, but
it affects you: on Linux, two open file descriptors in the same
process can independently lock the same file. On BSD/macOS, it's
per-process. We don't paper over the difference — if you're
locking from multiple goroutines in the same process, use
`sync.Mutex` for that, then `flock` for cross-process.

### Holder.Release races the kernel cleanup

If your process exits abruptly between `Release()`'s `os.Remove`
and `Close()`, the marker file may remain on disk briefly. The
kernel still releases the lock; the file is cosmetic. `Sweep`
isn't a feature of `flock` because there's no real benefit
(the lock isn't held; just the empty file lingers).

### Windows LockFileEx semantics differ subtly

LockFileEx with EXCLUSIVE_LOCK is per-handle, not per-process.
Two handles in the same Windows process to the same file CAN
conflict (matches Linux flock behaviour, not BSD). Documented;
not a bug.

## redislock

### TTL trade-off is unavoidable

Set TTL too short → healthy long-running jobs lose their lock to
timeout, two holders run in parallel. Set TTL too long → crash
recovery takes that long. There is no perfect setting. Mitigation
patterns: use `Holder.Extend(ctx)` from the holder, OR size TTL
based on p99 healthy runtime + safety margin.

### Redis cluster failover can produce two holders

If the primary dies before replicating the SET-NX to the replica,
and the replica is promoted, the new primary doesn't have your
key — another caller can SET-NX successfully. Two holders. This
is fundamental to AP-Redis and one reason the Redlock algorithm
exists (and why it's controversial).

For workloads where this matters, use `etcdlock` instead. For
cron-singleton and queue dedup workloads (where occasional double
runs are acceptable, idempotent, or rare), `redislock` is fine.

### Lua-guarded Release still has a tiny window

The Lua script runs `GET key; if value matches, DEL key`.
Between Acquire and Release, the TTL could expire and a new
holder could acquire a fresh lock with a NEW value. Our Lua
guard prevents Release from deleting the new lock — it returns
`ErrLockLost` instead. But the original holder believed it was
holding a valid lock until Release; any work it did after TTL
expiry was unprotected. Use fencing tokens to close this gap.

### No Redlock multi-master support

By design — see [`non-goals.md`](./non-goals.md). If you
genuinely need quorum-correct distributed locking, use
`etcdlock`.

## pglock

### Reentrancy is the family-wide exception

Postgres advisory locks are reentrant; same session can `Acquire`
the same key twice and the lock counter increments. Other
backends in the family are NOT reentrant. Code that depends on
reentrancy will silently break when ported to flock/redislock/etcdlock.

If you write code against `lock.Locker`, you're in
non-reentrant land — `Acquire` while holding returns `ErrLocked`
on every backend EXCEPT pglock. Don't rely on reentrancy if
you might swap backends.

### One connection per held lock

Each `Holder` keeps a connection out of the pool for the
lifetime of the lock. If you hold many concurrent locks, you
need a correspondingly large pool. The math: `MaxConns ≥
max simultaneous Holders + headroom for normal queries`.
Tune `pgxpool.Config.MaxConns` accordingly.

### Postgres connection drops release the lock

This is a feature (crash safety), but it's also a footgun: a
network hiccup that triggers a pool reconnect quietly drops the
lock. Your code keeps running, thinking it holds the lock,
until the next `Holder.Release` runs and (correctly) reports
no-op. If you depend on the lock for correctness during the
work, use fencing tokens (currently planned for pglock —
`txid_current()` would work) or mod_revision-style protection
from the consumer end.

### String-name → int64 collision possible (but unlikely)

We hash names via FNV-1a into a 64-bit space. Two different
names hashing to the same int64 would collide as the same
Postgres advisory lock. For 1M lock names, the birthday
probability is ~3e-8 — effectively zero, but technically
possible.

### No fencing token

`pglock` doesn't currently expose a fencing token. The natural
fit is `txid_current()` on the holder's session, but it's not
yet wired up. Use `etcdlock` if you need globally-monotonic
fences.

### No semaphore mode

`pg_advisory_lock` is binary — held or not. To get N-holders,
you'd need a custom table with row-level locks. Not currently
shipped.

## etcdlock

### Heavy dependency footprint

The etcd v3 client pulls grpc, protobuf, golang.org/x/* — about
50 transitive deps. By far the largest in the family. If your
service doesn't already use etcd, this is a substantial addition.

### TTL trade-off still applies

Same as Redis — TTL too short means healthy holders lose locks;
too long means slow crash recovery. The auto-keepalive
goroutine in `concurrency.NewSession` mitigates this for
healthy holders, but if your holder is partitioned from etcd
for longer than the TTL, the lock is reclaimed. Code defensively.

### Network partitions can cause `ErrLockLost`-like situations

If your process is partitioned from the etcd cluster long
enough for the lease to expire, etcd treats you as gone and
deletes your key. The next Acquire elsewhere wins. When your
network heals, your code still thinks it holds the lock —
identical to the redislock case but with stronger guarantees
during the partition (Raft prevents a flapping primary from
losing your lock as long as you're connected).

`Holder.Token()` (mod_revision) is the defense. The downstream
consumer rejects writes with `token < highest seen` —
including writes from a partitioned-and-recovered holder.

### FIFO fairness can cause head-of-line blocking

`concurrency.Mutex` queues waiters in FIFO order under
`Lock(ctx)`. We use `TryLock` to preserve non-blocking
semantics, but if a downstream consumer of `*etcdlock.Holder`
ever calls `mutex.Lock(ctx)` directly (we don't expose
`mutex` publicly, so this isn't currently a hazard, but
future API additions might), beware.

### Lock keys persist on TTL-style cleanup

When a lease expires, etcd removes the lock key but the
**fence counter (mod_revision)** is global to the cluster.
Even after our key is deleted, the next acquire's
mod_revision is strictly larger than ours. So fencing
remains correct across crashes.

### Etcd cluster operations matter

You're depending on etcd's availability for every Acquire.
If the cluster is partitioned (no quorum), every Acquire
fails. Operationally, etcd needs ≥3 nodes for safety; ≥5 for
high availability. Adding etcd to your stack just for locking
is rarely justified — use this when you're already running
etcd for other reasons (Kubernetes, service discovery).

## memlock

### Per-process scope

By design — it's an in-memory map. Use only for unit tests.
**Do not deploy memlock in production**, even for "just one
process" cases — use `flock` or `filelock` instead, both of
which guarantee mutual exclusion across processes (the actual
threat model in production).

### No persistence

If your test creates a Holder and the test binary crashes
before Release, the in-memory state is gone — next test run
sees a clean slate. That's usually what you want for tests,
but it means memlock is genuinely useless if you wanted
"survives process restart" semantics.

### Token sequence resets per Factory

A new `memlock.NewFactory()` starts the fence counter at zero.
Cross-test isolation is built in (each test gets a fresh
factory), but it means memlock can't validate downstream
consumers that rely on monotonicity across long sessions.

## contrib/gocronlock

### gocron's Locker interface is leaky

gocron's `Locker.Lock` / `Lock.Unlock` shape doesn't match
`lock.Locker` exactly — gocron threads ctx through Unlock too.
We adapt by ignoring Unlock's ctx (Release in our backends
isn't ctx-aware in the locker.Holder interface). For local
backends (filelock, flock, memlock) this is fine. For
distributed backends (redislock, pglock, etcdlock) the
release call IS doing network I/O, so a context with deadline
would be useful. Currently the adapter uses `context.Background()`
for cleanup. If you need cleanup-with-deadline, hold the concrete
`*<backend>.Factory` and call `WithLock` directly.

### gocron's "lock per job by name" pattern locks named jobs only

gocron uses the job's name (or function name if unnamed) as
the lock key. Anonymous functions get autogenerated names that
are NOT stable across builds. If your job naming has any
non-determinism, two replicas might use different lock names
and both run the job. Always pass `gocron.WithName("...")`
when using gocronlock.

### Subpackage adds gocron's heavy dep tree

Importing `lock/contrib/gocronlock` pulls gocron + cron parser
+ clockwork + uuid (4 transitive deps). That's why the adapter
is an opt-in subpath, not in any core backend's go.mod.

## Fundamental issue across distributed backends: clock skew

`redislock` and `etcdlock` rely on TTL for crash recovery.
TTL is expressed in seconds the server measures. If the
server's clock skews backward, locks expire later than expected;
forward, earlier. Etcd uses Raft-leader-time, which is more
robust than Redis's per-node time, but neither is bulletproof
under pathological clock skew.

We don't ship clock-skew mitigation — that's a deployment
concern. If your servers' clocks drift more than seconds,
**your problems are bigger than locking**. Use NTP.

## Fundamental issue across all backends: split brain

A network partition that splits a Raft cluster (etcd) or a
Sentinel-managed Redis primary can produce scenarios where two
processes both believe they hold a lock. Etcd is the most
robust here because Raft requires quorum to grant a lease;
Redis is most permissive (single-primary, replica eventually
serves). Filelock + flock are immune since they're single-host.

If two-holders-during-partition is a correctness risk for your
work, use fencing tokens AND consume them downstream. Without
downstream cooperation, no lock library can prevent split-brain
writes.

## Documentation honesty

If you find a flaw in the implementation we haven't documented
here, please file an issue. We'd rather expand this list than
hide problems. Links: GitHub issues at
[`github.com/ubgo/lock/issues`](https://github.com/ubgo/lock/issues).
