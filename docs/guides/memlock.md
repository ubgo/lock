# memlock — guide

> In-memory drop-in for unit tests. Same shape as the production
> backends; zero filesystem, zero network, zero infra.

## When to use memlock

**Pick memlock when:**

- You're writing unit tests for code that depends on
  `lock.Locker` (or the family interface).
- You want millisecond-fast tests with zero filesystem flakiness
  and zero CI infra requirements.

**Don't pick memlock when:**

- You're in production. memlock is per-process; it provides NO
  cross-process mutual exclusion. Even for "single process"
  production cases, use `flock` or `filelock` — they guarantee
  exclusion against any future process that joins the host.

## Quickstart

```go
import (
    "context"
    "errors"
    "testing"

    "github.com/ubgo/lock"
    "github.com/ubgo/lock/memlock"
)

func TestService(t *testing.T) {
    locks := memlock.NewFactory()
    svc := payments.New(locks.AsLocker())

    if err := svc.DailyExport(context.Background()); err != nil {
        t.Fatal(err)
    }
}
```

## How it works

A `*memlock.Factory` carries:

- A `map[string]*lockState` keyed by lock name, guarded by a
  `sync.Mutex`.
- A `map[string]*atomic.Uint64` for fence counters per name.

`Acquire(ctx, name)` does:

1. Check ctx for cancellation.
2. Lock the factory's mutex.
3. Look up `state[name]`; if absent, create.
4. If `state.held >= cfg.maxConcurrent`, return `ErrLocked`.
5. Increment `state.held` and the fence counter.
6. Return a `*Holder`.

`Holder.Release` decrements the count and removes the state when
it reaches zero.

That's it. ~150 LOC total.

## API reference

```go
// Construction
locks := memlock.NewFactory()

// Acquire
holder, err := locks.Acquire(ctx, "name", memlock.WithMaxConcurrent(3))
defer holder.Release()

// WithLock
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

// Holder methods
holder.Release()        // Idempotent
token := holder.Token() // Per-name monotonic uint64

// Backend-agnostic
var l lock.Locker = locks.AsLocker()
```

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithMaxConcurrent(n int)` | 1 | Up to N parallel holders for the same name (semaphore mode). |

## Use cases

### 1. Drop-in test backend

```go
func TestProcessPayments(t *testing.T) {
    locks := memlock.NewFactory()
    svc := &Service{locks: locks.AsLocker()}

    // Production code unchanged — accepts lock.Locker.
    if err := svc.ProcessPayments(ctx); err != nil {
        t.Fatal(err)
    }
}
```

No Redis, no Postgres, no filesystem — pure in-memory. Tests run
in microseconds.

### 2. Test contention scenarios

```go
func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
    locks := memlock.NewFactory()

    // Two acquires; second must fail.
    h1, err := locks.Acquire(ctx, "x")
    if err != nil { t.Fatal(err) }
    defer h1.Release()

    if _, err := locks.Acquire(ctx, "x"); !errors.Is(err, memlock.ErrLocked) {
        t.Fatalf("got %v, want ErrLocked", err)
    }
}
```

### 3. Fast semaphore tests

```go
locks := memlock.NewFactory()
const n = 3

for i := 0; i < n; i++ {
    h, _ := locks.Acquire(ctx, "sem", memlock.WithMaxConcurrent(n))
    defer h.Release()
}
// (n+1)th must fail.
if _, err := locks.Acquire(ctx, "sem", memlock.WithMaxConcurrent(n)); !errors.Is(err, memlock.ErrLocked) {
    t.Fatal("expected ErrLocked")
}
```

## Why this exists

Distributed lock libraries are notoriously hard to test. Real
Redis / Postgres / etcd is overkill for unit tests; mocking the
client is fragile. memlock gives you the **same `lock.Locker`
interface** as production with **zero infrastructure**.

This is identical to the pattern in `database/sql` where
`testing/sqltest` provides an in-memory driver.

## Flaws

See [`docs/flaws.md` §memlock](../flaws.md#memlock):

- **Per-process scope** — by design. Don't deploy in production.
- **No persistence** — state evaporates on process exit.
- **Token sequence resets per Factory** — each `NewFactory()`
  starts at zero. Cross-test isolation by default.
