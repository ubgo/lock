package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeMarkerForTest writes m to <dir>/<name>.lock and returns the path.
// Helper used to seed the filesystem with a marker that has specific
// identity fields, so the stale-detection logic can be tested without
// having to actually launch and crash a process.
func writeMarkerForTest(t *testing.T, dir, name string, m marker) string {
	t.Helper()
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	if err := writeMarker(f, m); err != nil {
		_ = f.Close()
		t.Fatalf("write marker: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

func TestStaleStrategyString(t *testing.T) {
	for _, c := range []struct {
		s    StaleStrategy
		want string
	}{
		{StaleStrategyPIDFirst, "pid-first"},
		{StaleStrategyPIDOnly, "pid-only"},
		{StaleStrategyTimeOnly, "time-only"},
		{StaleStrategy(99), "unknown"},
	} {
		if got := c.s.String(); got != c.want {
			t.Errorf("%d.String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// PIDFirst — host mismatch falls back to time window
func TestAcquireTakesOverWhenForeignHostAndTimeExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "fh", marker{
		pid:      999999,
		host:     "some-other-host",
		acquired: now.Add(-2 * time.Hour),
	})

	l := New("fh", WithDir(dir), WithStaleAfter(time.Hour))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	// New marker should reflect the local host, not "some-other-host".
	m, err := readMarker(h.Path())
	if err != nil {
		t.Fatal(err)
	}
	if m.host == "some-other-host" {
		t.Fatal("takeover did not rewrite host field")
	}
}

// PIDFirst — host mismatch but time window NOT expired → ErrLocked
func TestAcquireErrLockedWhenForeignHostAndTimeNotExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "fh2", marker{
		pid:      999999,
		host:     "some-other-host",
		acquired: now.Add(-30 * time.Minute),
	})

	l := New("fh2", WithDir(dir), WithStaleAfter(time.Hour))
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

// PIDOnly — host mismatch always returns ErrLocked, even when time would expire
func TestAcquirePIDOnlyForeignHostNeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "po", marker{
		pid:      999999,
		host:     "some-other-host",
		acquired: now.Add(-100 * time.Hour), // very stale by time
	})

	l := New("po", WithDir(dir),
		WithStaleStrategy(StaleStrategyPIDOnly),
		WithStaleAfter(time.Hour),
	)
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked (PIDOnly must not fall back to time)", err)
	}
}

// TimeOnly — local host but time window expired → take over
func TestAcquireTimeOnlyTakesOverWhenExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "to", marker{
		pid:      os.Getpid(), // would be probeAlive — but TimeOnly skips probe
		host:     hostname(),
		acquired: now.Add(-2 * time.Hour),
	})

	l := New("to", WithDir(dir),
		WithStaleStrategy(StaleStrategyTimeOnly),
		WithStaleAfter(time.Hour),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = h.Release()
}

// TimeOnly — within window → ErrLocked
func TestAcquireTimeOnlyErrLockedWithinWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "tow", marker{
		pid:      999999,
		host:     hostname(),
		acquired: now.Add(-30 * time.Minute),
	})

	l := New("tow", WithDir(dir),
		WithStaleStrategy(StaleStrategyTimeOnly),
		WithStaleAfter(time.Hour),
	)
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

// PIDFirst — local marker with dead PID → take over (Linux + Darwin both support kill(0))
func TestAcquirePIDFirstTakesOverWhenPIDIsDead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PID 4M dead-check works on unix; windows probe runs but uses a different syscall path covered elsewhere")
	}
	dir := t.TempDir()
	writeMarkerForTest(t, dir, "dp", marker{
		pid:      4_000_000, // implausibly high → probeDead
		host:     hostname(),
		acquired: time.Now(),
	})

	l := New("dp", WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = h.Release()
}

// PIDFirst — own PID matches marker → probeAlive → ErrLocked
func TestAcquirePIDFirstOwnPIDIsLocked(t *testing.T) {
	dir := t.TempDir()
	writeMarkerForTest(t, dir, "own", marker{
		pid:      os.Getpid(),
		pidStart: processStartTime(os.Getpid()),
		host:     hostname(),
		acquired: time.Now(),
	})

	l := New("own", WithDir(dir))
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

// PIDFirst on Linux — alive but start_time mismatch → take over (PID reuse)
func TestAcquirePIDFirstStartTimeMismatchTakesOver(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("start time mismatch detection requires Linux start-time impl")
	}
	dir := t.TempDir()
	writeMarkerForTest(t, dir, "rs", marker{
		pid:      os.Getpid(),
		pidStart: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), // wrong
		host:     hostname(),
		acquired: time.Now(),
	})

	l := New("rs", WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = h.Release()
}

// PIDFirst — fall through to time when host mismatches AND no time window → ErrLocked
func TestAcquirePIDFirstNoTimeWindowForeignHostErrLocked(t *testing.T) {
	dir := t.TempDir()
	writeMarkerForTest(t, dir, "nt", marker{
		pid:      1,
		host:     "different-host",
		acquired: time.Now().Add(-99 * time.Hour),
	})
	// No WithStaleAfter — so foreign-host inconclusive falls through to
	// "no time window" → ErrLocked.
	l := New("nt", WithDir(dir))
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

// Marker with corrupt content → surface error, do not silently take over.
func TestAcquireSurfacesCorruptMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.lock")
	if err := os.WriteFile(path, []byte("pid=not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := New("corrupt", WithDir(dir))
	_, err := l.Acquire(context.Background())
	if err == nil {
		t.Fatal("expected error from corrupt marker")
	}
	if errors.Is(err, ErrLocked) {
		t.Fatalf("corrupt marker should NOT be reported as ErrLocked, got %v", err)
	}
}

// Strategy + StaleAfter values are written to the marker debug fields.
func TestAcquireWritesDebugFields(t *testing.T) {
	dir := t.TempDir()
	l := New("debug", WithDir(dir),
		WithStaleStrategy(StaleStrategyTimeOnly),
		WithStaleAfter(2*time.Hour),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	m, err := readMarker(h.Path())
	if err != nil {
		t.Fatal(err)
	}
	if m.strategy != "time-only" {
		t.Errorf("strategy: got %q, want %q", m.strategy, "time-only")
	}
	if m.staleAfter != "2h0m0s" {
		t.Errorf("staleAfter: got %q, want %q", m.staleAfter, "2h0m0s")
	}
}

// Per-call options override factory defaults for stale strategy.
func TestPerCallStrategyOverridesFactory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	defer setNow(now)()

	writeMarkerForTest(t, dir, "ov", marker{
		pid:      os.Getpid(),
		pidStart: processStartTime(os.Getpid()),
		host:     hostname(),
		acquired: now.Add(-2 * time.Hour),
	})

	// Factory says PIDFirst (default) — would say ErrLocked (own PID alive).
	// Per-call override to TimeOnly with expired window → take over.
	locks := NewFactory(WithDir(dir))
	h, err := locks.Acquire(context.Background(), "ov",
		WithStaleStrategy(StaleStrategyTimeOnly),
		WithStaleAfter(time.Hour),
	)
	if err != nil {
		t.Fatalf("per-call override Acquire: %v", err)
	}
	_ = h.Release()
}
