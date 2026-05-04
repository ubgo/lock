package filelock

import (
	"os"
	"time"
)

// buildMarker assembles a marker for the current acquire call. Identity
// fields are sourced from the OS at acquire time; debug fields reflect
// what the writer believed about its own configuration at the time of
// acquire — see MISSION §4.5 for why those values are NEVER trusted by
// readers, only displayed.
func buildMarker(cfg config) marker {
	m := marker{
		pid:      os.Getpid(),
		pidStart: processStartTime(os.Getpid()),
		host:     hostname(),
		acquired: nowFn(),
		strategy: cfg.strategy.String(),
	}
	if cfg.staleAfterIsOn {
		m.staleAfter = cfg.staleAfter.String()
	} else {
		m.staleAfter = "none"
	}
	if cfg.maxConcurrent > 1 {
		m.maxConcurrent = cfg.maxConcurrent
	}
	if cfg.slotIsSet {
		m.slot = cfg.slot
	}
	m.traceID = cfg.traceID
	return m
}

// hostname returns the local hostname or "unknown" if [os.Hostname]
// fails. We never want the marker to refuse to write because of a
// hostname lookup oddity (containers without hostname set, etc.) —
// staleness logic treats "unknown" as a host mismatch which is the
// safe fall-through.
func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// nowFn is the package's clock. Tests swap it via [setNow] to make
// time-dependent behaviour (acquired-at, stale window) deterministic.
var nowFn = time.Now

// setNow replaces the package clock and returns a restore function.
// Test-only — do not use in production code.
func setNow(t time.Time) (restore func()) {
	prev := nowFn
	nowFn = func() time.Time { return t }
	return func() { nowFn = prev }
}
