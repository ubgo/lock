# Documentation index

Docs for `github.com/ubgo/lock`. Start with the
[main README](../README.md) for the comparison matrix and decision
tree; come here for the deep dives.

## Per-backend guides

In-depth: how each backend works, when to use it, when not to,
full API reference, worked examples, operational notes, and
flaws.

- [`guides/filelock.md`](./guides/filelock.md) — marker file (single host)
- [`guides/flock.md`](./guides/flock.md) — flock(2) / LockFileEx (single host)
- [`guides/redislock.md`](./guides/redislock.md) — Redis (multi-host, AP)
- [`guides/pglock.md`](./guides/pglock.md) — Postgres advisory (multi-host)
- [`guides/etcdlock.md`](./guides/etcdlock.md) — etcd (multi-host, CP)
- [`guides/memlock.md`](./guides/memlock.md) — in-memory (tests)
- [`guides/gocronlock.md`](./guides/gocronlock.md) — gocron v2 adapter

## Cross-cutting docs

- [`use-cases.md`](./use-cases.md) — **12 real-world scenarios**
  with concrete code: cron singleton, leader election,
  GC-pause defense, migration runner, per-tenant serialization,
  worker pool, gocron integration, …
- [`family-comparison.md`](./family-comparison.md) — full
  side-by-side capability matrix across all backends in the
  family. Decision matrix mapping constraints to backends.
- [`comparison.md`](./comparison.md) — feature matrix vs **other**
  Go locking libraries (`gofrs/flock`, `bsm/redislock`,
  `redsync`, `nikolaydubina/go-pglock`, etcd's native
  `concurrency.Mutex`).
- [`snippets.md`](./snippets.md) — 15 copy-paste recipes for
  common patterns (cron singleton, fencing, semaphore, sweep,
  observability hooks, etc.).
- [`migration.md`](./migration.md) — line-by-line diffs for
  moving from each major Go lock library.
- [`non-goals.md`](./non-goals.md) — what we deliberately don't
  ship and why (Redlock, reentrancy, blocking acquire, …).
- [`flaws.md`](./flaws.md) — honest limitations of each backend
  + cross-cutting issues. Read this before adopting in
  production.

## Design docs

Deeper into how the family is built and why decisions were made.

- [`design/crash-recovery.md`](./design/crash-recovery.md) —
  how each backend handles process crashes / partitions / TTL
  expiry.
- [`design/fencing-tokens.md`](./design/fencing-tokens.md) —
  the Kleppmann scenario, the per-backend token sources,
  per-name vs global monotonicity, and consumer-side
  implementation.
- [`design/observability.md`](./design/observability.md) —
  slog / Prometheus / OTel hooks; TraceID propagation into the
  marker file (filelock-only currently).
- [`design/races.md`](./design/races.md) — TOCTOU, NFS, PID
  reuse, sweep races, Redis failover, etcd lease expiry,
  Postgres session drops, flock per-fd vs per-process.

## Reading order recommendations

**"I just want to use it"** → main README → pick a backend →
that backend's guide.

**"I'm evaluating for production"** → main README →
[`comparison.md`](./comparison.md) → [`flaws.md`](./flaws.md) →
backend guide for your shortlist.

**"I'm building a library on top"** → main README → backend
guide for `memlock` (for tests) → [`design/fencing-tokens.md`](./design/fencing-tokens.md).

**"I'm migrating from X"** → [`migration.md`](./migration.md) →
target backend's guide.

**"I want to understand the trade-offs deeply"** → all of
[`design/`](./design/) → [`flaws.md`](./flaws.md).

## Conventions

- Code blocks are runnable Go. Imports are explicit.
- Examples assume `ctx context.Context` is in scope.
- Where a backend has a feature flag (`WithStaleAfter`,
  `WithMaxConcurrent`, `WithTTL`), we name it on first use and
  link to the option's docs.
- Honest limitations are in [`flaws.md`](./flaws.md). If you
  find one we haven't documented, file an issue.
