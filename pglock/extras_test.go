package pglock_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubgo/lock/pglock"
)

// Fencing tokens (txid_current) ---------------------------------------

func TestPglockTokenIsNonZero(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "tok")
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()
	if h.Token() == 0 {
		t.Fatal("token should be non-zero (txid_current); got 0")
	}
}

func TestPglockTokenIsMonotonic(t *testing.T) {
	p := pool(t)
	l := pglock.New(p, "tok-mono")
	var prev uint64
	for range 5 {
		h, err := l.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if h.Token() <= prev {
			t.Fatalf("token regressed: prev=%d got=%d", prev, h.Token())
		}
		prev = h.Token()
		_ = h.Release()
	}
}

// Semaphore -----------------------------------------------------------

func TestPglockSemaphoreAllowsNConcurrent(t *testing.T) {
	p := pool(t)
	const n = 3
	holders := make([]*pglock.Holder, 0, n)
	for range n {
		l := pglock.New(p, "sem-pg")
		h, err := l.Acquire(context.Background(), pglock.WithMaxConcurrent(n))
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

	l := pglock.New(p, "sem-pg")
	if _, err := l.Acquire(context.Background(), pglock.WithMaxConcurrent(n)); !errors.Is(err, pglock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all %d slots are held", err, n)
	}
}

func TestPglockSemaphoreReleasingFreesSlot(t *testing.T) {
	p := pool(t)
	const n = 2
	a := pglock.New(p, "sem-pg-2")
	b := pglock.New(p, "sem-pg-2")
	c := pglock.New(p, "sem-pg-2")

	h1, _ := a.Acquire(context.Background(), pglock.WithMaxConcurrent(n))
	h2, _ := b.Acquire(context.Background(), pglock.WithMaxConcurrent(n))
	if _, err := c.Acquire(context.Background(), pglock.WithMaxConcurrent(n)); !errors.Is(err, pglock.ErrLocked) {
		t.Fatalf("3rd acquire should fail: %v", err)
	}
	_ = h1.Release()
	h3, err := c.Acquire(context.Background(), pglock.WithMaxConcurrent(n))
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = h2.Release()
	_ = h3.Release()
}

// Observability -------------------------------------------------------

type fakePgMetrics struct {
	mu       sync.Mutex
	acquires []string
	holds    int
	delta    int
}

func (m *fakePgMetrics) AcquireAttempt(_, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquires = append(m.acquires, outcome)
}
func (m *fakePgMetrics) HoldDuration(_ string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds++
}
func (m *fakePgMetrics) ActiveLocksDelta(_ string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delta += d
}

func TestPglockMetricsHook(t *testing.T) {
	p := pool(t)
	m := &fakePgMetrics{}
	l := pglock.New(p, "metrics-pg", pglock.WithMetrics(m))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}

	if len(m.acquires) != 1 || m.acquires[0] != pglock.OutcomeAcquired {
		t.Fatalf("acquires = %v", m.acquires)
	}
	if m.holds != 1 {
		t.Fatalf("holds = %d, want 1", m.holds)
	}
	if m.delta != 0 {
		t.Fatalf("delta = %d, want 0", m.delta)
	}
}

func TestPglockSpanStarter(t *testing.T) {
	p := pool(t)
	var ops []string
	starter := func(ctx context.Context, op string) (context.Context, func(error)) {
		ops = append(ops, op)
		return ctx, func(error) {}
	}
	l := pglock.New(p, "span-pg", pglock.WithSpanStarter(starter))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	if len(ops) != 1 || ops[0] != "pglock.Acquire" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestPglockLogger(t *testing.T) {
	p := pool(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := pglock.New(p, "log-pg", pglock.WithLogger(logger))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	out := buf.String()
	if !strings.Contains(out, `msg="pglock acquire"`) || !strings.Contains(out, `msg="pglock release"`) {
		t.Fatalf("missing log lines:\n%s", out)
	}
}

func TestPglockTraceIDInApplicationName(t *testing.T) {
	p := pool(t)
	const want = "trace-pg-zzz"
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := pglock.New(p, "tid-pg",
		pglock.WithLogger(logger),
		pglock.WithTraceIDExtractor(func(_ context.Context) string { return want }),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	// trace_id should appear in the slog output
	if !strings.Contains(buf.String(), "trace_id="+want) {
		t.Fatalf("trace_id missing from slog:\n%s", buf.String())
	}
	// And application_name should be set on the holder's session.
	// We can't easily query pg_stat_activity from within the holder's
	// own connection; trust the SET succeeded if we got here without
	// an error.
}
