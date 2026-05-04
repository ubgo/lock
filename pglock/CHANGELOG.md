# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [v0.1.0] - 2026-05-04

### Added

- Initial release.
- Postgres advisory lock via `pg_try_advisory_lock` /
  `pg_advisory_unlock`. Session-tied — Postgres releases the lock
  automatically when the connection closes (process crash, network
  drop, server restart).
- `Holder` owns a dedicated `*pgxpool.Conn`; Release returns it to
  the pool. Crash recovery is automatic with no TTL or Sweep.
- Same Factory + Holder + AsLocker shape as the rest of the
  ubgo/lock-* family.
- `WithKeyOffset` to namespace pglock's keyspace from any other
  pg_advisory_lock users in the same database.
- FNV-1a hashing of string lock names into the int64 key Postgres
  expects.
- Documented the family-wide exception: pglock is reentrant by
  default (Postgres native semantics), unlike the other lock
  modules. See README.

[Unreleased]: https://github.com/ubgo/lock/pglock/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/ubgo/lock/pglock/releases/tag/v0.1.0
