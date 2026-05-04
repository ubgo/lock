package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Token returns the monotonic fencing token assigned to this Holder at
// Acquire time. Pass it to downstream services to reject writes from
// stale lock holders — the canonical defense against the "process
// paused (GC, swap, ctrl-Z) then resumed and wrote with stale data"
// scenario described by Kleppmann's "How to do distributed locking".
//
// # Single-host scope
//
// In ubgo/filelock, fences are stored as a sidecar file
// `<dir>/<name>.fence` and incremented per Acquire. Within a single
// host this provides best-effort monotonicity:
//
//   - Singleton mode (n=1): the marker file IS the critical section,
//     so fence increments are naturally serialized across Acquires.
//     Strict monotonicity holds.
//   - Semaphore mode (n>1): fence increments race across concurrent
//     slot acquires. Two near-simultaneous Acquires can read the same
//     prior value and write the same new value, giving two holders
//     the SAME token. Downstream "reject if token <= highest seen"
//     consumers handle this correctly by accepting >= (and they
//     should, since semaphore mode permits N writers anyway).
//
// # The same API across the family
//
// The same Token method is exposed by every backend in the ubgo/lock-*
// family. Distributed backends use their store's native primitive for
// strict monotonicity: etcd's mod_revision, Postgres' txid_current(),
// Redis-with-Lua atomic INCR. Caller code is identical regardless of
// backend.
//
// A token of zero means the fence file could not be read or written
// (operator-visible: corrupt fence sidecar, unwritable directory).
// Treat zero as "fencing disabled for this acquire" — acceptable for
// best-effort uses, ignore at your peril if you depend on it.
func (h *Holder) Token() uint64 {
	return h.token
}

// fencePath returns the sidecar file path for a given dir+name.
// Singleton and semaphore mode share the same fence path: the fence
// counter is per-name, not per-slot, so a single monotonic sequence
// covers all holders of that lock name.
func fencePath(dir, name string) string {
	return filepath.Join(dir, name+".fence")
}

// bumpFence reads the current fence value from disk, increments it,
// writes the new value via temp-file + rename, and returns the new
// value. Errors return zero — the caller treats zero as "fencing
// failed for this acquire" rather than blocking the lock acquisition
// over a sidecar write.
func bumpFence(dir, name string) uint64 {
	path := fencePath(dir, name)
	prev := readFence(path)
	next := prev + 1
	if err := writeFence(path, next); err != nil {
		return 0
	}
	return next
}

// readFence reads the decimal uint64 from path. Returns 0 on any
// error: missing file (first acquire), permission, malformed contents.
// We never want a fence read failure to crash Acquire — fencing is
// best-effort additional info, not the primary lock mechanism.
func readFence(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// writeFence atomically replaces path with v. Uses temp file + rename
// so a partial write never produces a half-readable fence file —
// readFence either sees the old value or the new one, never garbage.
func writeFence(path string, v uint64) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".filelock-fence-*")
	if err != nil {
		return fmt.Errorf("filelock: fence tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "%d\n", v); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filelock: fence write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filelock: fence close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		// Concurrent rename collision — ignore; the other writer's
		// value will be the next prev for our caller's retry.
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("filelock: fence rename: %w", err)
		}
	}
	return nil
}
