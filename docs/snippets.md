# Snippets — copy-paste recipes

Curated patterns. Each is self-contained — paste, fill in the
business logic, ship.

## Table of contents

1. [Cron singleton (single replica)](#1-cron-singleton-single-replica)
2. [Cron singleton across N replicas (Redis)](#2-cron-singleton-across-n-replicas-redis)
3. [Cron singleton with Postgres (no TTL to tune)](#3-cron-singleton-with-postgres-no-ttl-to-tune)
4. [Cron singleton with kernel-fenced flock (single host)](#4-cron-singleton-with-kernel-fenced-flock-single-host)
5. [Long-running job that auto-extends its lease](#5-long-running-job-that-auto-extends-its-lease)
6. [Backend-agnostic service (lock.Locker)](#6-backend-agnostic-service-lockLocker)
7. [Tests with memlock (no infra)](#7-tests-with-memlock-no-infra)
8. [Fencing tokens — defend against GC pause](#8-fencing-tokens--defend-against-gc-pause)
9. [Periodic stale-marker sweep (filelock)](#9-periodic-stale-marker-sweep-filelock)
10. [Wire any backend into gocron](#10-wire-any-backend-into-gocron)
11. [Multiple lock names with shared config (Factory)](#11-multiple-lock-names-with-shared-config-factory)
12. [Per-call options override factory defaults](#12-per-call-options-override-factory-defaults)
13. [Semaphore — up to N parallel holders (filelock)](#13-semaphore--up-to-n-parallel-holders-filelock)
14. [Operator inspection — what's holding this lock?](#14-operator-inspection--whats-holding-this-lock)
15. [Observability hooks — slog + Prometheus + OTel](#15-observability-hooks--slog--prometheus--otel)

---

## 1. Cron singleton (single replica)

```go
import (
    "context"
    "errors"

    "github.com/ubgo/lock/filelock"
)

func nightlyImport(ctx context.Context) error {
    locks := filelock.NewFactory(filelock.WithDir("/var/run/myservice"))

    err := locks.WithLock(ctx, "nightly-import", func(ctx context.Context) error {
        return runImport(ctx)
    })
    if errors.Is(err, filelock.ErrLocked) {
        return nil // previous run still active; skip
    }
    return err
}
```

## 2. Cron singleton across N replicas (Redis)

```go
import (
    "context"
    "errors"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/ubgo/lock/redislock"
)

func midnightBilling(ctx context.Context, rdb *redis.Client) error {
    locks := redislock.NewFactory(rdb,
        redislock.WithTTL(10*time.Minute),
        redislock.WithKeyPrefix("billing"),
    )

    err := locks.WithLock(ctx, "midnight-billing", func(ctx context.Context) error {
        return processBilling(ctx)
    })
    if errors.Is(err, redislock.ErrLocked) {
        return nil // another replica owns it; we skip
    }
    return err
}
```

## 3. Cron singleton with Postgres (no TTL to tune)

```go
import (
    "context"
    "errors"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/ubgo/lock/pglock"
)

func paymentReconcile(ctx context.Context, pool *pgxpool.Pool) error {
    locks := pglock.NewFactory(pool)

    err := locks.WithLock(ctx, "payment-reconcile", func(ctx context.Context) error {
        return reconcile(ctx)
    })
    if errors.Is(err, pglock.ErrLocked) {
        return nil
    }
    return err
}
```

Postgres releases the advisory lock when the connection drops —
crash safety with no TTL to size.

## 4. Cron singleton with kernel-fenced flock (single host)

```go
import (
    "context"
    "errors"

    "github.com/ubgo/lock/flock"
)

func reportRun(ctx context.Context) error {
    fl := flock.New("nightly-report", flock.WithDir("/var/run"))

    holder, err := fl.Acquire(ctx)
    if errors.Is(err, flock.ErrLocked) {
        return nil
    }
    if err != nil {
        return err
    }
    defer holder.Release()

    return runReport(ctx)
}
```

If your process is killed (`kill -9`, OOM, kernel panic), the OS
releases the lock automatically when the file descriptor closes.
No PID probe, no Sweep, no markers to clean up.

## 5. Long-running job that auto-extends its lease

```go
import (
    "context"
    "time"

    "github.com/ubgo/lock/redislock"
)

func longExport(ctx context.Context, locks *redislock.Factory) error {
    holder, err := locks.Acquire(ctx, "long-export",
        redislock.WithTTL(2*time.Minute),
    )
    if err != nil {
        return err
    }
    defer holder.Release()

    // Renew every minute (well within the 2-minute TTL).
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()
    go func() {
        t := time.NewTicker(time.Minute)
        defer t.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-t.C:
                if err := holder.Extend(ctx); err != nil {
                    cancel() // lock lost — abort job
                    return
                }
            }
        }
    }()

    return runLongExport(ctx)
}
```

## 6. Backend-agnostic service (`lock.Locker`)

```go
package payments

import (
    "context"
    "errors"

    "github.com/ubgo/lock"
)

type Service struct {
    locks lock.Locker
}

func New(l lock.Locker) *Service {
    return &Service{locks: l}
}

func (s *Service) DailyExport(ctx context.Context) error {
    h, err := s.locks.Acquire(ctx, "daily-export")
    if errors.Is(err, lock.ErrLocked) {
        return nil
    }
    if err != nil {
        return err
    }
    defer h.Release()
    return s.runExport(ctx)
}
```

Wire any backend at startup:

```go
// flock for local dev:
svc := payments.New(flock.NewFactory(flock.WithDir("/tmp")).AsLocker())

// redislock for prod:
svc := payments.New(redislock.NewFactory(rdb).AsLocker())

// memlock for tests:
svc := payments.New(memlock.NewFactory().AsLocker())
```

## 7. Tests with memlock (no infra)

```go
import (
    "context"
    "testing"

    "github.com/ubgo/lock/memlock"
)

func TestDailyExport(t *testing.T) {
    locks := memlock.NewFactory()
    svc := payments.New(locks.AsLocker())

    if err := svc.DailyExport(context.Background()); err != nil {
        t.Fatal(err)
    }
    // No Redis, no Postgres, no filesystem — pure in-memory.
}
```

## 8. Fencing tokens — defend against GC pause

```go
holder, err := locks.Acquire(ctx, "payment-export")
if err != nil {
    return err
}
defer holder.Release()

fence := holder.Token() // monotonic uint64

if err := s3.PutWithFence(ctx, "payments/today.csv", data, fence); err != nil {
    return err
}
```

The downstream wrapper records the highest token it has seen and
rejects writes with `token < highest`. A's stale write (after a long
GC pause) fails; B's fresh write succeeds.

## 9. Periodic stale-marker sweep (filelock)

```go
import (
    "context"
    "log/slog"
    "time"

    "github.com/ubgo/lock/filelock"
)

func runSweeper(ctx context.Context, locks *filelock.Factory) {
    t := time.NewTicker(5 * time.Minute)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            // Wrap Sweep in its own filelock to avoid two sweepers
            // stepping on each other.
            _ = locks.WithLock(ctx, "filelock-sweep", func(ctx context.Context) error {
                n, err := locks.Sweep(ctx)
                slog.Info("filelock sweep", "reclaimed", n, "err", err)
                return nil
            })
        }
    }
}
```

## 10. Wire any backend into gocron

```go
import (
    "github.com/go-co-op/gocron/v2"
    "github.com/ubgo/lock/contrib/gocronlock"
    "github.com/ubgo/lock/redislock"
)

func newScheduler(rdb *redis.Client) (gocron.Scheduler, error) {
    locks := redislock.NewFactory(rdb)

    return gocron.NewScheduler(
        gocron.WithDistributedLocker(gocronlock.New(locks.AsLocker())),
    )
}
```

gocron auto-locks each job by name. No manual `WithLock` per job.

## 11. Multiple lock names with shared config (Factory)

The "28 cron singletons share one TempDir" use case:

```go
locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))

// Each call site is one line. Per-call options for per-job tuning.
_ = locks.WithLock(ctx, "syncgtmandga4", syncJob,
    filelock.WithStaleAfter(2*time.Hour))
_ = locks.WithLock(ctx, "mailerlitesubscribe", mailerliteJob,
    filelock.WithStaleAfter(10*time.Minute))
_ = locks.WithLock(ctx, "syncshopifyorders", shopifyJob,
    filelock.WithStaleAfter(12*time.Hour))
```

## 12. Per-call options override factory defaults

```go
locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithStaleAfter(time.Hour), // factory default
)

// Most call sites use the default:
_ = locks.WithLock(ctx, "regular-job", regularJob)

// Long-running cron overrides per-call:
_ = locks.WithLock(ctx, "monthly-bill-run", billRun,
    filelock.WithStaleAfter(48*time.Hour),
)
```

Resolution: `library default → factory default → per-call option`.
The factory itself is unchanged; per-call options scope to the
single Acquire.

## 13. Semaphore — up to N parallel holders (filelock)

```go
err := locks.WithLock(ctx, "indexer", indexBatch,
    filelock.WithMaxConcurrent(3),
)
```

Up to 3 indexers run in parallel; the 4th caller gets `ErrLocked`.
Internal layout: `<dir>/indexer.0.lock`, `.1.lock`, `.2.lock`.

## 14. Operator inspection — what's holding this lock?

For filelock, `cat <path>` gives a full marker:

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

For redislock:

```sh
$ redis-cli GET myservice:import-orders
"4bd9...ab12"
$ redis-cli TTL myservice:import-orders
(integer) 87
```

For pglock:

```sql
SELECT pid, granted, objid
FROM pg_locks
WHERE locktype = 'advisory' AND objid = $1; -- holder.Key()
```

For etcdlock:

```sh
$ etcdctl get /ubgo/etcdlock/import-orders/ --prefix
```

## 15. Observability hooks — slog + Prometheus + OTel

```go
import (
    "log/slog"

    "github.com/ubgo/lock/filelock"
    "go.opentelemetry.io/otel/trace"
)

locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithLogger(slog.Default()),
    filelock.WithMetrics(myPrometheusRecorder),
    filelock.WithSpanStarter(myOTelSpanStarter),
    filelock.WithTraceIDExtractor(func(ctx context.Context) string {
        if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
            return span.SpanContext().TraceID().String()
        }
        return ""
    }),
)
```

The marker now records the active TraceID so operators can jump
from a stale marker to the originating trace in Tempo / Jaeger /
Honeycomb.
