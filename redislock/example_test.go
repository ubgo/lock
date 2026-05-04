package redislock_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ubgo/lock/redislock"
)

// Example shows the Acquire / Release flow against a Redis
// backend. Real code uses your production Redis client; this
// example uses miniredis for a self-contained runnable demo.
func Example() {
	mr, _ := miniredis.Run()
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	locks := redislock.NewFactory(rdb, redislock.WithTTL(2*time.Minute))

	holder, err := locks.Acquire(context.Background(), "midnight-billing")
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = holder.Release() }()

	// Second caller — Redis SET NX rejects.
	if _, err := locks.Acquire(context.Background(), "midnight-billing"); errors.Is(err, redislock.ErrLocked) {
		fmt.Println("another replica owns it")
	}

	// Output:
	// another replica owns it
}

// ExampleHolder_Extend shows how a long-running holder renews
// its lease — keeps a tight TTL for fast crash recovery while
// allowing the work to legitimately run for hours.
func ExampleHolder_Extend() {
	mr, _ := miniredis.Run()
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	locks := redislock.NewFactory(rdb, redislock.WithTTL(time.Minute))
	ctx := context.Background()

	h, _ := locks.Acquire(ctx, "long-export")
	defer func() { _ = h.Release() }()

	// Halfway through the TTL, advance miniredis' clock and renew.
	mr.FastForward(30 * time.Second)
	if err := h.Extend(ctx); err != nil {
		fmt.Println("extend:", err)
		return
	}
	fmt.Println("lease extended")

	// Output:
	// lease extended
}

// ExampleWithMaxConcurrent shows semaphore mode using N keys.
func ExampleWithMaxConcurrent() {
	mr, _ := miniredis.Run()
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	locks := redislock.NewFactory(rdb)
	ctx := context.Background()

	h0, _ := locks.Acquire(ctx, "indexer", redislock.WithMaxConcurrent(2))
	h1, _ := locks.Acquire(ctx, "indexer", redislock.WithMaxConcurrent(2))
	if _, err := locks.Acquire(ctx, "indexer", redislock.WithMaxConcurrent(2)); errors.Is(err, redislock.ErrLocked) {
		fmt.Println("3rd caller skipped")
	}
	_ = h0.Release()
	_ = h1.Release()

	// Output:
	// 3rd caller skipped
}
