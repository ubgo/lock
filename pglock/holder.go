package pglock

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Holder represents an acquired Postgres advisory lock. It owns a
// dedicated connection pulled from the pool — that connection's
// session holds the lock until Release returns the connection (and
// runs pg_advisory_unlock), or the connection's underlying socket
// dies.
//
// A Holder is single-use: once Release returns, subsequent calls are
// no-ops.
type Holder struct {
	conn       *pgxpool.Conn
	key        int64
	token      uint64    // txid_current() captured at Acquire (see Token)
	name       string    // recorded for observability
	acquiredAt time.Time // wall-clock at Acquire; used for HoldDuration
	observe    observeOptions
	released   atomic.Bool
}

// Key returns the bigint key Postgres sees for this lock. Useful for
// operator queries — `SELECT pid FROM pg_locks WHERE locktype =
// 'advisory' AND objid = <key>` shows who holds it.
func (h *Holder) Key() int64 {
	return h.key
}

// Token returns the Postgres transaction ID captured at the time
// of Acquire — `txid_current()`. Postgres txids are monotonic per
// instance; use as a fencing token to defend against stale-holder
// writes downstream.
//
// A token of zero means the txid query failed (rare: connection
// blip after the advisory lock was held). Treat zero as "fencing
// disabled for this acquire."
func (h *Holder) Token() uint64 {
	return h.token
}

// Release runs pg_advisory_unlock on the held connection and returns
// the connection to the pool. Calling Release more than once is a
// no-op.
//
// If the process crashes without calling Release, Postgres releases
// the advisory lock automatically when it notices the session has
// disconnected — the killer feature of pglock.
func (h *Holder) Release() error {
	return h.ReleaseContext(context.Background())
}

// ReleaseContext threads ctx through the pg_advisory_unlock call.
func (h *Holder) ReleaseContext(ctx context.Context) error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	var ok bool
	err := h.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", h.key).Scan(&ok)
	h.conn.Release()

	if !h.acquiredAt.IsZero() {
		h.observe.recordHold(h.name, time.Since(h.acquiredAt))
	}
	h.observe.recordActive(h.name, -1)

	if err != nil {
		return fmt.Errorf("pglock: advisory_unlock: %w", err)
	}
	return nil
}
