package filelock

import "time"

// probeResult is the verdict of a PID liveness probe. The acquire
// algorithm cares about three states, not two — "I asked the OS and
// it told me 'permission denied'" is not the same as "alive" or
// "dead", and conflating them produces silent bugs (see MISSION §4.6
// failure mode (d)).
type probeResult int

const (
	// probeAlive means the OS confirmed the PID is a running process AND,
	// when an expected start time was provided, that start time matches.
	// The lock is held by a live process — caller should return ErrLocked.
	probeAlive probeResult = iota

	// probeDead means the OS confirmed the PID is not a running process,
	// OR the PID is alive but its start time disagrees with the marker
	// (PID reuse). The marker is stale; caller may take over.
	probeDead

	// probeInconclusive means the OS could not give a definitive answer
	// — typical causes: target PID belongs to a different UID, the
	// marker came from a different host, the platform doesn't expose a
	// reliable probe. Caller falls back to the time-based stale window
	// (PIDFirst) or returns ErrLocked (PIDOnly) per the strategy.
	probeInconclusive
)

// String renders the result for log output. Stable across versions —
// downstream tooling can grep for these literal strings.
func (r probeResult) String() string {
	switch r {
	case probeAlive:
		return "alive"
	case probeDead:
		return "dead"
	case probeInconclusive:
		return "inconclusive"
	default:
		return "unknown"
	}
}

// probePID asks the OS whether pid is currently a running process. If
// expectedStart is non-zero AND the platform exposes a start time for
// pid, the two are compared: a mismatch means the PID was recycled
// after the marker was written, and the result is [probeDead].
//
// pid <= 0 always returns [probeDead] — there is no such process.
//
// If the marker's host doesn't match the local hostname the caller
// must NOT call this function: PIDs are host-local and the answer
// would be meaningless. Cross-host gating happens in the acquire
// algorithm before probePID is reached (MISSION §4.6 (b), §4.8).
func probePID(pid int, expectedStart time.Time) probeResult {
	if pid <= 0 {
		return probeDead
	}
	alive := isAlive(pid)
	if alive != probeAlive {
		return alive
	}
	if expectedStart.IsZero() {
		// Marker has no recorded start time (M2-era marker, or a
		// platform that doesn't expose one). We can't detect PID reuse.
		// Trust the alive answer.
		return probeAlive
	}
	actualStart := processStartTime(pid)
	if actualStart.IsZero() {
		// Platform exposes alive-check but not start time. Same
		// situation as above — trust alive.
		return probeAlive
	}
	if !actualStart.Equal(expectedStart) {
		// PID number reused since the marker was written. The original
		// holder is gone; what's running now is something else.
		return probeDead
	}
	return probeAlive
}
