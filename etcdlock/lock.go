package etcdlock

import (
	"context"
	"errors"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// Lock is a single-name etcd-backed distributed lock. Construct via [New].
type Lock struct {
	cli  *clientv3.Client
	name string
	cfg  config
}

// New returns a [Lock] for the given name backed by cli (an etcd v3 client).
func New(cli *clientv3.Client, name string, opts ...Option) *Lock {
	return &Lock{
		cli:  cli,
		name: name,
		cfg:  applyOptions(defaultConfig(), opts),
	}
}

// Name returns the lock name.
func (l *Lock) Name() string {
	return l.name
}

// Acquire creates a new etcd session (lease) and tries to grab the
// concurrency.Mutex for this name. Returns [ErrLocked] when no
// slot is available; any other error is from the etcd client.
//
// In semaphore mode (cfg.maxConcurrent > 1) acquire iterates slots
// 0..n-1, each with its own concurrency.Mutex prefix.
func (l *Lock) Acquire(ctx context.Context, opts ...Option) (*Holder, error) {
	cfg := applyOptions(l.cfg, opts)
	return acquire(ctx, l.cli, l.name, cfg)
}

// acquire is the shared internal worker.
//
// Flow:
//  1. Create a concurrency.Session (etcd lease + auto-keepalive).
//  2. Build a Mutex on the per-name (or per-slot) prefix.
//  3. TryLock — non-blocking acquire. Returns ErrLocked on contention.
//  4. Capture mod_revision as the fencing token.
//  5. If a TraceID is set, PUT it as the value of the lock key for
//     operator visibility via `etcdctl get`.
//
// On any error after the session is created we close the session so
// the lease doesn't leak.
func acquire(ctx context.Context, cli *clientv3.Client, name string, cfg config) (h *Holder, err error) {
	start := time.Now()
	traceID := cfg.observe.extractTraceID(ctx)

	ctx, finishSpan := cfg.observe.startSpan(ctx, "etcdlock.Acquire")
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
		h, err = acquireSlot(ctx, cli, name, cfg, -1, traceID)
		return h, err
	}
	for slot := 0; slot < cfg.maxConcurrent; slot++ {
		h, err = acquireSlot(ctx, cli, name, cfg, slot, traceID)
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

// acquireSlot creates a session and tries to lock one slot. Pass
// slot=-1 for singleton mode; slot>=0 for the per-slot prefix
// `<prefix>/<name>/<slot>`.
func acquireSlot(ctx context.Context, cli *clientv3.Client, name string, cfg config, slot int, traceID string) (*Holder, error) {
	ttlSecs := int(cfg.ttl.Seconds())
	if ttlSecs < 5 {
		ttlSecs = 5
	}
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(ttlSecs), concurrency.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("etcdlock: new session: %w", err)
	}

	prefix := cfg.prefix + "/" + name
	if slot >= 0 {
		prefix = fmt.Sprintf("%s/%s/%d", cfg.prefix, name, slot)
	}
	mutex := concurrency.NewMutex(session, prefix)

	if err := mutex.TryLock(ctx); err != nil {
		if errors.Is(err, concurrency.ErrLocked) {
			_ = session.Close()
			return nil, ErrLocked
		}
		_ = session.Close()
		return nil, fmt.Errorf("etcdlock: try_lock: %w", err)
	}

	// mod_revision of the lock key is the fencing token. Globally
	// monotonic across the etcd cluster.
	token := uint64(mutex.Header().Revision) //nolint:gosec // etcd revisions are non-negative

	// If a TraceID is configured, PUT it as the value of the lock
	// key (concurrency.Mutex sets value="" by default). Best-effort:
	// if the PUT fails, the lock is still valid.
	if traceID != "" {
		_, _ = cli.Put(ctx, mutex.Key(), "trace:"+traceID, clientv3.WithLease(session.Lease()))
	}

	return &Holder{
		session: session,
		mutex:   mutex,
		token:   token,
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
