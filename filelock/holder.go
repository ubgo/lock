package filelock

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Holder represents an acquired lock. It is returned by Acquire and must
// be released via [Holder.Release] when the protected work is done.
//
// A Holder is single-use: once Release returns successfully (or with the
// error surfaced from os.Remove), subsequent calls are no-ops. Holders
// are safe for concurrent use, though there's rarely a reason to release
// from more than one goroutine.
type Holder struct {
	path       string
	token      uint64    // fencing token assigned at Acquire (see Token method)
	name       string    // lock name; recorded for observability
	acquiredAt time.Time // wall-clock at Acquire; used for HoldDuration
	observe    observeOptions
	released   atomic.Bool
}

// Path returns the absolute path of the marker file backing this holder.
// Useful for logging and debugging — operators reading
// `cat <path>` will see the marker's identity and debug fields.
func (h *Holder) Path() string {
	return h.path
}

// Release removes the marker file. Calling Release more than once is a
// no-op — the second and subsequent calls return nil without touching
// the filesystem. This makes `defer holder.Release()` safe even when
// some earlier code path also released explicitly.
//
// Errors other than "file does not exist" are surfaced to the caller —
// they typically indicate filesystem trouble (permission, read-only fs)
// that the caller probably wants to log.
func (h *Holder) Release() error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	err := os.Remove(h.path)
	switch {
	case err == nil:
		// fine
	case errors.Is(err, os.ErrNotExist):
		err = nil
	default:
		err = fmt.Errorf("filelock: remove: %w", err)
	}

	// Observability: emit metrics + log even on error so operators
	// see the failure. Hold duration is from acquired-at to now.
	if !h.acquiredAt.IsZero() {
		h.observe.recordHold(h.name, nowFn().Sub(h.acquiredAt))
	}
	h.observe.recordActive(h.name, -1)
	return err
}
