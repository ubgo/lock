package redislock

import (
	"context"
	"log/slog"
	"time"

	"github.com/ubgo/lock"
)

// MetricsRecorder is an alias for [lock.MetricsRecorder]. Defined
// in the root [github.com/ubgo/lock] module so every backend in the
// family shares one set of types.
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
// given Acquire call.
type observeOptions struct {
	metrics MetricsRecorder
	span    SpanStarter
	logger  *slog.Logger
	traceID TraceIDExtractor
}

// WithMetrics installs a [MetricsRecorder].
func WithMetrics(r MetricsRecorder) Option {
	return func(c *config) { c.observe.metrics = r }
}

// WithSpanStarter installs a [SpanStarter] for tracing.
func WithSpanStarter(s SpanStarter) Option {
	return func(c *config) { c.observe.span = s }
}

// WithLogger installs an slog.Logger that receives structured
// events on every Acquire / Release.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.observe.logger = l }
}

// WithTraceIDExtractor installs a function that pulls a TraceID out
// of the acquire context. The returned ID, when non-empty, is
// embedded in the Redis holder value (alongside the random
// identifier) so operators inspecting `redis-cli GET <key>` see
// the trace that originated the lock.
func WithTraceIDExtractor(ext TraceIDExtractor) Option {
	return func(c *config) { c.observe.traceID = ext }
}

func noopSpan(ctx context.Context, _ string) (context.Context, func(err error)) {
	return ctx, func(error) {}
}

func (o observeOptions) startSpan(ctx context.Context, op string) (context.Context, func(error)) {
	if o.span == nil {
		return noopSpan(ctx, op)
	}
	return o.span(ctx, op)
}

func (o observeOptions) recordAcquire(name, outcome string, duration time.Duration, traceID string, err error) {
	if o.metrics != nil {
		o.metrics.AcquireAttempt(name, outcome, duration)
	}
	if o.logger != nil {
		attrs := []slog.Attr{
			slog.String("name", name),
			slog.String("outcome", outcome),
			slog.Duration("duration", duration),
			slog.Any("error", err),
		}
		if traceID != "" {
			attrs = append(attrs, slog.String("trace_id", traceID))
		}
		o.logger.LogAttrs(context.Background(), slog.LevelInfo, "redislock acquire", attrs...)
	}
}

func (o observeOptions) recordActive(name string, delta int) {
	if o.metrics != nil {
		o.metrics.ActiveLocksDelta(name, delta)
	}
}

func (o observeOptions) recordHold(name string, duration time.Duration) {
	if o.metrics != nil {
		o.metrics.HoldDuration(name, duration)
	}
	if o.logger != nil {
		o.logger.LogAttrs(context.Background(), slog.LevelInfo, "redislock release",
			slog.String("name", name),
			slog.Duration("hold", duration),
		)
	}
}

func (o observeOptions) extractTraceID(ctx context.Context) string {
	if o.traceID == nil {
		return ""
	}
	return o.traceID(ctx)
}
