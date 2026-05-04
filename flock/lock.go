package flock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lock is a single-name kernel-fenced advisory lock. Construct one
// via [New]; the value is immutable so the same Lock can be safely
// shared across goroutines.
type Lock struct {
	name string
	cfg  config
}

// New returns a [Lock] for the given name. The name is used as the
// lock filename: `<name>.lock`. Directory defaults to [os.TempDir];
// override with [WithDir].
func New(name string, opts ...Option) *Lock {
	return &Lock{
		name: name,
		cfg:  applyOptions(defaultConfig(), opts),
	}
}

// Name returns the lock name as supplied to [New].
func (l *Lock) Name() string {
	return l.name
}

// Path returns the absolute path of the lock file.
func (l *Lock) Path() string {
	return lockPath(l.cfg.dir, l.name)
}

// Acquire opens the lock file and takes a kernel-fenced advisory
// lock on it. Returns a [Holder] on success; [ErrLocked] if another
// process holds it; any other error is filesystem trouble.
//
// In semaphore mode (cfg.maxConcurrent > 1) acquire iterates slots
// 0..n-1 and returns the first one that succeeds; if every slot
// returns ErrLocked the function returns ErrLocked.
//
// ctx is honoured at entry — a cancelled context returns ctx.Err()
// without touching the filesystem. There is no Wait variant.
func (l *Lock) Acquire(ctx context.Context, opts ...Option) (*Holder, error) {
	cfg := applyOptions(l.cfg, opts)
	return acquire(ctx, l.name, cfg)
}

// lockPath returns the lock file path for singleton mode.
func lockPath(dir, name string) string {
	return filepath.Join(dir, name+".lock")
}

// slotLockPath returns the per-slot lock file path used in
// semaphore mode (n>1).
func slotLockPath(dir, name string, slot int) string {
	return filepath.Join(dir, fmt.Sprintf("%s.%d.lock", name, slot))
}

// acquire is the internal worker shared by Lock.Acquire and
// Factory.Acquire.
//
// Algorithm:
//
//  1. MkdirAll the directory.
//  2. If singleton mode, try one slot; otherwise iterate
//     0..maxConcurrent-1.
//  3. For each slot: open (or create) the lock file; call the
//     platform's non-blocking advisory lock (flock LOCK_NB on Unix,
//     LockFileEx with LOCKFILE_FAIL_IMMEDIATELY on Windows).
//  4. On success, bump the per-name fence counter and return a Holder.
//     On contention, close the fd and continue to the next slot.
//
// The kernel automatically releases the lock when the fd is closed
// (Holder.Release) OR when the process exits.
func acquire(ctx context.Context, name string, cfg config) (h *Holder, err error) {
	start := time.Now()
	traceID := cfg.observe.extractTraceID(ctx)

	ctx, finishSpan := cfg.observe.startSpan(ctx, "flock.Acquire")
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
	if err = os.MkdirAll(cfg.dir, 0o755); err != nil {
		return nil, fmt.Errorf("flock: mkdir: %w", err)
	}

	if cfg.maxConcurrent <= 1 {
		h, err = acquireSlot(name, cfg, -1)
		return h, err
	}
	for slot := 0; slot < cfg.maxConcurrent; slot++ {
		h, err = acquireSlot(name, cfg, slot)
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

// acquireSlot opens + locks one slot. Pass slot=-1 for singleton
// mode (uses the standard layout); slot>=0 selects
// `<dir>/<name>.<slot>.lock`.
func acquireSlot(name string, cfg config, slot int) (*Holder, error) {
	var path string
	if slot < 0 {
		path = lockPath(cfg.dir, name)
	} else {
		path = slotLockPath(cfg.dir, name, slot)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("flock: open: %w", err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock: lock: %w", err)
	}

	token := bumpFence(cfg.dir, name)
	return &Holder{path: path, file: f, token: token}, nil
}

// outcomeFor maps an Acquire error into the stable Outcome string
// used by metrics labels and log fields.
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
