# flock

> Kernel-fenced advisory lock for cooperating processes on the same
> machine. The kernel releases the lock the instant your process exits
> — no PID probe, no stale window, no Sweep, no operator pages at 3am.

```bash
go get github.com/ubgo/lock/flock
```

Part of the [`ubgo/lock-*` family](#sister-modules).

## Quick start

```go
import (
    "context"
    "errors"
    "log"

    "github.com/ubgo/lock/flock"
)

func main() {
    ctx := context.Background()
    fl := flock.New("nightly-import", flock.WithDir("/var/run/myservice"))

    holder, err := fl.Acquire(ctx)
    if errors.Is(err, flock.ErrLocked) {
        log.Println("another run is in progress; skipping")
        return
    }
    if err != nil {
        log.Fatal(err)
    }
    defer holder.Release()

    // ... protected work ...
}
```

For services with many lock names sharing config:

```go
locks := flock.NewFactory(flock.WithDir(cfg.LockDir))

err := locks.WithLock(ctx, "import-orders", func(ctx context.Context) error {
    return importOrders(ctx)
})
if errors.Is(err, flock.ErrLocked) {
    log.Println("import-orders already running; skipping")
    return nil
}
return err
```

## flock vs filelock — which should I use?

Both are single-host advisory locks. The trade-off:

| | `ubgo/flock` | `ubgo/filelock` |
|---|---|---|
| Mechanism | `flock(2)` / `LockFileEx` (kernel) | Sentinel file with `O_EXCL` (userspace) |
| Crash recovery | Kernel — instant on process exit | PID liveness probe + stale window + Sweep |
| Stale-marker risk | None — kernel garbage-collects fds | Recoverable but visible until reclaimed |
| Operator visibility | No marker body | Rich marker file with PID/host/strategy/etc. |
| Cross-host (NFS) | Unreliable (NFS flock has known issues) | Explicit cross-host modes |
| Semaphore (N holders) | Planned | ✅ `WithMaxConcurrent` |
| Fencing tokens | Planned | ✅ `Holder.Token()` |
| Same family API | ✅ `lock.Locker` interface | ✅ `lock.Locker` interface |

**Pick `flock` when** crash safety is paramount and you don't need
operator-readable marker files. The kernel handles all the housekeeping;
your code is shorter.

**Pick `filelock` when** you want operators to inspect a marker and see
who held the lock when, OR you need semaphore mode, OR you want
fencing tokens, OR you're on a shared filesystem and need explicit
cross-host modes.

## How crash safety works

When you `Acquire`, flock opens (or creates) a file at `<dir>/<name>.lock`
and asks the kernel for a non-blocking exclusive advisory lock on the
file descriptor:

- **Linux/macOS/BSD** — `flock(fd, LOCK_EX|LOCK_NB)`
- **Windows** — `LockFileEx(handle, LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY)`

The kernel records the lock against the descriptor / handle. When your
process exits — cleanly via `Release`, or abruptly via `kill -9`,
panic, OOM, power loss — the kernel automatically tears down the
descriptor and releases the lock. The next `Acquire` from any process
sees a free lock instantly.

**No marker file body to parse**, **no PID probe**, **no time window
to tune** — the OS does the bookkeeping for you.

## API at a glance

| Symbol | Purpose |
|---|---|
| `New(name, opts...) *Lock` | Construct a single-name lock. |
| `NewFactory(opts...) *Factory` | Construct a multi-name factory. |
| `Lock.Acquire(ctx, opts...) (*Holder, error)` | Take the lock. |
| `Factory.Acquire(ctx, name, opts...) (*Holder, error)` | Same, factory-scoped. |
| `Factory.WithLock(ctx, name, fn, opts...) error` | Acquire → fn → Release. |
| `WithLock(ctx, fl, fn, opts...) error` | Standalone form for `*Lock`. |
| `Factory.AsLocker() lock.Locker` | Backend-agnostic interface. |
| `Holder.Release() error` | Idempotent. Kernel also releases on process exit. |
| `Holder.Path() string` | Lock file path. |
| `WithDir(dir)` | Default `os.TempDir()`. |

## Examples

### Cron-singleton

The classic "skip if already running" — five lines including imports.

```go
locks := flock.NewFactory(flock.WithDir("/var/run/myservice"))

err := locks.WithLock(ctx, "nightly-report", generateReport)
if errors.Is(err, flock.ErrLocked) {
    return // another worker has it; skip silently
}
return err
```

### Backend-agnostic — accept `lock.Locker`

Production wires a `*flock.Factory`. Tests substitute
`*memlock.Factory` (from `github.com/ubgo/lock/memlock` — usable
across the family). Same code path.

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

func (s *Service) Import(ctx context.Context) error {
    h, err := s.locks.Acquire(ctx, "import")
    if errors.Is(err, lock.ErrLocked) {
        return nil
    }
    if err != nil { return err }
    defer h.Release()
    return doImport(ctx)
}

// Wire in main.go:
//   svc := &Service{locks: flock.NewFactory(flock.WithDir(...)).AsLocker()}
//
// Wire in tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

### Crash test — verify the kernel really releases

A useful smoke test for the killer feature:

```go
// In one terminal:
fl := flock.New("crash-test", flock.WithDir("/tmp"))
h, _ := fl.Acquire(ctx)
fmt.Println("lock held; kill me with -9")
h.Release() // unreachable

// Send SIGKILL from another terminal:
//   kill -9 <pid>
//
// Now retry from a fresh process:
fl := flock.New("crash-test", flock.WithDir("/tmp"))
h, err := fl.Acquire(ctx)
// err == nil — the kernel released the lock when the SIGKILL'd
// process disappeared. With a marker-file lock you'd see ErrLocked
// until the next Sweep / takeover.
```

## Sister modules

| Module | When to pick |
|---|---|
| **`ubgo/flock`** *(this module)* | Single-host. Kernel-fenced crash safety. Minimal API. |
| `ubgo/filelock` | Single-host. Marker file with operator-readable PID/host/strategy. Semaphore + fencing. |
| `ubgo/redislock` *(planned)* | Multi-host. Already running Redis. Tolerate AP semantics. |
| `ubgo/pglock` *(planned)* | Multi-host. Already running Postgres. Free crash safety from session disconnect. |
| `ubgo/etcdlock` *(planned)* | Multi-host. Need strong consistency. Can run an etcd cluster. |
| `ubgo/locker` | Shared `Locker` / `Holder` interface. Import to write backend-agnostic code. |

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
