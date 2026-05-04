//go:build windows

package flock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// Windows lock-file flags (from winbase.h). The stdlib syscall package
// doesn't export these as named constants, so we declare them locally.
const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

// errLockViolation is the Windows error code returned when LockFileEx
// fails because someone else holds the lock and we asked for
// non-blocking semantics. ERROR_LOCK_VIOLATION = 33.
const errLockViolation syscall.Errno = 33

// kernel32.dll!LockFileEx — declared lazily so we don't pay the DLL
// load on processes that never use this package.
var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

// tryLock acquires a non-blocking exclusive advisory lock on f via
// LockFileEx with LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY.
// The kernel releases the lock when the file handle is closed or the
// process exits — same crash-safety guarantee as flock(2) on Unix.
func tryLock(f *os.File) error {
	var overlapped syscall.Overlapped
	r1, _, err := procLockFileEx.Call(
		uintptr(f.Fd()),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		uintptr(0),          // reserved
		uintptr(0xFFFFFFFF), // bytes to lock (low)
		uintptr(0xFFFFFFFF), // bytes to lock (high)
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 != 0 {
		return nil
	}
	if errors.Is(err, errLockViolation) {
		return ErrLocked
	}
	return err
}
