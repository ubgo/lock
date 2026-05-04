# flock — guide

> Kernel-fenced advisory lock for cooperating processes on the same
> machine. The kernel releases the lock the instant your process
> exits — no PID probe, no stale window, no Sweep, no operator pages
> at 3am.

## When to use flock

**Pick flock when:**

- You're locking on a single host.
- Crash safety matters and you want it kernel-guaranteed without
  any markers, TTLs, or Sweeps to manage.
- You want the smallest API surface in the family — fewer features
  to learn and configure.
- You don't need operator-readable markers, semaphore mode, or
  fencing tokens.

**Don't pick flock when:**

- Multiple hosts need coordination — flock on NFS is unreliable.
- You want to inspect a `.lock` file and see who holds it (use
  `filelock` instead — its markers carry full identity info).
- You need semaphore mode (N parallel holders) — use `filelock`.
- You need fencing tokens — use `filelock`, `redislock`, or
  `etcdlock`.

## Quickstart

```go
import (
    "context"
    "errors"
    "log"

    "github.com/ubgo/lock/flock"
)

func nightlyImport(ctx context.Context) error {
    fl := flock.New("nightly-import", flock.WithDir("/var/run/myservice"))

    holder, err := fl.Acquire(ctx)
    if errors.Is(err, flock.ErrLocked) {
        log.Println("another run is in progress; skipping")
        return nil
    }
    if err != nil { return err }
    defer holder.Release()

    return runImport(ctx)
}
```

## How it works

`flock` is a thin wrapper over the OS's native advisory locking
primitive:

- **Linux / macOS / BSD** — `flock(fd, LOCK_EX|LOCK_NB)`.
- **Windows** — `LockFileEx(handle, LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY)`.

The kernel records the lock against the file descriptor / handle.
**The instant the descriptor closes** (clean Release, process
exit, OOM kill, panic, kernel panic, power loss), the kernel
releases the lock. The next Acquire from any process sees a free
lock immediately.

This is the killer feature — no marker file body to parse, no PID
probe, no TTL, no Sweep, no operator pages when a cron crashes
mid-run.

### Implementation detail: per-fd vs per-process

- **Linux** — flock(2) is per-fd. Two open descriptors in the same
  process can independently lock the same file (rare, but valid).
- **BSD / macOS** — flock(2) is per-process; two fds in the same
  process see each other.
- **Windows** — LockFileEx with EXCLUSIVE_LOCK is per-handle, like
  Linux.

We don't paper over the difference. If you're locking from
multiple goroutines in the same process, use `sync.Mutex` for
that — `flock` is for cross-process coordination.

## API reference

```go
// Construction
fl := flock.New("name", flock.WithDir("/var/run"))
locks := flock.NewFactory(flock.WithDir("/var/run"))

// Acquire
holder, err := fl.Acquire(ctx)
holder, err := locks.Acquire(ctx, "name")
defer holder.Release()

// WithLock
err := locks.WithLock(ctx, "name", func(ctx context.Context) error {
    return doWork(ctx)
})

// Backend-agnostic
var l lock.Locker = locks.AsLocker()
```

### Options

| Option | Default | Purpose |
|---|---|---|
| `WithDir(dir string)` | `os.TempDir()` | Where lock files live. |

That's it. Compared to filelock's ~10 options, flock has 1. The
kernel handles everything else.

## Use cases

### 1. Cron-singleton — minimal API

```go
fl := flock.New("nightly-report", flock.WithDir("/var/run"))
holder, err := fl.Acquire(ctx)
if errors.Is(err, flock.ErrLocked) {
    return nil
}
defer holder.Release()
return generateReport(ctx)
```

Five lines including imports. No staleness window to tune.

### 2. Backend-agnostic — accept lock.Locker

```go
import "github.com/ubgo/lock"

type Service struct {
    locks lock.Locker
}

// Wire flock at startup:
//   svc := &Service{locks: flock.NewFactory(flock.WithDir(...)).AsLocker()}
//
// Wire memlock for tests:
//   svc := &Service{locks: memlock.NewFactory().AsLocker()}
```

### 3. Verify the kernel really releases (smoke test)

```sh
# Terminal 1:
$ go run ./cmd/lock-and-sleep    # acquires "/tmp/test.lock", sleeps
[pid 12345] holding the lock

# Terminal 2:
$ kill -9 12345
$ go run ./cmd/lock-and-sleep    # tries to acquire same name
[pid 12346] holding the lock     # succeeded — kernel released
```

With a marker-file lock you'd see ErrLocked until Sweep / takeover.
With flock the kernel cleans up automatically.

## Operational notes

### Inspecting a held lock

There's no marker body — just an empty file. To find who holds it:

```sh
$ lsof /var/run/job.lock      # Linux/macOS
COMMAND  PID    USER   FD   TYPE NAME
mybinary 12345  root   3uW  REG  /var/run/job.lock
```

The `W` in FD type means "exclusive lock held."

### Lock file lingers after Release

Holder.Release does:

1. Close the file descriptor (kernel releases the lock).
2. Try `os.Remove` the file (cosmetic — the lock is already gone).

If between steps 1 and 2 your process is killed, the file may
linger. Cosmetic only — next Acquire creates / opens it fresh.

### NFS — don't

`flock(2)` on Linux NFS works in newer kernels (4.0+), but not
universally. macOS NFS, Windows network shares, and older Linux
NFSv2/v3 may silently fall back to local-only locking — defeating
the cross-host coordination point. We don't recommend `flock` on
network filesystems. Use `redislock`, `pglock`, or `etcdlock`.

## Comparison vs filelock

| Concern | flock | filelock |
|---|---|---|
| Mechanism | `flock(2)` / `LockFileEx` (kernel) | `O_EXCL` marker (userspace) |
| Crash recovery | Kernel — instant | PID probe + Sweep |
| Stale-marker risk | None | Visible until reclaimed |
| Operator visibility | empty file | Rich PID/host/strategy/etc. |
| Semaphore | Planned | ✅ |
| Fencing tokens | Planned | ✅ |
| API surface | Minimal | Rich |

**Pick flock for** zero-fuss single-host crash safety. **Pick
filelock for** operator-readable markers, semaphore mode, fencing.

## Flaws

See [`docs/flaws.md` §flock](../flaws.md#flock) for the full
list. Highlights:

- **NFS support is platform-dependent** — don't trust it.
- **Per-fd vs per-process semantics differ across Unix flavours.**
- **Holder.Release races the kernel cleanup** for the file
  removal (cosmetic only).
- **No fencing, no semaphore, no metadata** — by design; use
  filelock if you need any of those.

## Migration

From `gofrs/flock` — see [`docs/migration.md`](../migration.md).
