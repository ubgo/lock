package lock

import (
	"context"
	"time"
)

// Outcome enumerates how an Acquire attempt ended. Stable string
// values across backends — observability backends (Prometheus
// labels, OTel attributes, log fields) can rely on them across
// versions.
const (
	OutcomeAcquired  = "acquired"  // Acquire returned a Holder.
	OutcomeErrLocked = "errlocked" // Acquire returned ErrLocked.
	OutcomeError     = "error"     // Acquire returned any other error.
)

// MetricsRecorder is the integration point for metrics backends.
// The interface is defined in stdlib types only so applications
// can wire in Prometheus, OTel meters, statsd, or anything else
// without forcing every consumer to pull in those deps.
//
// Implementations must be safe for concurrent use.
type MetricsRecorder interface {
	// AcquireAttempt records an Acquire that finished — successfully
	// or otherwise. outcome is one of the Outcome* constants;
	// duration is wall-clock time spent in Acquire.
	AcquireAttempt(name, outcome string, duration time.Duration)

	// HoldDuration records how long a successful Holder was held
	// before Release. Called once per Release on the original
	// (non-idempotent) call.
	HoldDuration(name string, duration time.Duration)

	// ActiveLocksDelta reports a change in the count of currently
	// held locks for name (+1 on Acquire, -1 on Release). Backends
	// that expose a gauge sum these into the running total.
	ActiveLocksDelta(name string, delta int)
}

// SpanStarter is the integration point for tracing backends. It is
// invoked at the start of every Acquire / Release / Sweep, and the
// returned func is called when the operation completes. Designed to
// match the OpenTelemetry Tracer.Start shape so a one-line adapter
// can wire it up without dragging the otel module into core.
//
//	import "go.opentelemetry.io/otel"
//	starter := func(ctx context.Context, op string) (context.Context, func(err error)) {
//	    ctx, span := otel.Tracer("lock").Start(ctx, op)
//	    return ctx, func(err error) {
//	        if err != nil {
//	            span.RecordError(err)
//	        }
//	        span.End()
//	    }
//	}
type SpanStarter func(ctx context.Context, operation string) (context.Context, func(err error))

// TraceIDExtractor pulls a trace ID out of a context for inclusion
// in lock state — backends that store identifying info (filelock
// markers, redislock holder values, etc.) record the TraceID so
// operators reading lock state can jump to the originating trace.
// Default: returns "" (no trace ID embedded).
type TraceIDExtractor func(ctx context.Context) string
