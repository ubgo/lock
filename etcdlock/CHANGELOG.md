# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [v0.1.0] - 2026-05-04

### Added

- Initial release.
- etcd-backed distributed advisory lock built on
  `go.etcd.io/etcd/client/v3/concurrency.Mutex`. Each Acquire creates
  a new etcd Session (lease + auto-keepalive); `TryLock` is used so
  Acquire is non-blocking — matches the family contract.
- Lease-based crash safety: if the holder dies, the lease expires
  after the configured TTL and etcd deletes the key automatically.
- Globally-monotonic fencing tokens via etcd's `mod_revision` — a
  stronger guarantee than per-name counters, since mod_revision is
  ordered across the whole cluster.
- Same Factory + Holder + AsLocker shape as the rest of the
  ubgo/lock-* family.
- Tests gate on `ETCDLOCK_TEST_ENDPOINTS`; CI runs against an
  etcd:v3.5 service container.

[Unreleased]: https://github.com/ubgo/lock/etcdlock/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/ubgo/lock/etcdlock/releases/tag/v0.1.0
