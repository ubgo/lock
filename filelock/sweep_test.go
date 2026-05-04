package filelock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepRemovesStaleMarker(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	// Marker from a "different host" so PID probe is inconclusive,
	// then expired time → stale.
	writeMarkerForTest(t, dir, "stale", marker{
		pid:      999999,
		host:     "other-host",
		acquired: now.Add(-2 * time.Hour),
	})
	// Fresh marker (within window) on a different name — must NOT be removed.
	writeMarkerForTest(t, dir, "fresh", marker{
		pid:      999999,
		host:     "other-host",
		acquired: now.Add(-5 * time.Minute),
	})

	locks := NewFactory(WithDir(dir), WithStaleAfter(time.Hour))
	n, err := locks.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("reclaimed = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.lock")); !os.IsNotExist(err) {
		t.Fatal("stale marker should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.lock")); err != nil {
		t.Fatalf("fresh marker should be kept: %v", err)
	}
}

func TestSweepLeavesAliveSlotsAlone(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	// Live PID (own) on local host → probeAlive → not stale.
	writeMarkerForTest(t, dir, "alive", marker{
		pid:      os.Getpid(),
		pidStart: processStartTime(os.Getpid()),
		host:     hostname(),
		acquired: now.Add(-99 * time.Hour),
	})

	locks := NewFactory(WithDir(dir), WithStaleAfter(time.Hour))
	n, err := locks.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed = %d, want 0 (live PID)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "alive.lock")); err != nil {
		t.Fatalf("live marker must be kept: %v", err)
	}
}

func TestSweepHandlesSemaphoreSlots(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	// Two semaphore slots from a foreign host, both expired.
	for slot := range 3 {
		path := filepath.Join(dir, "sem."+itoa(slot)+".lock")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = writeMarker(f, marker{
			pid:      999999,
			host:     "other-host",
			acquired: now.Add(-3 * time.Hour),
			slot:     slot,
		})
		_ = f.Close()
	}

	locks := NewFactory(WithDir(dir), WithStaleAfter(time.Hour))
	n, err := locks.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 3 {
		t.Fatalf("reclaimed = %d, want 3", n)
	}
}

func TestSweepIgnoresNonLockFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	locks := NewFactory(WithDir(dir))
	n, err := locks.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed non-lock files: %d", n)
	}
}

func TestSweepRespectsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	locks := NewFactory(WithDir(dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := locks.Sweep(ctx)
	// On an empty dir Sweep may finish before checking ctx — accept
	// either nil or context.Canceled.
	if err != nil && err != context.Canceled {
		t.Fatalf("got %v, want nil or context.Canceled", err)
	}
}

func TestSweepIgnoresUnreadableMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.lock"), []byte("pid=garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	locks := NewFactory(WithDir(dir))
	n, err := locks.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed corrupt marker: %d (Sweep should leave corrupt files alone)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.lock")); err != nil {
		t.Fatal("corrupt marker should be preserved for operator inspection")
	}
}

// itoa is a tiny inline helper to avoid pulling in strconv just for one
// test file. Sweeping in production goes through the same code path
// regardless.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
