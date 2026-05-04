//go:build unix

package flock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock acquires a non-blocking exclusive advisory lock on f via
// flock(2) with LOCK_EX|LOCK_NB. The kernel:
//
//   - records the lock against the file descriptor (NOT the inode on
//     Linux — see flock(2) man page on that quirk);
//   - releases it the instant the fd is closed (Holder.Release) OR
//     the process exits (fork/exec'd children of a holder DO inherit
//     the lock; Linux flock semantics);
//   - returns EWOULDBLOCK / EAGAIN if another holder is currently
//     locking the same file.
//
// We translate EWOULDBLOCK into [ErrLocked] so callers get the same
// sentinel error across the lock family.
func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrLocked
	}
	return err
}
