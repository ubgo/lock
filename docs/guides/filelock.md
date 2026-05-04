# filelock — guide

> Marker-file advisory lock for cooperating processes on the same
> filesystem. Stdlib-only core. Crash-recoverable. PID-aware.
> Operator-readable. Rich features (semaphore, fencing, observability).

## When to use filelock

**Pick filelock when:**

- You're locking on a single host (or a single shared filesystem you
  control).
- You want operators to inspect a `.lock` file and see who holds it,
  when, and why (PID, host, strategy, trace ID).
- You want **rich features**: semaphore mode, fencing tokens,
  observability hooks, periodic Sweep.
- You want defaults that handle crashes correctly without needing
  external infra (Redis, Postgres, etcd).

**Don't pick filelock when:**

- Multiple hosts need coordination — use `redislock`, `pglock`, or
  `etcdlock`.
- You're on NFS / a flaky shared filesystem — use a distributed
  backend; `O_EXCL` semantics aren't trustworthy across NFS clients.
- You want kernel-level enforcement (the lock survives the process
  crash automatically without any markers) — use `flock` instead.
- You want zero filesystem footprint — use a distributed backend.

## Quickstart

```go
import (
    "context"
    "errors"
    "log"

    "github.com/ubgo/lock/filelock"
)

func nightlyImport(ctx context.Context) error {
    locks := filelock.NewFactory(filelock.WithDir("/var/run/myservice"))

    err := locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, filelock.ErrLocked) {
        log.Println("previous run still active; skipping")
        return nil
    }
    return err
}
```

## How it works

When you call `Acquire`:

1. **MkdirAll** the directory (idempotent). If your config-supplied
   dir doesn't exist, filelock creates it.
2. Try **atomic creation** of the marker: `os.OpenFile(path, O_CREATE|O_EXCL|O_WRONLY, 0644)`.
   The kernel guarantees this is atomic on local filesystems — only
   one process can win.
3. **Write the marker body** with identity (PID, pid_start, host,
   acquired) and debug fields (strategy, stale_after, max_concurrent,
   slot, trace_id).
