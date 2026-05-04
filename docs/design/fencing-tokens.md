# Design — fencing tokens

> The defense against the "process paused for GC, TTL expired,
> someone else took the lock, original process resumed and tried
> to write" scenario. Every backend in the family that supports
> them surfaces them as `Holder.Token() uint64`.

## The problem

The classic distributed-lock failure scenario, articulated by
[Martin Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html):

```
Time
  |
  |  Process A acquires lock — TTL=30s
  |
  |  Process A starts work
  |
  |  Process A is paused (GC, swap, ctrl-Z, ...)
  |
  |  ── 30s later ──
  |
  |  TTL expires; Redis deletes the key
  |
  |  Process B acquires the lock — fresh
  |
  |  Process B writes to S3 / DB / queue with state X
  |
  |  Process A wakes up
  |
  |  Process A doesn't realize TTL expired
  |
  |  Process A writes to S3 / DB / queue with state Y    ← OVERWRITES B
```

Lock libraries can't prevent this on their own. The lock state
moved from "A holds it" to "B holds it" while A was paused, but
A's CPU is unaware. When A resumes, its in-memory belief about
the lock is stale.

## The defense

A fencing token is a **monotonic identifier issued at Acquire
time**. The downstream consumer (S3 / DB / queue) records the
**highest token it has seen** and rejects writes whose token is
less than that.

```
Time
  |
  |  Process A acquires lock — token=42
  |
  |  Process A pauses
  |
  |  TTL expires
  |
  |  Process B acquires lock — token=43 (monotonic)
  |
  |  Process B writes "state X" with token=43
  |  Consumer records highest=43
  |
  |  Process A wakes up, writes "state Y" with token=42
  |  Consumer sees 42 < 43 → REJECTS A's write
```

A's write is no-op'd at the consumer. Correctness preserved.

## How each backend issues tokens

| Backend | Token source | Monotonicity scope |
|---|---|---|
| `filelock` | Per-name sidecar `<dir>/<name>.fence` (text counter, INCR-via-rename) | Per-name; strict in singleton, best-effort in semaphore |
| `redislock` | Redis `INCR <prefix>:<name>:fence` | Per-name; strict (Redis serializes INCR on a single key) |
| `etcdlock` | etcd `mod_revision` of the lock key write | **Global** — monotonic across the entire etcd cluster |
| `pglock` | Not currently exposed (planned: `txid_current()`) | n/a |
| `flock` | Not currently exposed (planned: same sidecar approach as filelock) | n/a |
| `memlock` | Per-name `atomic.Uint64` | Per-name; strict |

## Strict vs best-effort

**Strict monotonicity**: every successful Acquire returns a token
strictly greater than every previous successful Acquire's token
for the same name. No two Acquires ever return the same token.

**Best-effort monotonicity**: tokens are usually monotonic, but
under contention two near-simultaneous Acquires may see the
same prior fence value and write the same new value, returning
the same token to two holders.

`filelock` in semaphore mode (n>1) is best-effort because
multiple slots concurrently increment the per-name fence file
without atomic synchronization. Singleton mode is strict because
only one Acquire is in progress at a time per name.

If your downstream consumer uses `>=` (i.e. accepts equal
tokens), best-effort is fine — concurrent writes from two
holders both succeed, which is the semaphore semantics. If your
consumer uses `>` (strictly greater), best-effort can falsely
reject a legitimate concurrent writer.

## etcdlock's global monotonicity

etcd's `mod_revision` is **monotonic across the entire cluster**,
not per-name. This means:

```
holder1, _ := locks.Acquire(ctx, "lock-A")   // token = 4172
holder2, _ := locks.Acquire(ctx, "lock-B")   // token > 4172
holder3, _ := locks.Acquire(ctx, "lock-A")   // token > previous
```

Even though "lock-A" and "lock-B" are different names, their
fence tokens are totally ordered. Useful for downstream
consumers that need to merge writes from multiple resources
into a globally-ordered stream.

This is a stronger guarantee than per-name INCR (Redis) or
per-name sidecar (filelock).

## When fencing matters

You need fencing when:

- **Long-running jobs with tight TTLs.** GC pauses, swap, or
  network partitions can outlast TTL.
- **Critical writes.** Payment processing, financial records,
  anything where a duplicated write is a real cost.
- **Cross-region replication.** Geo-partitions are inherently
  long; lease expiry during partitions is the common case.

You probably don't need fencing when:

- Your work is **idempotent.** A duplicated cron run is wasteful
  but harmless.
- Your **TTL is generous.** If TTL >> max GC pause, fencing is
  defense for a scenario you're already very unlikely to hit.

## Implementing the consumer side

The consumer needs to:

1. Track the **highest token seen** for each protected resource.
2. Reject writes with `token < highest`.

Pseudocode for an S3 wrapper:

```go
type FencedStore struct {
    mu      sync.Mutex
    highest map[string]uint64
    s3      *s3.Client
}

func (s *FencedStore) Put(ctx context.Context, key string, data []byte, fence uint64) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    if h, ok := s.highest[key]; ok && fence < h {
        return fmt.Errorf("fenced: token %d < highest seen %d", fence, h)
    }
    s.highest[key] = fence

    return s.s3.Put(ctx, key, data)
}
```

For real systems, persist `highest` in a database (the same DB
the writes are going to is fine — bundle the fence check and the
write in one transaction). An in-memory map only protects within
one process; if your S3 wrapper has multiple replicas, the fence
state needs to be shared.

## Per-name vs global tokens

Choose your scope:

- **Per-name**: each lock name has its own fence sequence. Simple.
  Sufficient when each protected resource is locked by exactly
  one name.
- **Global**: a single counter across all locks. Useful when
  protected resources span multiple lock names.

`redislock` and `filelock` are per-name. `etcdlock` is global.

## What fencing doesn't solve

- **Idempotency.** If your work has any side effect (sending an
  email, charging a card), fencing prevents the duplicate write
  to your store but not the duplicate side effect. Idempotency
  keys at the side-effect API are the answer.
- **Application-level state.** If your application has its own
  in-memory state that two holders both modified, fencing the
  downstream write doesn't repair the in-memory divergence.

## Roadmap

- `pglock` — expose `txid_current()` as a fencing token. Globally
  monotonic across the Postgres instance.
- `flock` — sidecar counter (same approach as filelock).
- Cross-backend interface for fencing (currently only on the
  concrete `*Holder`, not on `lock.Holder`). Extension to
  `lock.Holder.Token()` would let polymorphic consumers use it
  too — at the cost of every backend having to implement.
