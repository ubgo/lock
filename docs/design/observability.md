# Design — observability

> Hooks for slog (structured logging), metrics (Prometheus / OTel
> meters), tracing (OTel spans), and TraceID propagation into the
> marker file. Currently shipped on `filelock`; rolling out to the
> rest of the family.

## Why observability hooks aren't backend-specific

Locks are a coordination boundary — every Acquire is a
worth-knowing event. "How long did Acquires for `nightly-import`
take this week?" "Which jobs hit ErrLocked the most?" "Did the
3am cron actually run on prod-replica-2 last night?" These
questions have the same answers regardless of backend.

So instead of bolting metrics into one backend and not the others,
we ship interface-typed hooks that any backend can implement.

## Interfaces

```go
// MetricsRecorder — Prometheus / OTel meter integration.
type MetricsRecorder interface {
    AcquireAttempt(name, outcome string, duration time.Duration)
    HoldDuration(name string, duration time.Duration)
    ActiveLocksDelta(name string, delta int)
}

// SpanStarter — OTel tracing.
type SpanStarter func(ctx context.Context, operation string) (context.Context, func(err error))

// TraceIDExtractor — pull a trace ID from ctx for the marker file.
type TraceIDExtractor func(ctx context.Context) string

// Logger — stdlib slog.
// (Just *slog.Logger; no custom interface.)
```

Currently exposed on `filelock`'s `WithMetrics` / `WithSpanStarter`
/ `WithLogger` / `WithTraceIDExtractor` options. Rolling out to
the rest.

## Outcome constants

Stable string values across backends:

```go
const (
    OutcomeAcquired  = "acquired"
    OutcomeErrLocked = "errlocked"
    OutcomeError     = "error"
)
```

Use these as Prometheus labels / OTel attributes — they won't
churn across versions.

## Wiring Prometheus

Implement the interface; pass to the factory:

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/ubgo/lock/filelock"
)

type promRecorder struct {
    acquires  *prometheus.CounterVec
    duration  *prometheus.HistogramVec
    hold      *prometheus.HistogramVec
    active    *prometheus.GaugeVec
}

func newPromRecorder(reg prometheus.Registerer) *promRecorder {
    p := &promRecorder{
        acquires: prometheus.NewCounterVec(prometheus.CounterOpts{
            Name: "lock_acquire_total",
            Help: "Lock acquire attempts.",
        }, []string{"name", "outcome"}),
        duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
            Name: "lock_acquire_seconds",
            Help: "Acquire duration.",
            Buckets: prometheus.DefBuckets,
        }, []string{"name", "outcome"}),
        hold: prometheus.NewHistogramVec(prometheus.HistogramOpts{
            Name: "lock_hold_seconds",
            Help: "Hold duration.",
            Buckets: []float64{0.1, 1, 10, 60, 300, 1800, 7200},
        }, []string{"name"}),
        active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
            Name: "lock_active",
            Help: "Currently-held locks.",
        }, []string{"name"}),
    }
    reg.MustRegister(p.acquires, p.duration, p.hold, p.active)
    return p
}

func (p *promRecorder) AcquireAttempt(name, outcome string, d time.Duration) {
    p.acquires.WithLabelValues(name, outcome).Inc()
    p.duration.WithLabelValues(name, outcome).Observe(d.Seconds())
}

func (p *promRecorder) HoldDuration(name string, d time.Duration) {
    p.hold.WithLabelValues(name).Observe(d.Seconds())
}

func (p *promRecorder) ActiveLocksDelta(name string, delta int) {
    p.active.WithLabelValues(name).Add(float64(delta))
}

// Wire it up:
locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithMetrics(newPromRecorder(prometheus.DefaultRegisterer)),
)
```

Filelock package never imports `prometheus/client_golang` — your
service does. The interface is the boundary.

## Wiring OTel tracing

```go
import (
    "go.opentelemetry.io/otel"
    "github.com/ubgo/lock/filelock"
)

tracer := otel.Tracer("filelock")

starter := func(ctx context.Context, op string) (context.Context, func(error)) {
    ctx, span := tracer.Start(ctx, op)
    return ctx, func(err error) {
        if err != nil {
            span.RecordError(err)
        }
        span.End()
    }
}

locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithSpanStarter(starter),
)
```

Each Acquire becomes a child span of the request span. Sweep gets
its own span too.

## Wiring slog

```go
locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithLogger(slog.Default()),
)
```

Filelock emits structured events on Acquire/Release/Sweep:

```json
{"level":"INFO","msg":"filelock acquire","name":"nightly-import","outcome":"acquired","duration":12_400_000,"error":null}
{"level":"INFO","msg":"filelock release","name":"nightly-import","hold":1_842_000_000_000}
```

## TraceID propagation into the marker

The killer feature: when an OTel span is active, the active TraceID
gets written into the marker's `trace_id` debug field:

```go
locks := filelock.NewFactory(
    filelock.WithDir("/var/run"),
    filelock.WithTraceIDExtractor(func(ctx context.Context) string {
        if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
            return span.SpanContext().TraceID().String()
        }
        return ""
    }),
)
```

Now `cat /var/run/myservice/job.lock` shows:

```
# Identity
pid=12345
host=worker-3
acquired=2026-05-01T18:42:11Z

# Debug
trace_id=4bf92f3577b34da6a3ce929d0e0e4736
```

Operators finding a stale marker can paste the TraceID into Tempo
/ Jaeger / Honeycomb and find the originating request — including
which user, which input, what state the system was in when the
holder started.

This is **unique to the family** as far as we've surveyed. No
other Go locking library does this.

## Rollout status

| Backend | Logger | Metrics | SpanStarter | TraceID |
|---|---|---|---|---|
| `filelock` | ✅ | ✅ | ✅ | ✅ |
| `flock` | planned | planned | planned | n/a (no marker) |
| `redislock` | planned | planned | planned | planned (debug field on the SET) |
| `pglock` | planned | planned | planned | planned (via SET app_name) |
| `etcdlock` | planned | planned | planned | planned (debug field on the lock key) |
| `memlock` | not planned | not planned | not planned | n/a |

## Why not bake in Prometheus / OTel directly?

Because:

1. Different services use different metrics backends (statsd,
   Datadog, OTel meter, Prometheus directly). One choice is wrong
   for half of them.
2. Forcing a metrics dep on every consumer of `lock` defeats the
   "tiny core" goal.
3. The interface approach lets you test without hooking up real
   Prometheus.

This is the same pattern as `database/sql.DB.Driver` — interface
contract, swappable concrete implementations.
