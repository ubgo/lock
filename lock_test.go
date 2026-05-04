package lock_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ubgo/lock"
)

// fakeLocker is a tiny in-memory implementation used to validate the
// interface contract. Real backends live under github.com/ubgo/lock/<name>.
type fakeLocker struct {
	mu   sync.Mutex
	held map[string]struct{}
}

func newFakeLocker() *fakeLocker {
	return &fakeLocker{held: make(map[string]struct{})}
}

func (f *fakeLocker) Acquire(_ context.Context, name string) (lock.Holder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.held[name]; ok {
		return nil, lock.ErrLocked
	}
	f.held[name] = struct{}{}
	return &fakeHolder{l: f, name: name}, nil
}

type fakeHolder struct {
	l        *fakeLocker
	name     string
	released bool
}

func (h *fakeHolder) Release() error {
	if h.released {
		return nil
	}
	h.released = true
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	delete(h.l.held, h.name)
	return nil
}

// TestInterfaceContract makes sure the published interfaces are exactly
// what implementations need to satisfy. If anyone changes the surface
// without updating implementations, this test stops compiling.
func TestInterfaceContract(t *testing.T) {
	t.Helper()
	var _ lock.Locker = (*fakeLocker)(nil)
	var _ lock.Holder = (*fakeHolder)(nil)
}

func TestErrLockedReturned(t *testing.T) {
	l := newFakeLocker()
	ctx := context.Background()

	h, err := l.Acquire(ctx, "job")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	_, err = l.Acquire(ctx, "job")
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("second Acquire: got %v, want ErrLocked", err)
	}

	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	h2, err := l.Acquire(ctx, "job")
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	_ = h2.Release()
}

func TestReleaseIdempotent(t *testing.T) {
	l := newFakeLocker()
	h, _ := l.Acquire(context.Background(), "job")

	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("second Release: %v (must be idempotent)", err)
	}
}
