package filelock

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMarkerRoundTrip(t *testing.T) {
	want := marker{
		pid:           4242,
		pidStart:      time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		host:          "worker-3",
		acquired:      time.Date(2026, 5, 1, 12, 1, 0, 0, time.UTC),
		strategy:      "pid-first",
		staleAfter:    "2h",
		maxConcurrent: 3,
		slot:          1,
		traceID:       "abc123",
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "round.lock")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(f, want); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := readMarker(path)
	if err != nil {
		t.Fatalf("readMarker: %v", err)
	}
	if got.pid != want.pid {
		t.Errorf("pid: got %d, want %d", got.pid, want.pid)
	}
	if !got.pidStart.Equal(want.pidStart) {
		t.Errorf("pidStart: got %v, want %v", got.pidStart, want.pidStart)
	}
	if got.host != want.host {
		t.Errorf("host: got %q, want %q", got.host, want.host)
	}
	if !got.acquired.Equal(want.acquired) {
		t.Errorf("acquired: got %v, want %v", got.acquired, want.acquired)
	}
	if got.strategy != want.strategy {
		t.Errorf("strategy: got %q, want %q", got.strategy, want.strategy)
	}
	if got.staleAfter != want.staleAfter {
		t.Errorf("staleAfter: got %q, want %q", got.staleAfter, want.staleAfter)
	}
	if got.maxConcurrent != want.maxConcurrent {
		t.Errorf("maxConcurrent: got %d, want %d", got.maxConcurrent, want.maxConcurrent)
	}
	if got.slot != want.slot {
		t.Errorf("slot: got %d, want %d", got.slot, want.slot)
	}
	if got.traceID != want.traceID {
		t.Errorf("traceID: got %q, want %q", got.traceID, want.traceID)
	}
}

func TestMarkerWritesIdentityHeader(t *testing.T) {
	var buf bytes.Buffer
	err := writeMarker(&buf, marker{
		pid:      1,
		host:     "h",
		acquired: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "# Identity") {
		t.Fatalf("missing Identity header in marker:\n%s", out)
	}
	if strings.Contains(out, "# Debug") {
		t.Fatalf("Debug header should be omitted when no debug fields set:\n%s", out)
	}
}

func TestMarkerOmitsDebugWhenAllUnset(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMarker(&buf, marker{
		pid:      1,
		host:     "h",
		acquired: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "strategy=") {
		t.Fatalf("strategy should not be emitted when unset:\n%s", buf.String())
	}
}

func TestMarkerEmitsDebugWhenAnyFieldSet(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMarker(&buf, marker{
		pid:      1,
		host:     "h",
		acquired: time.Now(),
		strategy: "pid-first",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "# Debug") {
		t.Fatalf("Debug header should appear:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "strategy=pid-first") {
		t.Fatalf("strategy missing:\n%s", buf.String())
	}
}

func TestReadMarkerIgnoresUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown.lock")
	body := "# header\n" +
		"pid=99\n" +
		"host=h\n" +
		"acquired=2026-05-01T00:00:00Z\n" +
		"future_field=whatever\n" +
		"another=42\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readMarker(path)
	if err != nil {
		t.Fatalf("readMarker: %v", err)
	}
	if m.pid != 99 {
		t.Fatalf("pid: got %d, want 99", m.pid)
	}
}

func TestReadMarkerRejectsMalformedPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.lock")
	if err := os.WriteFile(path, []byte("pid=not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readMarker(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestReadMarkerMissingFile(t *testing.T) {
	_, err := readMarker(filepath.Join(t.TempDir(), "no-such-file.lock"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want os.ErrNotExist", err)
	}
}

func TestAcquireWritesIdentityFields(t *testing.T) {
	dir := t.TempDir()
	l := New("identity", WithDir(dir))
	h, err := l.Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	m, err := readMarker(h.Path())
	if err != nil {
		t.Fatalf("readMarker: %v", err)
	}
	if m.pid != os.Getpid() {
		t.Errorf("pid: got %d, want %d", m.pid, os.Getpid())
	}
	if m.host == "" {
		t.Error("host should be populated")
	}
	if m.acquired.IsZero() {
		t.Error("acquired should be populated")
	}
}

func TestSetNowReplacesClock(t *testing.T) {
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	restore := setNow(frozen)
	defer restore()

	dir := t.TempDir()
	l := New("frozen", WithDir(dir))
	h, err := l.Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release() }()

	m, err := readMarker(h.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !m.acquired.Equal(frozen) {
		t.Fatalf("acquired: got %v, want %v", m.acquired, frozen)
	}
}
