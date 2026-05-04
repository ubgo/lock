# Changelog

All notable changes to this project will be documented in this file.

This is a **monorepo** with multiple Go modules under one Git repository.
Each module is versioned independently with a path-prefixed tag, e.g.
`filelock/v0.2.1` for `github.com/ubgo/lock/filelock`. The root
module (the `Locker` interface) uses a plain `vX.Y.Z` tag.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
each module also has its own per-module CHANGELOG.

## [Unreleased]

## [v0.1.0] - 2026-05-04

### Added

- Initial release of the `github.com/ubgo/lock` umbrella module.
- The shared `lock.Locker` / `lock.Holder` / `lock.ErrLocked`
  interface — used across every backend in the family.
- Decision tree, comparison matrix, and end-to-end use cases in
  the root README.
- Cross-cutting docs in `docs/`: `comparison.md`,
  `snippets.md`, `non-goals.md`, `migration.md`.

### Sister modules shipped at the same time

| Module | Tag | Mechanism |
|---|---|---|
| `github.com/ubgo/lock/filelock` | `filelock/v0.1.0` | Marker file with PID + stale window |
| `github.com/ubgo/lock/flock` | `flock/v0.1.0` | flock(2) / LockFileEx |
| `github.com/ubgo/lock/redislock` | `redislock/v0.1.0` | Redis SET NX EX + Lua |
| `github.com/ubgo/lock/pglock` | `pglock/v0.1.0` | Postgres advisory |
| `github.com/ubgo/lock/etcdlock` | `etcdlock/v0.1.0` | etcd lease + concurrency.Mutex |
| `github.com/ubgo/lock/memlock` | `memlock/v0.1.0` | In-memory (tests) |
| `github.com/ubgo/lock/contrib/gocronlock` | `contrib/gocronlock/v0.1.0` | gocron v2 adapter |

[Unreleased]: https://github.com/ubgo/lock/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/ubgo/lock/releases/tag/v0.1.0
