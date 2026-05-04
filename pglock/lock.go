package pglock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Lock is a single-name Postgres advisory lock. Construct one via [New].
type Lock struct {
	pool *pgxpool.Pool
	name string
	cfg  config
}

// New returns a [Lock] for the given name backed by pool. The pool
// must allow at least one dedicated connection at the time of
// Acquire — pglock pulls a connection out and holds it for the
// duration of the lock.
func New(pool *pgxpool.Pool, name string, opts ...Option) *Lock {
	return &Lock{
		pool: pool,
		name: name,
		cfg:  applyOptions(defaultConfig(), opts),
	}
}

// Name returns the lock name.
func (l *Lock) Name() string {
	return l.name
}

// Key returns the int64 Postgres sees for this lock (FNV-1a of name
// plus any [WithKeyOffset]). In semaphore mode this is the slot-0
// key; per-slot keys are k+0, k+1, …, k+n-1.
func (l *Lock) Key() int64 {
	return hashKey(l.name, l.cfg.keyOffset)
}

// Acquire pulls a dedicated connection from the pool and runs
// pg_try_advisory_lock on it. Returns a [Holder] on success;
// [ErrLocked] when no slot is available; or any error from the
// pool / Postgres.
//
// In semaphore mode (cfg.maxConcurrent > 1) acquire iterates slots
// 0..n-1 (each at key k+slot) and returns the first one that
// succeeds.
func (l *Lock) Acquire(ctx context.Context, opts ...Option) (*Holder, error) {
	cfg := applyOptions(l.cfg, opts)
	return acquire(ctx, l.pool, l.name, cfg)
}

// acquire is the shared internal worker.
func acquire(ctx context.Context, pool *pgxpool.Pool, name string, cfg config) (h *Holder, err error) {
	start := time.Now()
	traceID := cfg.observe.extractTraceID(ctx)

	ctx, finishSpan := cfg.observe.startSpan(ctx, "pglock.Acquire")
	defer func() {
		finishSpan(err)
		outcome := outcomeFor(err)
		cfg.observe.recordAcquire(name, outcome, time.Since(start), traceID, err)
		if outcome == OutcomeAcquired {
			cfg.observe.recordActive(name, +1)
			if h != nil {
				h.observe = cfg.observe
				h.name = name
				h.acquiredAt = time.Now()
			}
		}
	}()

	if err = ctx.Err(); err != nil {
		return nil, err
	}

	if cfg.maxConcurrent <= 1 {
		h, err = acquireSlot(ctx, pool, name, cfg, 0, traceID, false)
		return h, err
	}
	for slot := 0; slot < cfg.maxConcurrent; slot++ {
		h, err = acquireSlot(ctx, pool, name, cfg, slot, traceID, true)
		if err == nil {
			return h, nil
		}
		if errors.Is(err, ErrLocked) {
			continue
		}
		return nil, err
	}
	return nil, ErrLocked
}

// acquireSlot tries to take the advisory lock for a specific slot.
// In singleton mode (semaphore=false) the slot is always 0 and the
// key is k+0 == k. In semaphore mode the key is k+slot.
func acquireSlot(ctx context.Context, pool *pgxpool.Pool, name string, cfg config, slot int, traceID string, semaphore bool) (*Holder, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("pglock: pool acquire: %w", err)
	}

	baseKey := hashKey(name, cfg.keyOffset)
	key := baseKey
	if semaphore {
		key = baseKey + int64(slot) //nolint:gosec // intentional bit-arith
	}

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		conn.Release()
		return nil, fmt.Errorf("pglock: try_advisory_lock: %w", err)
	}
	if !got {
		conn.Release()
		return nil, ErrLocked
	}

	// If a TraceID is configured, set application_name on the session
	// so operators querying pg_stat_activity see the originating trace.
	if traceID != "" {
		// SET application_name is session-scoped; it persists for as
		// long as we hold the connection. Failure here is non-fatal.
		_, _ = conn.Exec(ctx, "SET application_name = $1", "lock:"+name+"|trace:"+traceID)
	}

	// Capture txid_current() as the fencing token. Best-effort:
	// failure leaves token=0 which Holder.Token() documents as
	// "fencing failed for this acquire."
	var txid uint64
	_ = conn.QueryRow(ctx, "SELECT txid_current()").Scan(&txid)

	return &Holder{
		conn:  conn,
		key:   key,
		token: txid,
	}, nil
}

// outcomeFor maps an Acquire error into the stable Outcome string.
func outcomeFor(err error) string {
	switch {
	case err == nil:
		return OutcomeAcquired
	case errors.Is(err, ErrLocked):
		return OutcomeErrLocked
	default:
		return OutcomeError
	}
}
