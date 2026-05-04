package flock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// fencePath returns the sidecar file path for a given dir+name.
// Singleton and semaphore modes share the same fence path: the
// fence counter is per-name, not per-slot, so a single monotonic
// sequence covers all holders of that lock name.
func fencePath(dir, name string) string {
	return filepath.Join(dir, name+".fence")
}

// bumpFence reads the current fence value from disk, increments
// it, writes the new value via temp-file + rename, and returns
// the new value. Errors return zero — the caller treats zero as
// "fencing failed for this acquire" rather than blocking the lock
// acquisition over a sidecar write.
func bumpFence(dir, name string) uint64 {
	path := fencePath(dir, name)
	prev := readFence(path)
	next := prev + 1
	if err := writeFence(path, next); err != nil {
		return 0
	}
	return next
}

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

// writeFence atomically replaces path with v. Uses temp file +
// rename so a partial write never produces a half-readable fence
// file — readFence either sees the old value or the new one,
// never garbage.
func writeFence(path string, v uint64) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".flock-fence-*")
	if err != nil {
		return fmt.Errorf("flock: fence tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "%d\n", v); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("flock: fence write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("flock: fence close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("flock: fence rename: %w", err)
		}
	}
	return nil
}
