package filelock

// StaleStrategy controls how Acquire decides whether an existing marker
// represents a still-running holder or a crashed-and-left-behind one.
//
// The default is [StaleStrategyPIDFirst], which is the right choice for
// almost every single-host workload: it uses the PID liveness probe as
// the primary signal and falls back to the time window only when the
// probe can't give a definitive answer (different host, different UID,
// missing /proc, etc.).
//
// See MISSION §4.7 for the full design rationale and §4.9 for the
// "what WithStaleAfter does NOT do" walkthrough — easy to misuse if
// you assume it's a runtime cap instead of a stale-takeover window.
type StaleStrategy int

const (
	// StaleStrategyPIDFirst probes the PID first; on a conclusive
	// answer (alive or dead) uses it, otherwise falls back to the
	// time window from WithStaleAfter (if set) or returns ErrLocked.
	// This is the default.
	StaleStrategyPIDFirst StaleStrategy = iota

	// StaleStrategyPIDOnly probes the PID and returns the answer if
	// conclusive; on inconclusive (cross-host, cross-UID, etc.) returns
	// ErrLocked. The time window is never consulted. Use this when
	// you'd rather wait for an operator than risk taking over a lock
	// that might still be held.
	StaleStrategyPIDOnly

	// StaleStrategyTimeOnly skips the PID probe entirely and uses the
	// WithStaleAfter window. Appropriate on filesystems where the PID
	// probe isn't meaningful (NFS without host filtering, exotic
	// containers). The window MUST be ≥ p99 healthy run time of the
	// protected job — see MISSION §4.9 for why. Without WithStaleAfter
	// this strategy can never auto-takeover.
	StaleStrategyTimeOnly
)

// String returns the wire-format name of the strategy as written to
// the marker's debug field. Stable across versions for grep-ability.
func (s StaleStrategy) String() string {
	switch s {
	case StaleStrategyPIDFirst:
		return "pid-first"
	case StaleStrategyPIDOnly:
		return "pid-only"
	case StaleStrategyTimeOnly:
		return "time-only"
	default:
		return "unknown"
	}
}
