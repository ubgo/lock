package etcdlock_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/ubgo/lock/etcdlock"
)

// Semaphore -----------------------------------------------------------

func TestEtcdlockSemaphoreAllowsNConcurrent(t *testing.T) {
	cli := client(t)
	const n = 3
	holders := make([]*etcdlock.Holder, 0, n)
	for range n {
		l := etcdlock.New(cli, "sem-etcd", etcdlock.WithTTL(10*time.Second))
		h, err := l.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n))
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

	l := etcdlock.New(cli, "sem-etcd", etcdlock.WithTTL(10*time.Second))
	if _, err := l.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n)); !errors.Is(err, etcdlock.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked when all %d slots are held", err, n)
	}
}

func TestEtcdlockSemaphoreReleasingFreesSlot(t *testing.T) {
	cli := client(t)
	const n = 2
	a := etcdlock.New(cli, "sem-etcd-2", etcdlock.WithTTL(10*time.Second))
	b := etcdlock.New(cli, "sem-etcd-2", etcdlock.WithTTL(10*time.Second))
	c := etcdlock.New(cli, "sem-etcd-2", etcdlock.WithTTL(10*time.Second))

	h1, _ := a.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n))
	h2, _ := b.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n))
	if _, err := c.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n)); !errors.Is(err, etcdlock.ErrLocked) {
		t.Fatalf("3rd acquire should fail: %v", err)
	}
	_ = h1.Release()
	h3, err := c.Acquire(context.Background(), etcdlock.WithMaxConcurrent(n))
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = h2.Release()
	_ = h3.Release()
}

// Observability -------------------------------------------------------

type fakeEtcdMetrics struct {
	mu       sync.Mutex
	acquires []string
	holds    int
	delta    int
}

func (m *fakeEtcdMetrics) AcquireAttempt(_, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquires = append(m.acquires, outcome)
}
func (m *fakeEtcdMetrics) HoldDuration(_ string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds++
}
func (m *fakeEtcdMetrics) ActiveLocksDelta(_ string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delta += d
}

func TestEtcdlockMetricsHook(t *testing.T) {
	cli := client(t)
	m := &fakeEtcdMetrics{}
	l := etcdlock.New(cli, "metrics-etcd",
		etcdlock.WithTTL(10*time.Second),
		etcdlock.WithMetrics(m),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if len(m.acquires) != 1 || m.acquires[0] != etcdlock.OutcomeAcquired {
		t.Fatalf("acquires = %v", m.acquires)
	}
	if m.holds != 1 {
		t.Fatalf("holds = %d, want 1", m.holds)
	}
	if m.delta != 0 {
		t.Fatalf("delta = %d, want 0", m.delta)
	}
}

func TestEtcdlockSpanStarter(t *testing.T) {
	cli := client(t)
	var ops []string
	starter := func(ctx context.Context, op string) (context.Context, func(error)) {
		ops = append(ops, op)
		return ctx, func(error) {}
	}
	l := etcdlock.New(cli, "span-etcd",
		etcdlock.WithTTL(10*time.Second),
		etcdlock.WithSpanStarter(starter),
	)
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	if len(ops) != 1 || ops[0] != "etcdlock.Acquire" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestEtcdlockLogger(t *testing.T) {
	cli := client(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	l := etcdlock.New(cli, "log-etcd",
		etcdlock.WithTTL(10*time.Second),
		etcdlock.WithLogger(logger),
	)
	h, _ := l.Acquire(context.Background())
	_ = h.Release()
	out := buf.String()
	if !strings.Contains(out, `msg="etcdlock acquire"`) || !strings.Contains(out, `msg="etcdlock release"`) {
		t.Fatalf("missing log lines:\n%s", out)
	}
}

func TestEtcdlockTraceIDInLockKeyValue(t *testing.T) {
	cli := client(t)
	const want = "trace-etcd-xyz"
	l := etcdlock.New(cli, "tid-etcd",
		etcdlock.WithTTL(10*time.Second),
		etcdlock.WithTraceIDExtractor(func(_ context.Context) string { return want }),
	)
	h, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Release() }()

	resp, err := cli.Get(context.Background(), h.Key())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kvs) == 0 {
		t.Fatal("lock key not found")
	}
	if got := string(resp.Kvs[0].Value); !strings.Contains(got, "trace:"+want) {
		t.Fatalf("lock key value = %q, want trace:%s embedded", got, want)
	}
}

// Compile-time verification we haven't broken the clientv3 import.
var _ = clientv3.Client{}
