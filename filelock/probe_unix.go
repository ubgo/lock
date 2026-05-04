//go:build unix

package filelock

import (
	"errors"
	"syscall"
)

// isAlive checks whether pid is a running process via signal 0.
//
//   - syscall.Kill(pid, 0) returns nil → process exists and is signalable
//     by us → probeAlive.
//   - ESRCH ("no such process") → probeDead.
//   - EPERM ("operation not permitted") → process exists but belongs to
//     a different UID. We can't probe further; return probeInconclusive
//     so callers can fall back to the time window.
//   - any other error → probeInconclusive (don't guess).
func isAlive(pid int) probeResult {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return probeAlive
	}
	if errors.Is(err, syscall.ESRCH) {
		return probeDead
	}
	if errors.Is(err, syscall.EPERM) {
		return probeInconclusive
	}
	return probeInconclusive
}
