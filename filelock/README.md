# filelock

> Marker-file advisory lock for cooperating processes on the same
> filesystem. Stdlib-only core. Crash-recoverable. PID-aware.

```bash
go get github.com/ubgo/lock/filelock
```

## Quick start

For one-off locks (CLI tools, tests):

```go
import (
    "context"
    "errors"
    "log"

    "github.com/ubgo/lock/filelock"
)

func main() {
    ctx := context.Background()
    fl := filelock.New("nightly-import", filelock.WithDir("/var/run"))

    holder, err := fl.Acquire(ctx)
    if errors.Is(err, filelock.ErrLocked) {
        log.Println("another run is in progress; skipping")
        return
    }
    if err != nil {
        log.Fatal(err)
    }
    defer holder.Release()

    // ... protected work ...
}
```

For services that lock many names with shared config (the
"28 cron singletons share one TempDir" case), use a `Factory`:

```go
locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))

err := locks.WithLock(ctx, "syncgtmandga4", syncJob,
    filelock.WithStaleAfter(2*time.Hour),
)
if errors.Is(err, filelock.ErrLocked) {
    log.Println("syncgtmandga4 already running")
    return nil
}
return err
```

## What this is (and isn't)

| | This package | `flock(2)` / `gofrs/flock` | Redis / etcd |
|---|---|---|---|
| Mechanism | Sentinel file with `O_CREATE\|O_EXCL` + PID probe | Kernel-enforced advisory locks | Network round-trip |
| Crash recovery | ✅ PID liveness probe + stale window + Sweep | ✅ kernel releases on exit | ✅ TTL |
| Cross-machine | ❌ | ❌ | ✅ |
| Wait / block | ❌ — `Acquire` returns `ErrLocked` immediately | ✅ | ✅ |
| Core dependencies | stdlib only | platform syscalls | network client |

Use this for **"is anything running this job right now?"** semantics:
cron deduplication, idempotent imports, single-runner queue
processors. Reach for a sister `ubgo/lock-*` module when the lock
needs to span machines.

## Features

- **Factory pattern** — `NewFactory(opts...)` carries shared config
  so call sites need only the lock name and any per-call overrides.
- **Crash recovery** — PID liveness probe (Linux/macOS/Windows) plus
  optional `WithStaleAfter` time fallback. Crashed holders get
  reclaimed automatically; v0.1 left them forever.
- **Three stale strategies** — `PIDFirst` (default), `PIDOnly`,
  `TimeOnly`. Pick based on whether you trust PIDs (single-host
  yes, NFS no) and whether you want a time fallback.
- **Semaphore mode** — `WithMaxConcurrent(n)` for "up to N parallel
  holders".
- **Fencing tokens** — `Holder.Token()` returns a monotonic counter
  for downstream "reject stale writer" defenses.
- **Sweep** — `Factory.Sweep(ctx)` periodically cleans up markers
  whose holders are gone.
- **Observability** — Prometheus / OpenTelemetry / slog hooks via
  interface-typed options. TraceID auto-recorded in markers.
- **In-memory test backend** (`memlock` subpackage) — drop-in factory
  for unit tests; zero filesystem, millisecond-fast.
- **gocron adapter** (`contrib/gocronlock` separate module) — wire a
  Factory into `gocron.WithDistributedLocker(...)` for instant
  cron-singleton mode. Lives outside the main module so the gocron
  dep tree stays out of the lean core.
- **Backend-agnostic** — `Factory.AsLocker()` returns the shared
  `github.com/ubgo/lock.Locker` interface, so swap backends without
  changing caller code.

## API at a glance

| Symbol | Purpose |
|--------|---------|
| `New(name, opts...) *Lock` | Construct a single-name lock. |
| `NewFactory(opts...) *Factory` | Construct a multi-name factory. |
| `Lock.Acquire(ctx, opts...) (*Holder, error)` | Take the lock. |
| `Factory.Acquire(ctx, name, opts...) (*Holder, error)` | Same, factory-scoped. |
| `Factory.WithLock(ctx, name, fn, opts...) error` | Acquire → fn → Release. |
| `WithLock(ctx, fl, fn, opts...) error` | Standalone form for `*Lock`. |
| `Factory.Sweep(ctx) (int, error)` | Reclaim stale markers. |
| `Factory.AsLocker() lock.Locker` | Backend-agnostic interface. |
| `Holder.Release() error` | Idempotent. |
| `Holder.Token() uint64` | Fencing token. |
| `Holder.Path() string` | Marker file path. |
| `WithDir(dir)` | Default `os.TempDir()`. |
| `WithStaleAfter(d)` | Reclaim markers older than d. |
| `WithStaleStrategy(s)` | Pick `PIDFirst` / `PIDOnly` / `TimeOnly`. |
| `WithMaxConcurrent(n)` | Semaphore mode. |
| `WithMetrics`, `WithSpanStarter`, `WithLogger`, `WithTraceIDExtractor` | Observability. |

