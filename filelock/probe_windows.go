//go:build windows

package filelock

import (
	"errors"
	"syscall"
)

// Windows Errno values for the cases we care about. The stdlib
// `syscall` package only exports ERROR_FILE_NOT_FOUND and a handful
// of others on Windows, so we declare the numeric codes locally
// rather than pull in golang.org/x/sys.
const (
	errInvalidParameter syscall.Errno = 87
	errAccessDenied     syscall.Errno = 5
)

// isAlive checks whether pid is a running process via OpenProcess with
// PROCESS_QUERY_LIMITED_INFORMATION (lighter than PROCESS_QUERY_INFORMATION,
// works for processes the caller can't fully inspect).
//
//   - OpenProcess succeeds → process exists → probeAlive.
//   - ERROR_INVALID_PARAMETER → typically "no such process" on Windows
//     when the PID doesn't exist → probeDead.
//   - ERROR_ACCESS_DENIED → process exists but caller lacks rights →
//     probeInconclusive.
//   - any other error → probeInconclusive.
func isAlive(pid int) probeResult {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(h)
		return probeAlive
	}
	if errors.Is(err, errInvalidParameter) {
		return probeDead
	}
	if errors.Is(err, errAccessDenied) {
		return probeInconclusive
	}
	return probeInconclusive
}
