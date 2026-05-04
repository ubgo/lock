package filelock_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubgo/lock/filelock"
)

// fakeMetrics is a hand-rolled MetricsRecorder for assertions. Captures
// every call so tests can verify the right events fired.
type fakeMetrics struct {
	mu        sync.Mutex
	acquires  []acquireEvent
	holds     []holdEvent
	activeSum int
}

type acquireEvent struct {
	name, outcome string
	dur           time.Duration
}

type holdEvent struct {
	name string
	dur  time.Duration
}

func (m *fakeMetrics) AcquireAttempt(name, outcome string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquires = append(m.acquires, acquireEvent{name, outcome, d})
}

func (m *fakeMetrics) HoldDuration(name string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds = append(m.holds, holdEvent{name, d})
}

func (m *fakeMetrics) ActiveLocksDelta(_ string, delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeSum += delta
}

func TestMetricsAcquireSuccess(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMetrics{}
	locks := filelock.NewFactory(filelock.WithDir(dir), filelock.WithMetrics(m))

	h, err := locks.Acquire(context.Background(), "ok")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}

	if len(m.acquires) != 1 {
		t.Fatalf("expected 1 acquire event, got %d", len(m.acquires))
	}
	if m.acquires[0].outcome != filelock.OutcomeAcquired {
		t.Errorf("outcome = %q, want %q", m.acquires[0].outcome, filelock.OutcomeAcquired)
	}
	if len(m.holds) != 1 {
		t.Fatalf("expected 1 hold event, got %d", len(m.holds))
	}
	if m.activeSum != 0 {
		t.Errorf("activeSum after balanced acquire+release = %d, want 0", m.activeSum)
	}
}

func TestMetricsAcquireErrLocked(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMetrics{}
	locks := filelock.NewFactory(filelock.WithDir(dir), filelock.WithMetrics(m))

	h, _ := locks.Acquire(context.Background(), "busy")
	defer func() { _ = h.Release() }()

	if _, err := locks.Acquire(context.Background(), "busy"); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}

	// Two acquire events: first acquired, second errlocked.
	if len(m.acquires) != 2 {
		t.Fatalf("expected 2 acquire events, got %d", len(m.acquires))
	}
	if m.acquires[1].outcome != filelock.OutcomeErrLocked {
		t.Errorf("second outcome = %q, want %q", m.acquires[1].outcome, filelock.OutcomeErrLocked)
	}
}

func TestSpanStarterCalled(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	starter := func(ctx context.Context, op string) (context.Context, func(error)) {
		ops = append(ops, op)
		return ctx, func(error) {}
	}

	locks := filelock.NewFactory(filelock.WithDir(dir), filelock.WithSpanStarter(starter))
	h, _ := locks.Acquire(context.Background(), "trace")
	_ = h.Release()

	if len(ops) != 1 || ops[0] != "filelock.Acquire" {
		t.Fatalf("expected one Acquire span, got %v", ops)
	}
}

func TestLoggerEmitsAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	locks := filelock.NewFactory(filelock.WithDir(dir), filelock.WithLogger(logger))
	h, _ := locks.Acquire(context.Background(), "logged")
	_ = h.Release()

	out := buf.String()
	if !strings.Contains(out, `msg="filelock acquire"`) {
		t.Errorf("missing acquire log line:\n%s", out)
	}
	if !strings.Contains(out, `msg="filelock release"`) {
		t.Errorf("missing release log line:\n%s", out)
	}
}

func TestTraceIDExtractorWritesToMarker(t *testing.T) {
	dir := t.TempDir()
	const want = "trace-abc-123"
	extractor := func(_ context.Context) string { return want }

	locks := filelock.NewFactory(filelock.WithDir(dir),
		filelock.WithTraceIDExtractor(extractor),
	)
	h, err := locks.Acquire(context.Background(), "tid")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	// Read the marker file directly to confirm trace_id is recorded.
	data := readFile(t, h.Path())
	if !strings.Contains(data, "trace_id="+want) {
		t.Fatalf("marker missing trace_id=%s; full body:\n%s", want, data)
	}
}

func TestNilLoggerAndMetricsAreSafe(t *testing.T) {
	dir := t.TempDir()
	locks := filelock.NewFactory(filelock.WithDir(dir),
		filelock.WithLogger(nil),
		filelock.WithMetrics(nil),
	)
	h, err := locks.Acquire(context.Background(), "nilobs")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