## Examples

### Single cron singleton

The classic "skip if already running" pattern. Five lines including
imports.

```go
locks := filelock.NewFactory(filelock.WithDir("/var/run/myservice"))

err := locks.WithLock(ctx, "nightly-report", func(ctx context.Context) error {
    return generateReport(ctx)
})
if errors.Is(err, filelock.ErrLocked) {
    return // another worker has it; skip silently
}
return err
```

### Crash recovery with `WithStaleAfter`

If a job legitimately runs for ~10 minutes and the host occasionally
hard-crashes, set a stale window so the next run can take over. The
PID probe usually decides first; the time window is the cross-host /
inconclusive fallback.

```go
err := locks.WithLock(ctx, "import-orders", importOrders,
    filelock.WithStaleAfter(30*time.Minute), // generous; only used if probe is inconclusive
)
```

### Different stale windows per cron

`WithStaleAfter` is per-call. The factory holds the shared `dir`; each
call site picks the right window for its job. Mailerlite finishes in
seconds; Shopify sync may legitimately run for hours.

```go
_ = locks.WithLock(ctx, "mailerlite-sync", mailerliteSync,
    filelock.WithStaleAfter(10*time.Minute))

_ = locks.WithLock(ctx, "shopify-orders", shopifyOrders,
    filelock.WithStaleAfter(12*time.Hour))
```

### Semaphore — up to N parallel workers

`WithMaxConcurrent(n)` lets up to N holders run the same name
simultaneously. Useful for "throughput-bounded but cap at N" workers.

```go
err := locks.WithLock(ctx, "indexer", indexBatch,
    filelock.WithMaxConcurrent(3),
)
// Up to 3 indexers run in parallel; the 4th caller gets ErrLocked.
```

### Fencing tokens for "reject stale writer"

The Kleppmann scenario: process A acquires, gets paused (GC, swap,
ctrl-Z) for a long time, the lock is taken over, then A wakes up and
tries to write with stale data. Fencing tokens stop A's write
downstream.

```go
holder, err := locks.Acquire(ctx, "payment-export")
if err != nil { return err }
defer holder.Release()

token := holder.Token() // monotonic uint64
if err := s3.PutWithFence(ctx, "payments/today.csv", data, token); err != nil {
    return err
}
// downstream wrapper rejects writes with token <= highest seen
```

### Periodic sweep

Crashed holders' markers don't always get reclaimed during a normal
acquire (especially in semaphore mode where another slot might be
free). Run `Sweep` from a cron, protected by its own filelock so two
sweepers don't race:

```go
go func() {
    t := time.NewTicker(5 * time.Minute)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            _ = locks.WithLock(ctx, "filelock-sweep", func(ctx context.Context) error {
                n, err := locks.Sweep(ctx)
                slog.Info("filelock sweep", "reclaimed", n, "err", err)
                return nil
            })
        }
    }
}()
```

### Observability — slog + TraceID propagation

Wire a `slog.Logger` so every Acquire / Release emits a structured
event, and a `TraceIDExtractor` so the active OTel TraceID lands in
the marker file. Operators reading a stale marker can jump straight
to the trace that originally acquired it.

```go
import "go.opentelemetry.io/otel/trace"

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

`cat /var/run/myservice/import-orders.lock` then shows
`trace_id=4bf92f3577b34da6a3ce929d0e0e4736`.

### Hot-swap to in-memory for tests

Production wires a `*filelock.Factory`; tests wire a
`*memlock.Factory`. Both satisfy the same surface (and the same
`lock.Locker` interface). Tests run in milliseconds with zero
filesystem flakiness.

```go
import "github.com/ubgo/lock/memlock"

func TestProcessPayments(t *testing.T) {
    locks := memlock.NewFactory()
    err := processPayments(ctx, locks) // production code accepts a *Factory or lock.Locker
    // ...
}
```

### gocron adapter (separate module)

If you use `github.com/go-co-op/gocron/v2`, the `contrib/gocronlock`
module gives you a one-line wire-up that hands gocron a distributed
locker:

```bash
go get github.com/ubgo/lock/contrib/gocronlock
```

```go
import (
    "github.com/go-co-op/gocron/v2"
    "github.com/ubgo/lock/filelock"
    "github.com/ubgo/lock/contrib/gocronlock"
)

locks := filelock.NewFactory(filelock.WithDir("/var/run"))

scheduler, _ := gocron.NewScheduler(
    gocron.WithDistributedLocker(gocronlock.New(locks.AsLocker())),
)
scheduler.NewJob(
    gocron.DurationJob(time.Minute),
    gocron.NewTask(syncJob),
    gocron.WithName("sync-data"),
)
// gocron auto-locks each job by name. No manual filelock.WithLock needed.
```

The adapter is a separate Go module so the gocron dep tree (cron
parser, clockwork, uuid) stays out of users that don't need it.

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
