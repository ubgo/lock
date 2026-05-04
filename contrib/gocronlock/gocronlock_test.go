package gocronlock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-co-op/gocron/v2"
	"github.com/ubgo/lock"
	"github.com/ubgo/lock/contrib/gocronlock"
	"github.com/ubgo/lock/filelock"
	"github.com/ubgo/lock/memlock"
)

// asGocronLocker is a tiny helper that takes any gocron.Locker and
// returns it. Used to anchor a compile-time interface assertion
// without tripping staticcheck QF1011 (redundant explicit type on
// var declarations).
func asGocronLocker(l gocron.Locker) gocron.Locker { return l }

func TestNewSatisfiesGocronLocker(t *testing.T) {
	locks := filelock.NewFactory(filelock.WithDir(t.TempDir()))
	if got := asGocronLocker(gocronlock.New(locks.AsLocker())); got == nil {
		t.Fatal("gocronlock.New returned nil")
	}
}

func TestLockUnlock(t *testing.T) {
	locks := filelock.NewFactory(filelock.WithDir(t.TempDir()))
	g := gocronlock.New(locks.AsLocker())

	gl, err := g.Lock(context.Background(), "job")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := gl.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestLockReturnsErrorWhenContended(t *testing.T) {
	locks := filelock.NewFactory(filelock.WithDir(t.TempDir()))
	g := gocronlock.New(locks.AsLocker())

	gl, err := g.Lock(context.Background(), "busy")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = gl.Unlock(context.Background()) }()

	_, err = g.Lock(context.Background(), "busy")
	if err == nil {
		t.Fatal("expected error from second Lock")
	}
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("got %v, want lock.ErrLocked", err)
	}
}

func TestWorksWithMemlock(t *testing.T) {
	// Drop-in test backend — gocronlock is backend-agnostic.
	g := gocronlock.New(memlock.NewFactory().AsLocker())

	gl, err := g.Lock(context.Background(), "mem")
	if err != nil {
		t.Fatal(err)
	}
	if err := gl.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
}
