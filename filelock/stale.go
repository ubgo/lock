package filelock

// isStale runs the stale-detection algorithm (MISSION §4.8) against an
// existing marker, returning true when the marker should be reclaimed.
//
//	         ┌─────────────────────────────────────────────────────┐
//	strategy │ on existing marker, returns true (= take over) when │
//	─────────┼─────────────────────────────────────────────────────┤
//	PIDFirst │ probe says dead, OR probe inconclusive AND time     │
//	         │ window says expired                                 │
//	PIDOnly  │ probe says dead                                     │
//	TimeOnly │ time window says expired                            │
//
// "probe inconclusive" is broader than just EPERM — it also covers
// "different host" (PIDs are host-local) and "platform doesn't expose
// a probe at all".
func isStale(m marker, cfg config) bool {
	switch cfg.strategy {
	case StaleStrategyTimeOnly:
		return timeExpired(m, cfg)
	case StaleStrategyPIDFirst, StaleStrategyPIDOnly:
		probe := probeMarker(m)
		switch probe {
		case probeDead:
			return true
		case probeAlive:
			return false
		case probeInconclusive:
			if cfg.strategy == StaleStrategyPIDOnly {
				return false
			}
			return timeExpired(m, cfg)
		}
	}
	return false
}

// probeMarker maps a marker's identity fields into a probeResult,
// short-circuiting to inconclusive when host-based pre-conditions don't
// hold (different host, missing host or pid).
func probeMarker(m marker) probeResult {
	if m.pid <= 0 {
		// No PID recorded — pre-M2 marker or write was truncated.
		// Treat as inconclusive; the strategy decides the fallback.
		return probeInconclusive
	}
	if m.host == "" {
		return probeInconclusive
	}
	local := hostname()
	if local == "unknown" || m.host != local {
		// Cross-host probe is meaningless — PIDs are host-local.
		return probeInconclusive
	}
	return probePID(m.pid, m.pidStart)
}

// timeExpired reports whether the marker's recorded acquire time is
// older than the configured stale window. Returns false when the
// window is unset (no auto-takeover) or the marker has no acquired
// timestamp (corrupt / pre-M2).
func timeExpired(m marker, cfg config) bool {
	if !cfg.staleAfterIsOn {
		return false
	}
	if m.acquired.IsZero() {
		return false
	}
	return nowFn().Sub(m.acquired) > cfg.staleAfter
}
