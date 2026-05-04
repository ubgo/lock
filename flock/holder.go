package flock

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Holder represents an acquired lock. It is returned by Acquire and
// must be released via [Holder.Release] when the protected work is
// done. The kernel will also release the lock automatically if the
// process exits without calling Release (the killer feature of flock
// vs marker-file locks).
//
// A Holder is single-use: once Release returns, subsequent calls are
// no-ops. Holders are safe for concurrent use, though there's rarely
// a reason to release from more than one goroutine.
type Holder struct {
	path       string
	file       *os.File
	token      uint64    // fencing token assigned at Acquire (see Token)
	name       string    // lock name; recorded for observability
	acquiredAt time.Time // wall-clock at Acquire; used for HoldDuration
	observe    observeOptions
	released   atomic.Bool
}

// Path returns the absolute path of the lock file backing this
// holder. Useful for logging.
func (h *Holder) Path() string {
	return h.path
}

// Token returns the monotonic fencing token assigned at Acquire.
// Backed by a per-name sidecar counter (`<dir>/<name>.fence`)
// incremented atomically on every successful Acquire.
//
// Strict monotonicity in singleton mode (the kernel's flock holds
// the marker file exclusively while we bump). Best-effort in
// semaphore mode, where multiple slot acquires concurrently
// increment the same per-name counter without explicit
// synchronization — see [docs/design/fencing-tokens.md] for the
// full discussion.
//
// A token of zero means the fence sidecar could not be read or
// written (operator-visible: corrupt fence file, unwritable
// directory). Treat zero as "fencing disabled for this acquire."
func (h *Holder) Token() uint64 {
	return h.token
}

// Release closes the file descriptor — the kernel drops the
// associated advisory lock immediately, allowing another waiter (or
// the next caller of Acquire) to take it. Calling Release more than
// once is a no-op.
//
// Errors from os.File.Close are surfaced; the lock is considered
// released regardless of whether Close succeeded (the OS will tear
// down the fd on process exit at the latest).
func (h *Holder) Release() error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	closeErr := h.file.Close()
	removeErr := os.Remove(h.path)

	if !h.acquiredAt.IsZero() {
		h.observe.recordHold(h.name, time.Since(h.acquiredAt))
	}
	h.observe.recordActive(h.name, -1)

	switch {
	case closeErr == nil && (removeErr == nil || errors.Is(removeErr, os.ErrNotExist)):
		return nil
	case closeErr != nil:
		return fmt.Errorf("flock: close: %w", closeErr)
	default:
		return fmt.Errorf("flock: remove: %w", removeErr)
	}
}
