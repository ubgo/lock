package redislock

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Holder represents an acquired distributed lock. Returned by
// Acquire; Release returns it to the pool by deleting the Redis key.
//
// A Holder is single-use: once Release returns, subsequent calls are
// no-ops. Holders are safe for concurrent use.
type Holder struct {
	rdb        redis.Scripter
	key        string
	value      string
	token      uint64
	ttl        time.Duration
	name       string    // recorded for observability
	acquiredAt time.Time // wall-clock at Acquire; used for HoldDuration
	observe    observeOptions
	released   atomic.Bool
}

// Token returns the monotonic fencing token assigned at Acquire.
// Use this downstream to reject writes from stale holders — the
// classic "process paused for GC then resumed" defense.
func (h *Holder) Token() uint64 {
	return h.token
}

// Key returns the Redis key backing this holder. Useful for logging
// and operator tooling (`redis-cli GET <key>` shows the holder
// value, `TTL <key>` shows remaining lease).
func (h *Holder) Key() string {
	return h.key
}

// Release deletes the Redis key — but only if the key's value still
// matches this holder's unique value. If it doesn't (TTL expired,
// admin force-deleted), Release returns [ErrLockLost] and the caller
// should treat their work as not-exclusive-anymore.
//
// Calling Release more than once is a no-op.
func (h *Holder) Release() error {
	return h.ReleaseContext(context.Background())
}

// ReleaseContext is like Release but threads ctx through the Redis
// call. Use when you have a deadline / cancellation discipline that
// extends to cleanup.
func (h *Holder) ReleaseContext(ctx context.Context) error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	res, err := releaseScript.Run(ctx, h.rdb, []string{h.key}, h.value).Result()

	// Observability: emit even on error so operators see the issue.
	if !h.acquiredAt.IsZero() {
		h.observe.recordHold(h.name, time.Since(h.acquiredAt))
	}
	h.observe.recordActive(h.name, -1)

	if err != nil {
		return fmt.Errorf("redislock: release: %w", err)
	}
	deleted, ok := res.(int64)
	if !ok || deleted == 0 {
		return ErrLockLost
	}
	return nil
}

// Extend bumps the lock's TTL back to its original duration, but
// only if the lock is still ours. Use this from a long-running
// holder that wants to keep the lock past its initial TTL — e.g.
// from a periodic auto-renewal goroutine.
//
// Returns [ErrLockLost] if the key no longer holds our value (TTL
// already expired and somebody else may have acquired it).
func (h *Holder) Extend(ctx context.Context) error {
	if h.released.Load() {
		return errors.New("redislock: holder already released")
	}
	ms := h.ttl.Milliseconds()
	res, err := extendScript.Run(ctx, h.rdb, []string{h.key}, h.value, ms).Result()
	if err != nil {
		return fmt.Errorf("redislock: extend: %w", err)
	}
	ok, _ := res.(int64)
	if ok == 0 {
		return ErrLockLost
	}
	return nil
}
