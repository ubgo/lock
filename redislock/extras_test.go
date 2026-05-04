package redislock_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ubgo/lock/redislock"
)

func startMiniExtras(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// Semaphore -----------------------------------------------------------

func TestRedislockSemaphoreAllowsNConcurrent(t *testing.T) {
	rdb, _ := startMiniExtras(t)
	const n = 3
	holders := make([]*redislock.Holder, 0, n)
	for range n {
		l := redislock.New(rdb, "sem")
		h, err := l.Acquire(context.Background(), redislock.WithMaxConcurrent(n))
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

	l := redislock.New(rdb, "sem")
	if _, err := l.Acquire(context.Background(), redislock.WithMaxConcurrent(n)); !errors.Is(err, redislock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all %d slots are held", err, n)
	}
}

func TestRedislockSemaphoreReleasingFreesSlot(t *testing.T) {
	rdb, _ := startMiniExtras(t)
	const n = 2

	a := redislock.New(rdb, "sem2")
	b := redislock.New(rdb, "sem2")
	c := redislock.New(rdb, "sem2")

	h1, _ := a.Acquire(context.Background(), redislock.WithMaxConcurrent(n))
	h2, _ := b.Acquire(context.Background(), redislock.WithMaxConcurrent(n))

	if _, err := c.Acquire(context.Background(), redislock.WithMaxConcurrent(n)); !errors.Is(err, redislock.ErrLocked) {
		t.Fatalf("3rd acquire should fail: got %v", err)
	}

	_ = h1.Release()
	h3, err := c.Acquire(context.Background(), redislock.WithMaxConcurrent(n))
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = h2.Release()
	_ = h3.Release()
}

func TestRedislockSemaphoreSlotKeysCreated(t *testing.T) {
	rdb, mr := startMiniExtras(t)
	locks := redislock.NewFactory(rdb)

	h0, err := locks.Acquire(context.Background(), "dbg", redislock.WithMaxConcurrent(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h0.Release() }()

	h1, err := locks.Acquire(context.Background(), "dbg", redislock.WithMaxConcurrent(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h1.Release() }()

	if !mr.Exists("redislock:dbg:0") {
		t.Error("expected redislock:dbg:0")
	}
	if !mr.Exists("redislock:dbg:1") {
		t.Error("expected redislock:dbg:1")
	}
}

// Observability -------------------------------------------------------

type fakeRedisMetrics struct {
	mu       sync.Mutex
	acquires []string
	holds    int
	delta    int
}

func (m *fakeRedisMetrics) AcquireAttempt(_, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquires = append(m.acquires, outcome)
}
func (m *fakeRedisMetrics) HoldDuration(_ string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds++
}
func (m *fakeRedisMetrics) ActiveLocksDelta(_ string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delta += d
}

func TestRedislockMetricsHook(t *testing.T) {
	rdb, _ := startMiniExtras(t)
	m := &fakeRedisMetrics{}
	l := redislock.New(rdb, "metrics", redislock.WithMetrics(m))

	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if len(m.acquires) != 1 || m.acquires[0] != redislock.OutcomeAcquired {
		t.Fatalf("acquires = %v", m.acquires)
	}
	if m.holds != 1 {
		t.Fatalf("holds = %d, want 1", m.holds)
	}
	if m.delta != 0 {
		t.Fatalf("delta = %d, want 0", m.delta)
	}
}

func TestRedislockSpanStarter(t *testing.T) {
	rdb, _ := startMiniExtras(t)
	var ops []string
	starter := func(ctx context.Context, op string) (context.Context, func(error)) {
		ops = append(ops, op)
		return ctx, func(error) {}
	}
	l := redislock.New(rdb, "span", redislock.WithSpanStarter(starter))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	if len(ops) != 1 || ops[0] != "redislock.Acquire" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestRedislockLogger(t *testing.T) {
	rdb, _ := startMiniExtras(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := redislock.New(rdb, "log", redislock.WithLogger(logger))
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	out := buf.String()
	if !strings.Contains(out, `msg="redislock acquire"`) || !strings.Contains(out, `msg="redislock release"`) {
		t.Fatalf("missing log lines:\n%s", out)
	}
}

func TestRedislockTraceIDInHolderValue(t *testing.T) {
	rdb, mr := startMiniExtras(t)
	const want = "trace-redis-xyz"
	l := redislock.New(rdb, "tid",
		redislock.WithTraceIDExtractor(func(_ context.Context) string { return want }),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	val, err := mr.Get("redislock:tid")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(val, "|trace:"+want) {
		t.Fatalf("holder value missing trace: %q", val)
	}
}
