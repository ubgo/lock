package filelock

import (
	"context"
	"log/slog"
	"time"

	"github.com/ubgo/lock"
)

// MetricsRecorder is an alias for [lock.MetricsRecorder]. Defined
// in the root [github.com/ubgo/lock] module so every backend in the
// family shares one set of types. The alias preserves filelock's
// pre-v0.2 import paths.
type MetricsRecorder = lock.MetricsRecorder

// SpanStarter is an alias for [lock.SpanStarter].
type SpanStarter = lock.SpanStarter

// TraceIDExtractor is an alias for [lock.TraceIDExtractor].
type TraceIDExtractor = lock.TraceIDExtractor

// Outcome* — re-exported aliases of the root constants. Stable
// strings used by metrics/log labels across every backend.
const (
	OutcomeAcquired  = lock.OutcomeAcquired
	OutcomeErrLocked = lock.OutcomeErrLocked
	OutcomeError     = lock.OutcomeError
)

// observeOptions carries every observability hook configured for a
// given Acquire call. Held inside config so per-call overrides work.
type observeOptions struct {
	metrics      MetricsRecorder
	span         SpanStarter
	logger       *slog.Logger
	traceID      TraceIDExtractor
	loggerIsSet  bool // distinguishes a nil-but-set logger from default
	metricsIsSet bool
}

// WithMetrics installs a [MetricsRecorder]. Pass a nil recorder to
// disable metrics on a per-call basis.
func WithMetrics(r MetricsRecorder) Option {
	return func(c *config) {
		c.observe.metrics = r
		c.observe.metricsIsSet = true
	}
}

// WithSpanStarter installs a [SpanStarter] for tracing.
func WithSpanStarter(s SpanStarter) Option {
	return func(c *config) { c.observe.span = s }
}

// WithLogger installs a [slog.Logger] that will receive structured
// events for every Acquire / Release / Sweep. Pass [slog.Default]() to
// route events to the application's default handler. A nil logger
// silences logging.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		c.observe.logger = l
		c.observe.loggerIsSet = true
	}
}

// WithTraceIDExtractor installs a function that pulls a trace ID out
// of the acquire context. The returned ID, when non-empty, is written
// to the marker's `trace_id` debug field so operators inspecting a
// stale marker can find the originating request in their tracing UI.
func WithTraceIDExtractor(ext TraceIDExtractor) Option {
	return func(c *config) { c.observe.traceID = ext }
}

// noopSpan is the SpanStarter used when no tracer is configured.
// Returning the original ctx and a no-op finalizer keeps the call
// sites clean (no nil checks).
func noopSpan(ctx context.Context, _ string) (context.Context, func(err error)) {
	return ctx, func(error) {}
}

// startSpan returns the configured SpanStarter or [noopSpan].
func (o observeOptions) startSpan(ctx context.Context, op string) (context.Context, func(error)) {
	if o.span == nil {
		return noopSpan(ctx, op)
	}
	return o.span(ctx, op)
}

// recordAcquire forwards an acquire outcome to metrics + log if
// configured. duration is wall-clock time taken in Acquire.
func (o observeOptions) recordAcquire(name, outcome string, duration time.Duration, err error) {
	if o.metrics != nil {
		o.metrics.AcquireAttempt(name, outcome, duration)
	}
	if o.logger != nil {
		o.logger.LogAttrs(context.Background(), slog.LevelInfo, "filelock acquire",
			slog.String("name", name),
			slog.String("outcome", outcome),
			slog.Duration("duration", duration),
			slog.Any("error", err),
		)
	}
}

// recordActive forwards an active-locks delta to metrics if configured.
func (o observeOptions) recordActive(name string, delta int) {
	if o.metrics != nil {
		o.metrics.ActiveLocksDelta(name, delta)
	}
}

// recordHold forwards a hold-duration to metrics + log if configured.
func (o observeOptions) recordHold(name string, duration time.Duration) {
	if o.metrics != nil {
		o.metrics.HoldDuration(name, duration)
	}
	if o.logger != nil {
		o.logger.LogAttrs(context.Background(), slog.LevelInfo, "filelock release",
			slog.String("name", name),
			slog.Duration("hold", duration),
		)
	}
}

// extractTraceID is a safe wrapper that handles a nil extractor.
func (o observeOptions) extractTraceID(ctx context.Context) string {
	if o.traceID == nil {
		return ""
	}
	return o.traceID(ctx)
}
