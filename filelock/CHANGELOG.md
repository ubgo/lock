# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.2.1] - 2026-05-04

### Changed

- **Moved `gocronlock` to a separate Go module** at
  `github.com/ubgo/lock/contrib/gocronlock`. The core
  `github.com/ubgo/lock/filelock` `go.mod` no longer requires
  `github.com/go-co-op/gocron/v2`, `github.com/google/uuid`,
  `github.com/jonboulle/clockwork`, or `github.com/robfig/cron/v3`.
  Users that need the gocron adapter add it explicitly with
  `go get github.com/ubgo/lock/contrib/gocronlock`.

### Migration

The import path moved:

```diff
- import "github.com/ubgo/lock/filelock/gocronlock"
+ import "github.com/ubgo/lock/contrib/gocronlock"
```

The package API (`gocronlock.New`) is unchanged.

## [v0.2.0] - 2026-05-02

### Added

- **`Factory` pattern.** `filelock.NewFactory(opts...)` carries shared
  config (typically `WithDir`) so the 28-cron-singletons-share-one-tempdir
  case collapses to one line per call site.
- **`Holder` type.** `Acquire(ctx)` now returns a `*Holder` instead of
  the calling `*Lock` mutating in place. Idempotent `Release` makes
  `defer holder.Release()` safe alongside explicit cleanup.
- **`WithLock(ctx, name, fn, opts...)` helper** on both `Factory` and
  the top-level package — collapses `Acquire → defer Release → fn()`
  into one call. Releases on panic.
- **Context support.** Every Acquire takes `context.Context`; cancelled
  contexts return `ctx.Err()` without touching the filesystem.
- **Marker file format** with identity (`pid`, `pid_start`, `host`,
  `acquired`) and debug (`strategy`, `stale_after`, `max_concurrent`,
  `slot`, `trace_id`) fields. Operators reading `cat <marker>.lock`
  see who held the lock and what they thought their config was.
  Identity fields are read by `Acquire`; debug fields are display-only.
- **PID liveness probe.** Linux + Darwin via `kill(2)` signal 0;
  Windows via `OpenProcess`. Linux additionally compares process start
  time (`/proc/<pid>/stat`) to detect PID reuse.
- **`StaleStrategy` enum** (`PIDFirst` / `PIDOnly` / `TimeOnly`)
  controlling how Acquire decides whether an existing marker is held
  or stale. `PIDFirst` is the default; falls back to the time window
  on inconclusive probes (cross-host, cross-UID).
- **`WithStaleAfter(d)`** time-based stale-takeover window. Per-call
  override of the factory default.
- **Semaphore mode.** `WithMaxConcurrent(n)` allows up to N parallel
  holders for the same lock name. Layout:
  `<dir>/<name>.<slot>.lock` for `n>1`; singleton mode keeps the v0.1
  `<dir>/<name>.lock` path for backwards compatibility of the
  on-disk artifact.
- **`Factory.Sweep(ctx)`** periodic cleanup of crashed-process
  markers. Returns count reclaimed; race-tolerant via re-read before
  remove.
- **Fencing tokens** via `Holder.Token()`. Per-name monotonic counter
  in `<dir>/<name>.fence` sidecar; usable downstream to reject
  writes from stale lock holders. Strict monotonicity in singleton
  mode; best-effort in semaphore mode (documented).
- **Observability hooks** via four interface-typed options:
  - `WithMetrics(MetricsRecorder)` — Prometheus / OTel meters / statsd.
  - `WithSpanStarter(SpanStarter)` — tracing.
  - `WithLogger(*slog.Logger)` — stdlib structured logging.
  - `WithTraceIDExtractor(...)` — writes the active OTel TraceID into
    the marker's `trace_id` debug field so operators can jump from a
    stale marker to the originating trace.
- **`Factory.AsLocker()` / `Lock.AsLocker()`** adapters returning
  the shared `github.com/ubgo/lock.Locker` interface for
  backend-agnostic call sites.
- **`memlock` subpackage** — in-memory factory with the same shape,
  drop-in for unit tests. No filesystem, no PID, no marker —
  millisecond-fast and zero flakiness.
- **`gocronlock` subpackage** — adapter to
  `github.com/go-co-op/gocron/v2.Locker`. Drop into
  `gocron.WithDistributedLocker(...)` for instant cron-singleton
  mode without per-job boilerplate.

### Changed

- **`Acquire(ctx)` returns `*Holder`** instead of mutating the
  receiver. Callers update `holder, err := l.Acquire(ctx)` →
  `defer holder.Release()`.
- **`Release` moved from `*Lock` to `*Holder`.** A Lock is config; a
  Holder is the live acquired state.
- **Default `StaleStrategy` is `PIDFirst`** with no time window. v0.1
  callers see no behaviour change unless they configure stale takeover.

### Removed

- **`Lock.IsLocked()`** — load-bearing on the broken
  `IsLocked → Acquire` TOCTOU pattern. Migration: just call
  `Acquire`; check `errors.Is(err, ErrLocked)`. See MISSION §2.1.

### Fixed

- Crashed-process markers are now reclaimable. v0.1 left them
  forever; v0.2's PID probe + stale window + Sweep cover every
  realistic recovery path.

## [v0.1.0] - 2026-05-01

### Added

- Initial release.
- Marker-file advisory lock with atomic `O_CREATE|O_EXCL` acquisition.
- Default directory is `os.TempDir()`; override with `WithDir`.
- `Acquire`, `Release`, `IsLocked`, `Path`.
- Stdlib-only — zero third-party dependencies.

[Unreleased]: https://github.com/ubgo/lock/filelock/compare/v0.2.1...HEAD
[v0.2.1]: https://github.com/ubgo/lock/filelock/releases/tag/v0.2.1
[v0.2.0]: https://github.com/ubgo/lock/filelock/releases/tag/v0.2.0
[v0.1.0]: https://github.com/ubgo/lock/filelock/releases/tag/v0.1.0
