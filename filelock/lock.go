package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lock is a single-name marker-file lock. Construct one via [New]; the
// value is immutable so the same Lock can be safely shared across
// goroutines. For services with many lock names that share configuration
// (a common dir, a common stale window, etc.), prefer [Factory] — Lock
// is the standalone form for one-off cases.
type Lock struct {
	name string
	cfg  config
}

// New returns a [Lock] for the given name. The name is used as the
// marker filename: `<name>.lock`. Directory defaults to [os.TempDir];
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

// Path returns the absolute path of the marker file the lock will
// create on Acquire. The directory portion can be overridden per-call
// via Acquire(ctx, WithDir(...)) — Path reflects the lock's static
// configuration, not any per-call override.
func (l *Lock) Path() string {
	return markerPath(l.cfg.dir, l.name)
}

// Acquire creates the marker file. Returns a [Holder] on success;
// returns [ErrLocked] if another live holder already holds it; any
// other error indicates a filesystem problem.
//
// If the existing marker represents a crashed or otherwise-dead holder,
// Acquire takes it over per the configured [StaleStrategy] (default
// [StaleStrategyPIDFirst]). See MISSION §4.6–§4.8 for the algorithm.
//
// ctx is honoured at entry — a cancelled context returns ctx.Err()
// without touching the filesystem. There is no Wait variant; Acquire
// either succeeds, takes over a stale lock, or returns ErrLocked.
//
// Per-call opts override the Lock's construction-time configuration
// for this Acquire only; the Lock itself is unchanged.
func (l *Lock) Acquire(ctx context.Context, opts ...Option) (*Holder, error) {
	cfg := applyOptions(l.cfg, opts)
	return acquire(ctx, l.name, cfg)
}

// markerPath returns the marker file path for a given dir+name. Kept
// in one place so the layout convention (<dir>/<name>.lock) lives next
// to the code that reads and writes it. Singleton mode (the default
// and the only mode prior to M5) keeps the v0.1 layout.
func markerPath(dir, name string) string {
	return filepath.Join(dir, name+".lock")
}

// slotMarkerPath returns the per-slot marker file path used in
// semaphore mode (n>1). The format is `<dir>/<name>.<slot>.lock` —
// flat (no nested directory) because os.MkdirAll on the lock dir is
// already idempotent and adding a per-name subdir buys nothing.
func slotMarkerPath(dir, name string, slot int) string {
	return filepath.Join(dir, fmt.Sprintf("%s.%d.lock", name, slot))
}

// acquire is the internal worker shared by Lock.Acquire and
// Factory.Acquire.
//
// Algorithm (MISSION §4.8):
//
//  1. Try atomic O_CREATE|O_EXCL. If it succeeds, write the marker
//     body and return — fresh acquire.
//  2. If the marker exists, read it and decide via the strategy
//     whether the existing holder is alive (ErrLocked) or stale
//     (take it over via temp-file + rename).
//
// In semaphore mode (cfg.maxConcurrent > 1) the algorithm runs against
// each slot in order until one succeeds; if every slot returns
// ErrLocked the function returns ErrLocked. Other errors short-circuit
// (we don't keep trying slots after a filesystem error).
//
// Observability — if any of [WithMetrics] / [WithSpanStarter] /
// [WithLogger] / [WithTraceIDExtractor] is configured, this function
// emits the corresponding events.
func acquire(ctx context.Context, name string, cfg config) (h *Holder, err error) {
	start := time.Now()
	cfg.traceID = cfg.observe.extractTraceID(ctx)

	ctx, finishSpan := cfg.observe.startSpan(ctx, "filelock.Acquire")
	defer func() {
		finishSpan(err)
		outcome := outcomeFor(err)
		cfg.observe.recordAcquire(name, outcome, time.Since(start), err)
		if outcome == OutcomeAcquired {
			cfg.observe.recordActive(name, +1)
			if h != nil {
				h.observe = cfg.observe
				h.name = name
				h.acquiredAt = nowFn()
			}
		}
	}()

	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(cfg.dir, 0o755); err != nil {
		return nil, fmt.Errorf("filelock: mkdir: %w", err)
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

// outcomeFor maps an Acquire error into the stable Outcome string used
// by metrics labels and log fields.
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

// acquireSlot runs the §4.8 algorithm against a single slot. Pass
// slot=-1 for singleton mode (uses the v0.1 layout); slot>=0 selects
// `<dir>/<name>.<slot>.lock`.
//
// On success the holder's fencing token is bumped via the per-name
// sidecar (see fence.go). The fence bump is best-effort and never
// blocks acquisition.
func acquireSlot(name string, cfg config, slot int) (*Holder, error) {
	slotCfg := cfg
	if slot >= 0 {
		slotCfg.slot = slot
		slotCfg.slotIsSet = true
	}

	var path string
	if slot < 0 {
		path = markerPath(cfg.dir, name)
	} else {
		path = slotMarkerPath(cfg.dir, name, slot)
	}

	h, err := acquireMarker(path, slotCfg)
	if err != nil {
		return nil, err
	}
	h.token = bumpFence(cfg.dir, name)
	return h, nil
}

// acquireMarker is the marker-creation half of acquireSlot. Split out
// so fence-token bumping can wrap it without nested error-handling.
func acquireMarker(path string, cfg config) (*Holder, error) {
	// Step 1: fast path — try to create the marker exclusively.
	if h, err := tryCreateMarker(path, cfg); err == nil {
		return h, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	// Step 2: marker exists. Decide stale vs held.
	existing, readErr := readMarker(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			// Marker raced away between OpenFile and readMarker. Retry
			// the fast path once — most contended caller wins.
			if h, err := tryCreateMarker(path, cfg); err == nil {
				return h, nil
			} else if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			return nil, ErrLocked
		}
		// Marker is unreadable (corrupt, permission). Don't take over
		// silently — surface the error so an operator sees it.
		return nil, fmt.Errorf("filelock: read marker: %w", readErr)
	}

	if !isStale(existing, cfg) {
		return nil, ErrLocked
	}

	// Stale → take over via temp file + rename. The rename is atomic
	// on POSIX; on Windows os.Rename uses MoveFileEx which is also
	// atomic for replace-existing.
	return takeoverMarker(path, cfg)
}

// tryCreateMarker attempts the O_CREATE|O_EXCL fast path. Returns a
// Holder on success, os.ErrExist if the marker already exists, or any
// other os/filesystem error.
func tryCreateMarker(path string, cfg config) (*Holder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if writeErr := writeMarker(f, buildMarker(cfg)); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("filelock: write marker: %w", writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("filelock: close: %w", closeErr)
	}
	return &Holder{path: path}, nil
}

// takeoverMarker atomically replaces an existing marker. The temp file
// is created in the same directory so os.Rename is a same-filesystem
// operation (cross-fs renames are not atomic on POSIX).
func takeoverMarker(path string, cfg config) (*Holder, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".filelock-takeover-*")
	if err != nil {
		return nil, fmt.Errorf("filelock: takeover tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if writeErr := writeMarker(tmp, buildMarker(cfg)); writeErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("filelock: write takeover marker: %w", writeErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("filelock: close takeover marker: %w", closeErr)
	}
	if renameErr := os.Rename(tmpPath, path); renameErr != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("filelock: rename takeover marker: %w", renameErr)
	}
	return &Holder{path: path}, nil
}
