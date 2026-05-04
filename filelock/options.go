package filelock

import (
	"os"
	"time"
)

// Option configures a [Lock], a [Factory], or a single Acquire / WithLock
// call. The same Option type works in all three places — see
// applyOptions for the precedence rules (per-call beats factory beats
// library default).
type Option func(*config)

// config is the resolved set of options for one Acquire call. It is
// internal: callers compose options via the public Option type and the
// constructors below; the package combines them at acquire time.
type config struct {
	dir            string
	staleAfter     time.Duration // 0 == not set
	staleAfterIsOn bool          // distinguishes "0 because unset" from "0 explicitly"
	strategy       StaleStrategy
	maxConcurrent  int  // 1 == singleton (default), n>1 == semaphore
	slot           int  // populated by acquire when probing slot N; not user-set
	slotIsSet      bool // marks slot as a real slot index (not just zero)
	observe        observeOptions
	traceID        string // populated by acquire from observe.extractTraceID(ctx); not user-set
}

// defaultConfig returns the library-default config used when neither a
// factory nor a per-call option overrides a field.
func defaultConfig() config {
	return config{
		dir:           os.TempDir(),
		strategy:      StaleStrategyPIDFirst,
		maxConcurrent: 1,
	}
}

// applyOptions returns base after applying opts in order.
func applyOptions(base config, opts []Option) config {
	cfg := base
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithDir places the marker file in dir. When set on a [Factory] it
// applies to every Acquire / WithLock call made through that factory; a
// per-call WithDir overrides it. Default is [os.TempDir].
func WithDir(dir string) Option {
	return func(c *config) { c.dir = dir }
}

// WithStaleAfter configures how long after a marker's recorded acquire
// time it may be reclaimed by a new Acquire. The window is consulted
// only when the strategy says so:
//
//   - [StaleStrategyPIDFirst] (default): consulted as fallback when the
//     PID probe is inconclusive (different host, permission denied).
//   - [StaleStrategyPIDOnly]: never consulted (warns if set).
//   - [StaleStrategyTimeOnly]: always consulted; required for
//     auto-takeover under this strategy.
//
// IMPORTANT: this is NOT a runtime cap on the holder's job. It is the
// "how long after the holder appears dead may we take over?" window.
// See MISSION §4.9 for the foot-gun walkthrough — using a 5-minute
// WithStaleAfter on TimeOnly while the protected job legitimately runs
// for 6 minutes leads to two parallel runs.
//
// A duration of zero is treated as "not set" — the strategy decides
// what to do without a time window.
func WithStaleAfter(d time.Duration) Option {
	return func(c *config) {
		c.staleAfter = d
		c.staleAfterIsOn = d > 0
	}
}

// WithStaleStrategy selects the stale-detection mode. See
// [StaleStrategy] for the three values and when to pick each.
func WithStaleStrategy(s StaleStrategy) Option {
	return func(c *config) { c.strategy = s }
}

// WithMaxConcurrent enables semaphore mode: up to n holders may hold
// the lock for the same name simultaneously. Default n=1 is singleton
// mode (the v0.1 behaviour). Values < 1 are clamped to 1.
//
// Layout — singleton mode keeps the v0.1 path `<dir>/<name>.lock`;
// n>1 uses `<dir>/<name>.<slot>.lock` for slot in 0..n-1.
//
// IMPORTANT — the N-must-agree rule (MISSION §4.10): every caller of
// the same lock name must pass the same n. Mixing n=2 and n=3 callers
// silently breaks the limit because the n=2 caller never probes slot 2.
// We deliberately do NOT persist n in a metadata file — its migration
// story is uglier than the bug it would prevent. Deploy callers
// together with matching n.
//
// Stale-takeover during Acquire reclaims only the slot being probed.
// To clean up other crashed slots, run [Factory.Sweep] periodically
// (planned in M6). The single-slot-takeover keeps Acquire's semantics
// easy to reason about.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}
