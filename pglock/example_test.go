package pglock_test

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ubgo/lock/pglock"
)

// dsnFromEnv returns the test DSN or empty. Real code uses your
// own pgxpool.Pool directly; this helper just lets the example
// run when PGLOCK_TEST_DSN is set, without breaking pkg.go.dev's
// rendering when it isn't.
func dsnFromEnv() string { return os.Getenv("PGLOCK_TEST_DSN") }

// Example shows the Acquire / Release flow against a Postgres
// backend. The lock is recorded against the connection's session;
// Postgres releases it automatically if the session disconnects
// (clean Release, process crash, network drop, server restart).
//
// Run with PGLOCK_TEST_DSN set to a reachable Postgres instance:
//
//	PGLOCK_TEST_DSN="postgres://user:pass@localhost/db?sslmode=disable" go test
func Example() {
	dsn := dsnFromEnv()
	if dsn == "" {
		return // Documentation-only when DSN unset.
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		fmt.Println("pool:", err)
		return
	}
	defer pool.Close()

	locks := pglock.NewFactory(pool)

	holder, err := locks.Acquire(context.Background(), "nightly-import")
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = holder.Release() }()

	if _, err := locks.Acquire(context.Background(), "nightly-import"); errors.Is(err, pglock.ErrLocked) {
		fmt.Println("already held by another session")
	}
}

// ExampleHolder_Token shows the per-Postgres-instance fencing
// token (`txid_current()`) captured at Acquire. Pass it to
// downstream consumers to reject stale-holder writes.
func ExampleHolder_Token() {
	dsn := dsnFromEnv()
	if dsn == "" {
		return
	}

	pool, _ := pgxpool.New(context.Background(), dsn)
	defer pool.Close()

	locks := pglock.NewFactory(pool)
	h, _ := locks.Acquire(context.Background(), "fence-demo")
	defer func() { _ = h.Release() }()

	if h.Token() > 0 {
		fmt.Println("token assigned")
	}
}

// ExampleWithMaxConcurrent shows semaphore mode — N holders for
// the same name simultaneously. Each slot uses a different
// hashed key (offset by slot index).
func ExampleWithMaxConcurrent() {
	dsn := dsnFromEnv()
	if dsn == "" {
		return
	}

	pool, _ := pgxpool.New(context.Background(), dsn)
	defer pool.Close()

	locks := pglock.NewFactory(pool)
	ctx := context.Background()

	h0, _ := locks.Acquire(ctx, "indexer", pglock.WithMaxConcurrent(2))
	h1, _ := locks.Acquire(ctx, "indexer", pglock.WithMaxConcurrent(2))
	defer func() { _ = h0.Release() }()
	defer func() { _ = h1.Release() }()
}
