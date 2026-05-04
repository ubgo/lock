# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.1.0] - 2026-05-04

### Added

- Initial release.
- Kernel-fenced advisory lock via `flock(2)` (Unix) / `LockFileEx`
  (Windows). Crash recovery is automatic: the kernel releases the
  lock on process exit (clean or otherwise).
- `Factory` and `Lock` types with the same shape as
  `github.com/ubgo/lock/filelock`. Per-call options override factory
  defaults; same `WithLock(ctx, name, fn, opts...)` helper.
- `AsLocker()` adapter to the shared
  `github.com/ubgo/lock.Locker` interface — write backend-agnostic
  code that swaps between flock, filelock, redislock, etc.
- Cross-platform: tested on linux, darwin, windows.
- Stdlib + `github.com/ubgo/lock` only — no other deps.

[Unreleased]: https://github.com/ubgo/lock/flock/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/ubgo/lock/flock/releases/tag/v0.1.0
