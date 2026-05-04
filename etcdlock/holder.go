package etcdlock

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.etcd.io/etcd/client/v3/concurrency"
)

// Holder represents an acquired etcd-backed lock. Owns an etcd
// session (lease) and a concurrency.Mutex; Release tears both down.
type Holder struct {
	session    *concurrency.Session
	mutex      *concurrency.Mutex
	token      uint64
	name       string    // recorded for observability
	acquiredAt time.Time // wall-clock at Acquire; used for HoldDuration
	observe    observeOptions
	released   atomic.Bool
}

// Token returns the etcd mod_revision at which this holder won the
// lock. mod_revision is globally monotonic across the entire etcd
// cluster, so it's a stronger fencing token than per-name counters.
func (h *Holder) Token() uint64 {
	return h.token
}

// Key returns the etcd key actually written for this holder. Useful
// for `etcdctl get <key>` debugging.
func (h *Holder) Key() string {
	return h.mutex.Key()
}

// Release unlocks via the concurrency.Mutex (which deletes the
// holder's key) and closes the session (revoking the lease).
// Calling Release more than once is a no-op.
func (h *Holder) Release() error {
	return h.ReleaseContext(context.Background())
}

// ReleaseContext threads ctx through the unlock + session-close.
func (h *Holder) ReleaseContext(ctx context.Context) error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	unlockErr := h.mutex.Unlock(ctx)
	closeErr := h.session.Close()

	if !h.acquiredAt.IsZero() {
		h.observe.recordHold(h.name, time.Since(h.acquiredAt))
	}
	h.observe.recordActive(h.name, -1)

	switch {
	case unlockErr != nil:
		return fmt.Errorf("etcdlock: unlock: %w", unlockErr)
	case closeErr != nil:
		return fmt.Errorf("etcdlock: close session: %w", closeErr)
	default:
		return nil
	}
}
