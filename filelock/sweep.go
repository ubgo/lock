package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sweep walks the factory's directory and reclaims marker files that
// are stale according to the factory's default [StaleStrategy] /
// [WithStaleAfter] configuration. Returns the number of markers
// removed.
//
// Sweep is the cleanup mechanism for crashed or interrupted holders
// whose markers Acquire never had reason to inspect (Acquire only
// reclaims the slot it actively wants — see MISSION §4.10–§4.11).
// In semaphore mode (n>1) markers can otherwise pile up indefinitely
// if a holder crashes and the next caller happens to find a different
// free slot.
//
// # When to run it
//
// Run Sweep periodically (a cron / [Factory.WithLock] sweep is fine).
// Recommended interval: half the shortest stale window you care
// about — e.g. if the tightest [WithStaleAfter] in your service is
// 10 minutes, sweep every 5.
//
// Wrap Sweep in its own filelock.WithLock with a name like
// "filelock-sweep" to avoid two sweepers stepping on each other:
//
//	locks := filelock.NewFactory(filelock.WithDir("/var/run"))
//	_ = locks.WithLock(ctx, "filelock-sweep", func(ctx context.Context) error {
//	    n, err := locks.Sweep(ctx)
//	    log.Info("filelock sweep", "reclaimed", n, "err", err)
//	    return nil
//	})
//
// # Race semantics
//
// Sweep reads each marker, applies the staleness check, and re-reads
// before removing — only deleting when the second read returns the
// same identity as the first. This catches the obvious race where
// another caller's takeover renames a fresh marker into place between
// our check and our remove. Without [flock] advisory locking (planned
// for the ubgo/flock sister module) Sweep cannot be perfectly
// race-free; the failure modes are bounded and recoverable
// (MISSION §4.11):
//
//   - Sweep removes a fresh marker → next Acquire creates a new one,
//     no correctness loss.
//   - Sweep skips a stale marker → next Sweep gets it. Eventually
//     consistent.
//
// Sweep uses the factory's default config to evaluate staleness; per
// call-site overrides (different stale windows per lock name) are not
// reachable through Sweep. This is by design — debug fields written
// to markers are never trusted by readers (§4.5). If your service
// has wildly varying stale windows, set the factory default
// conservatively (long) so Sweep doesn't reclaim too aggressively;
// per-call sites with tighter windows still take over via Acquire.
//
// Errors from filesystem operations on individual markers are
// swallowed and the sweep continues — Sweep is best-effort cleanup,
// not a transaction. A directory-level error (cannot read the dir)
// is returned.
func (f *Factory) Sweep(ctx context.Context) (int, error) {
	cfg := f.defaults
	entries, err := os.ReadDir(cfg.dir)
	if err != nil {
		return 0, fmt.Errorf("filelock: sweep readdir: %w", err)
	}
	reclaimed := 0
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return reclaimed, ctxErr
		}
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		path := filepath.Join(cfg.dir, name)
		if reclaimOne(path, cfg) {
			reclaimed++
		}
	}
	return reclaimed, nil
}

// reclaimOne reads, checks, re-reads and (if still stale) removes a
// single marker. Returns true iff the marker was successfully removed.
func reclaimOne(path string, cfg config) bool {
	first, err := readMarker(path)
	if err != nil {
		// Vanished or unreadable — leave it (unreadable markers are an
		// operator concern; we don't want Sweep to mask corruption by
		// silently deleting them).
		return false
	}
	if !isStale(first, cfg) {
		return false
	}

	// Re-read: if the marker has been replaced by a fresh acquire
	// (different acquired time or PID), don't remove.
	second, err := readMarker(path)
	if err != nil {
		return false
	}
	if !second.acquired.Equal(first.acquired) || second.pid != first.pid {
		return false
	}

	if err := os.Remove(path); err != nil {
		// Already gone (raced with another sweeper or an Acquire
		// takeover) is fine; report nothing reclaimed by us.
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		return false
	}
	return true
}
