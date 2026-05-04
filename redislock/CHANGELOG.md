# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [v0.1.0] - 2026-05-04

### Added

- Initial release.
- Redis-backed distributed advisory lock via SET key value NX EX <ttl>.
  Atomic acquisition; TTL handles crash recovery automatically.
- Lua-guarded Release: deletes the key only if the value still
  matches our holder, preventing a delayed Release from blowing away
  a successor's lock.
- `Holder.Extend(ctx)` for long-running jobs that need to renew
  the TTL.
- Fencing tokens via Redis `INCR` on a sibling key — monotonic
  per lock name.
- Same Factory + Holder + AsLocker shape as the rest of the
  ubgo/lock-* family.
- `ErrLockLost` error returned when a Release / Extend finds the
  key no longer holds our value.
- Tests run against
  [miniredis](https://github.com/alicebob/miniredis), so no real
  Redis is needed in CI.

[Unreleased]: https://github.com/ubgo/lock/redislock/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/ubgo/lock/redislock/releases/tag/v0.1.0
