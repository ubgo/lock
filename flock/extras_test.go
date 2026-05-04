package flock_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubgo/lock"
	"github.com/ubgo/lock/flock"
)

// Fencing -------------------------------------------------------------

func TestTokenStartsAtOneFlock(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("first", flock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
	if got := h.Token(); got != 1 {
		t.Fatalf("first acquire token = %d, want 1", got)
	}
}

func TestTokenIncrementsAcrossAcquiresFlock(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("seq", flock.WithDir(dir))

	tokens := make([]uint64, 5)
	for i := range 5 {
		h, err := l.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		tokens[i] = h.Token()
		if err := h.Release(); err != nil {
			t.Fatalf("Release %d: %v", i, err)
		}
	}
	for i := 1; i < len(tokens); i++ {
		if tokens[i] <= tokens[i-1] {
			t.Fatalf("token sequence not monotonic: %v", tokens)
		}
	}
}

func TestTokenSidecarFlock(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("side", flock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	if _, err := os.Stat(filepath.Join(dir, "side.fence")); err != nil {
		t.Fatalf("fence sidecar missing: %v", err)
	}
}

// Semaphore -----------------------------------------------------------

func TestFlockSemaphoreSingletonLayoutUnchanged(t *testing.T) {
	dir := t.TempDir()
	l := flock.New("singleton", flock.WithDir(dir))
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	if _, err := os.Stat(filepath.Join(dir, "singleton.lock")); err != nil {
		t.Fatalf("expected singleton.lock at v0.1 path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "singleton.0.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("singleton mode should not create .0 file, got err=%v", err)
	}
}

func TestFlockSemaphoreAllowsNConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 3
	holders := make([]*flock.Holder, 0, n)
	for range n {
		l := flock.New("sem", flock.WithDir(dir))
		h, err := l.Acquire(context.Background(), flock.WithMaxConcurrent(n))
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		holders = append(holders, h)
	}
	defer func() {
		for _, h := range holders {
			_ = h.Release()
		}
	}()

	// (n+1)th acquire must fail.
	l := flock.New("sem", flock.WithDir(dir))
	if _, err := l.Acquire(context.Background(), flock.WithMaxConcurrent(n)); !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all %d slots are held", err, n)
	}
}

func TestFlockSemaphoreReleasingFreesSlot(t *testing.T) {
	dir := t.TempDir()
	const n = 2
	a := flock.New("sem2", flock.WithDir(dir))
	b := flock.New("sem2", flock.WithDir(dir))
	c := flock.New("sem2", flock.WithDir(dir))

	h1, _ := a.Acquire(context.Background(), flock.WithMaxConcurrent(n))
	h2, _ := b.Acquire(context.Background(), flock.WithMaxConcurrent(n))
	if _, err := c.Acquire(context.Background(), flock.WithMaxConcurrent(n)); !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("3rd acquire should fail: got %v", err)
	}

	_ = h1.Release()
	h3, err := c.Acquire(context.Background(), flock.WithMaxConcurrent(n))
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = h2.Release()
	_ = h3.Release()
}

func TestFlockSemaphoreBoundedByN(t *testing.T) {
	dir := t.TempDir()
	const n = 4
	const goroutines = 24

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var winners atomic.Int32
	hCh := make(chan *flock.Holder, goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			l := flock.New("race", flock.WithDir(dir))
			h, err := l.Acquire(context.Background(), flock.WithMaxConcurrent(n))
			if err == nil {
				winners.Add(1)
				hCh <- h
			}
		}()
	}
	wg.Wait()
	close(hCh)
	if got := winners.Load(); got != n {
		t.Fatalf("expected exactly %d winners, got %d", n, got)
	}
	for h := range hCh {
		_ = h.Release()
	}
}

// Observability -------------------------------------------------------

type fakeMetrics struct {
	mu       sync.Mutex
	acquires []string // outcomes
	holds    int
	delta    int
}

func (m *fakeMetrics) AcquireAttempt(_, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquires = append(m.acquires, outcome)
}
func (m *fakeMetrics) HoldDuration(_ string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds++
}
func (m *fakeMetrics) ActiveLocksDelta(_ string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delta += d
}

func TestFlockMetricsHook(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMetrics{}
	l := flock.New("metrics", flock.WithDir(dir), flock.WithMetrics(m))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}

	if len(m.acquires) != 1 || m.acquires[0] != flock.OutcomeAcquired {
		t.Fatalf("acquires = %v", m.acquires)
	}
	if m.holds != 1 {
		t.Fatalf("holds = %d, want 1", m.holds)
	}
	if m.delta != 0 {
		t.Fatalf("delta = %d, want 0 (balanced acquire+release)", m.delta)
	}
}

func TestFlockMetricsErrLocked(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMetrics{}
	a := flock.New("busy", flock.WithDir(dir), flock.WithMetrics(m))
	b := flock.New("busy", flock.WithDir(dir), flock.WithMetrics(m))

	h, _ := a.Acquire(context.Background())
	defer func() { _ = h.Release() }()

	if _, err := b.Acquire(context.Background()); !errors.Is(err, flock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if len(m.acquires) != 2 || m.acquires[1] != flock.OutcomeErrLocked {
		t.Fatalf("acquires = %v", m.acquires)
	}
}

func TestFlockSpanStarter(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	starter := func(ctx context.Context, op string) (context.Context, func(error)) {
		ops = append(ops, op)
		return ctx, func(error) {}
	}
	l := flock.New("trace", flock.WithDir(dir), flock.WithSpanStarter(starter))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	if len(ops) != 1 || ops[0] != "flock.Acquire" {
		t.Fatalf("got %v, want one Acquire span", ops)
	}
}

func TestFlockLogger(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := flock.New("log", flock.WithDir(dir), flock.WithLogger(logger))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	out := buf.String()
	if !strings.Contains(out, `msg="flock acquire"`) || !strings.Contains(out, `msg="flock release"`) {
		t.Fatalf("missing log lines:\n%s", out)
	}
}

func TestFlockTraceIDExtractor(t *testing.T) {
	dir := t.TempDir()
	const want = "trace-xyz"
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := flock.New("tid", flock.WithDir(dir),
		flock.WithLogger(logger),
		flock.WithTraceIDExtractor(func(_ context.Context) string { return want }),
	)
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	if !strings.Contains(buf.String(), "trace_id="+want) {
		t.Fatalf("trace_id missing in log:\n%s", buf.String())
	}
}

// Sanity — flock.MetricsRecorder is an alias of lock.MetricsRecorder
// so a value typed as one is assignable to the other without conversion.
func acceptFlockMetrics(_ flock.MetricsRecorder) {}

func TestFlockTypesAreAliasedFromRoot(_ *testing.T) {
	var lr lock.MetricsRecorder = &fakeMetrics{}
	acceptFlockMetrics(lr) // assignability checked at compile time
}
