//go:build !linux

package filelock

import "time"

// processStartTime returns the zero Time on non-Linux platforms because
// extracting the kernel's recorded start time for an arbitrary PID
// requires either cgo (sysctl on macOS, GetProcessTimes on Windows) or
// a third-party syscall library, both of which we avoid in the v0.2
// stdlib-only core.
//
// The probe layer treats a zero start time as "no PID-reuse check
// available" and trusts isAlive on its own — which is correct on
// macOS and Windows for the in-cluster cron-singleton workloads
// filelock targets (PID-reuse on these OSes is rare on the timescales
// where a marker would still be on disk; the alive/dead signal alone
// is sufficient for the typical case).
//
// A future contrib module can add a real implementation when a user
// actually needs cross-platform PID-reuse detection.
func processStartTime(_ int) time.Time {
	return time.Time{}
}