4. If creation fails because the marker already exists:
   - **Read the existing marker** to get its identity.
   - Run the [staleness algorithm](../design/crash-recovery.md#filelock):
     PID liveness probe + optional time-window fallback.
   - If stale → **rename a fresh marker** atomically over the old one
     (POSIX rename is atomic, MoveFileEx is on Windows).
   - If alive → return `ErrLocked`.
5. **Bump the fence counter** in `<dir>/<name>.fence` for the
   `Holder.Token()` value.
6. Return a `*Holder`. Its `Release` is idempotent.

## API reference

### Construction

```go
fl := filelock.New("nightly-import",
    filelock.WithDir("/var/run/myservice"),
    filelock.WithStaleAfter(2*time.Hour),
)

locks := filelock.NewFactory(
    filelock.WithDir("/var/run/myservice"),
    filelock.WithLogger(slog.Default()),
)
```

### Acquire / Release

```go
holder, err := fl.Acquire(ctx)            // standalone Lock
holder, err := locks.Acquire(ctx, "name") // factory
defer holder.Release()                     // idempotent
```

### WithLock helpers (acquire → fn → release)

```go
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

err := filelock.WithLock(ctx, fl, func(ctx context.Context) error {
    return doWork(ctx)
})
```

### Holder methods

| Method | Purpose |
|---|---|
| `Release() error` | Removes marker; idempotent. |
| `Path() string` | Marker file path. Useful for logging. |
| `Token() uint64` | Monotonic fencing token (per-name sidecar counter). |

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithDir(dir string)` | `os.TempDir()` | Where markers live. |
| `WithStaleAfter(d time.Duration)` | unset | Reclaim markers older than `d` (when probe is inconclusive). |
| `WithStaleStrategy(s StaleStrategy)` | `PIDFirst` | How to evaluate "is the holder alive": `PIDFirst` / `PIDOnly` / `TimeOnly`. |
| `WithMaxConcurrent(n int)` | 1 | Up to N parallel holders for the same name. |
| `WithLogger(l *slog.Logger)` | nil | slog hook for Acquire/Release events. |
| `WithMetrics(m MetricsRecorder)` | nil | Prometheus / OTel meter integration. |
| `WithSpanStarter(s SpanStarter)` | nil | OTel tracing. |
| `WithTraceIDExtractor(f func(ctx) string)` | nil | Auto-record TraceID in marker debug fields. |

### Factory.Sweep

```go
n, err := locks.Sweep(ctx)
// reclaimed N stale markers under the factory's dir.
```

Sweep walks `<dir>/*.lock`, runs the same staleness check Acquire
uses, and removes markers whose holders are dead. Wrap it in its
own filelock so two sweepers don't collide:

```go
locks.WithLock(ctx, "filelock-sweep", func(ctx context.Context) error {
    n, _ := locks.Sweep(ctx)
    slog.Info("filelock sweep", "reclaimed", n)
    return nil
})
```

## Stale strategies

The PID-vs-time interaction has three named modes:

```go
type StaleStrategy int
const (
    StaleStrategyPIDFirst StaleStrategy = iota // default
    StaleStrategyPIDOnly
    StaleStrategyTimeOnly
)
```

| Strategy | Behaviour |
|---|---|
| `PIDFirst` (default) | Probe PID. Conclusive → use that answer. Inconclusive → fall back to `WithStaleAfter` if set, else ErrLocked. |
| `PIDOnly` | Probe PID. Conclusive → use that answer. Inconclusive → ErrLocked. **Time is never consulted.** |
| `TimeOnly` | Skip PID. Use `WithStaleAfter` if set, else ErrLocked. |

When to use which:

- **Single host, no NFS** → `PIDFirst` with no `WithStaleAfter` is
  fine. PID probe always tells the truth on the local host.
- **Single host, want safety net** → `PIDFirst` + generous
  `WithStaleAfter` (1h, 24h). The time fallback handles weird
  cases (different UID, /proc unavailable).
- **Cross-host or NFS** → `PIDOnly` or `TimeOnly`. PIDs aren't
  meaningful across hosts; we explicitly mark them as
  inconclusive when the marker's host doesn't match.
- **Don't trust your filesystem** → switch to a distributed
  backend.

## Marker file format

The marker contains identity fields (consulted by Acquire) and debug
fields (informational only):

```
# Identity — read by Acquire
pid=12345
pid_start=2026-05-01T18:42:09Z
host=worker-3.local
acquired=2026-05-01T18:42:11Z

# Debug — informational only
strategy=pid-first
stale_after=2h
max_concurrent=3
slot=1
trace_id=4bf92f3577b34da6a3ce929d0e0e4736
```

**Critical**: debug fields are NEVER read by Acquire. They exist so
operators inspecting a marker get useful debugging info. Strategy /
stale_after / max_concurrent are decided by the **reader** from its
own options. Reading them from the marker would create silent
config-drift bugs.

## Use cases

### 1. Cron-singleton (the headline use case)

```go
locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))

err := locks.WithLock(ctx, "syncgtmandga4", syncJob,
    filelock.WithStaleAfter(2*time.Hour),
)
if errors.Is(err, filelock.ErrLocked) { return nil }
return err
```

The factory holds the shared `dir`; per-call `WithStaleAfter`
configures per-cron tolerance.

### 2. Semaphore — N parallel workers

```go
err := locks.WithLock(ctx, "indexer", indexBatch,
    filelock.WithMaxConcurrent(3),
)
```

Up to 3 indexers run in parallel; the 4th caller gets `ErrLocked`.

### 3. Periodic Sweep on a schedule

See [`docs/snippets.md` §9](../snippets.md#9-periodic-stale-marker-sweep-filelock).

### 4. Observability — TraceID in the marker

```go
locks := filelock.NewFactory(
    filelock.WithDir("/var/run/myservice"),
    filelock.WithLogger(slog.Default()),
    filelock.WithTraceIDExtractor(func(ctx context.Context) string {
        if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
            return span.SpanContext().TraceID().String()
        }
        return ""
    }),
)
```

Operators reading `cat /var/run/myservice/job.lock` see the active
TraceID and can jump straight to Tempo / Jaeger / Honeycomb.

### 5. Different stale windows per cron

```go
locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))

_ = locks.WithLock(ctx, "mailerlite-sync", mailerliteSync,
    filelock.WithStaleAfter(10*time.Minute))

_ = locks.WithLock(ctx, "shopify-orders", shopifyOrders,
    filelock.WithStaleAfter(12*time.Hour))
```

## Operational notes

### Inspecting a held lock

```sh
$ cat /var/run/myservice/import-orders.lock
# Identity — read by Acquire
pid=12345
pid_start=2026-05-01T18:42:09Z
host=worker-3.local
acquired=2026-05-01T18:42:11Z

# Debug — informational only
strategy=pid-first
stale_after=2h
trace_id=4bf92f3577b34da6a3ce929d0e0e4736
```

### Force-releasing a lock manually

```sh
$ rm /var/run/myservice/import-orders.lock
```

The next `Acquire` succeeds. There's no special unlock command —
the marker file IS the lock state.

### Monitoring stale-marker accumulation

If your service has many semaphore slots and frequent crashes,
markers can pile up. Run `Sweep` on a schedule. Monitor
`reclaimed` count in your logs / metrics.

### Permission issues

`<dir>/<name>.lock` is created with mode `0644`. The directory is
created with `0755`. If your operator wants to chown the dir, do
it once at deploy time — filelock won't fix permissions later.

## Flaws

See [`docs/flaws.md` §filelock](../flaws.md#filelock) for the full
list. Highlights:

- **Stale markers are visible** until reclaimed (mitigation: Sweep).
- **NFS races** — `O_EXCL` not always atomic across NFS clients.
- **PID-reuse detection only on Linux** (other platforms trust the
  alive answer; mitigation: `WithStaleAfter`).
- **Semaphore mode requires `n` to agree** across all callers.

## Migration

From `lace/filelock` or any other `os.Create`-based marker library —
see [`docs/migration.md`](../migration.md).
