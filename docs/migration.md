# Migration guide

Moving from another Go locking library to `ubgo/lock`. The shape
is small enough that migrations are typically 1–2 lines per call
site.

## From `gofrs/flock`

```diff
- import "github.com/gofrs/flock"
+ import "github.com/ubgo/lock/flock"

- fl := flock.New("/var/run/myservice/job.lock")
- if locked, err := fl.TryLock(); err != nil || !locked {
-     return // already running
- }
- defer fl.Unlock()
+ fl := flock.New("job", flock.WithDir("/var/run/myservice"))
+ holder, err := fl.Acquire(ctx)
+ if errors.Is(err, flock.ErrLocked) {
+     return nil
+ }
+ if err != nil {
+     return err
+ }
+ defer holder.Release()
```

Notes:

- `gofrs/flock` takes a full file path; `ubgo/lock/flock` takes a
  name + dir option. The marker file ends up at `<dir>/<name>.lock`.
- `gofrs/flock` returns `(locked, err)` from `TryLock`;
  `ubgo/lock/flock` returns `(*Holder, err)` and uses `ErrLocked`
  as a sentinel. Use `errors.Is` to check.
- Crash safety is the same — kernel-fenced via `flock(2)` /
  `LockFileEx`.

## From `bsm/redislock`

```diff
- import "github.com/bsm/redislock"
+ import "github.com/ubgo/lock/redislock"

- locker := redislock.New(rdb)
- lock, err := locker.Obtain(ctx, "job", time.Minute, nil)
- if err == redislock.ErrNotObtained {
-     return // already held
- }
- defer lock.Release(ctx)
+ locks := redislock.NewFactory(rdb, redislock.WithTTL(time.Minute))
+ holder, err := locks.Acquire(ctx, "job")
+ if errors.Is(err, redislock.ErrLocked) {
+     return nil
+ }
+ if err != nil {
+     return err
+ }
+ defer holder.Release()
```

Notes:

- `bsm/redislock`'s `redislock.New(rdb)` becomes
  `redislock.NewFactory(rdb)` — the rest of the API mirrors.
- `ErrNotObtained` becomes `ErrLocked` (consistent across the
  family).
- Lua-guarded release works the same way under the hood (we
  always check the holder value before deleting).
- Add `WithTTL(...)` to the factory or per-call options.

## From `go-redsync/redsync`

`redsync` implements Redlock (multi-master quorum). `ubgo/lock`
deliberately does NOT implement Redlock — see
[`non-goals.md`](./non-goals.md).

If your existing redsync usage is single-master (one Redis pool),
the migration is straightforward:

```diff
- import (
-     "github.com/go-redsync/redsync/v4"
-     "github.com/go-redsync/redsync/v4/redis/goredis/v9"
- )
+ import "github.com/ubgo/lock/redislock"

- pool := goredis.NewPool(rdb)
- rs := redsync.New(pool)
- mutex := rs.NewMutex("job", redsync.WithExpiry(time.Minute))
- if err := mutex.LockContext(ctx); err != nil { /* ... */ }
- defer mutex.UnlockContext(ctx)
+ locks := redislock.NewFactory(rdb, redislock.WithTTL(time.Minute))
+ holder, err := locks.Acquire(ctx, "job")
+ if err != nil { /* ... */ }
+ defer holder.Release()
```

If you're using redsync with multi-master quorum (multiple
`pool.NewPool` instances), don't migrate — use `ubgo/lock/etcdlock`
instead. etcd's Raft gives you the safety guarantees Redlock
approximates.

## From `lace/filelock` (or any `os.Create`-based marker file)

This is the migration the family was originally designed for.

```diff
- import "github.com/<org>/lace/filelock"
+ import "github.com/ubgo/lock/filelock"

- flock := filelock.New("syncjob").SetDir(cfg.TempDir)
- if flock.IsActive() {
-     fmt.Println("cron already running")
-     return
- }
- flock.Lock()
- defer flock.Release()
+ locks := filelock.NewFactory(filelock.WithDir(cfg.TempDir))
+ err := locks.WithLock(ctx, "syncjob", syncJobFn,
+     filelock.WithStaleAfter(2*time.Hour),
+ )
+ if errors.Is(err, filelock.ErrLocked) {
+     return // another run active; skip
+ }
+ if err != nil { return err }
```

Notes:

- The old `IsActive() → Lock()` pattern had a TOCTOU race that let
  two processes both think they were the singleton. `WithLock`
  collapses to a single atomic call.
- `lace/filelock` left crashed-process markers forever.
  `filelock` here probes the PID + falls back to `WithStaleAfter`
  for cross-host or inconclusive cases.
- For services that lock many names, `Factory(WithDir(...))` lifts
  the shared dir out of every call site.

## From Postgres advisory locks (raw SQL)

```diff
- _, err := db.ExecContext(ctx, "SELECT pg_try_advisory_lock($1)", lockKey)
- // ... business logic ...
- _, err = db.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockKey)
+ import "github.com/ubgo/lock/pglock"
+
+ locks := pglock.NewFactory(pool)
+ err := locks.WithLock(ctx, "lock-name", businessLogic)
+ if errors.Is(err, pglock.ErrLocked) {
+     return nil
+ }
```

Notes:

- The raw SQL approach has a subtle bug: if you don't pull a
  dedicated connection from the pool, the lock and unlock can
  end up on different connections (advisory locks are
  session-tied). `pglock` handles connection pinning for you.
- `pglock` hashes string names into the int64 key for you (FNV-1a),
  so you can name locks by job name instead of allocating an
  integer keyspace manually.
- Use `WithKeyOffset` to namespace your keyspace if other code
  in the same database uses `pg_advisory_lock`.

## From `etcd/clientv3/concurrency.Mutex` directly

```diff
- session, _ := concurrency.NewSession(cli, concurrency.WithTTL(30))
- defer session.Close()
- mutex := concurrency.NewMutex(session, "/locks/job")
- if err := mutex.TryLock(ctx); err == concurrency.ErrLocked {
-     return // skip
- }
- defer mutex.Unlock(ctx)
+ import "github.com/ubgo/lock/etcdlock"
+
+ locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(30*time.Second))
+ err := locks.WithLock(ctx, "job", businessLogic)
+ if errors.Is(err, etcdlock.ErrLocked) { return nil }
```

The wrapper is mostly ergonomics: a Factory pattern, a
`WithLock(ctx, name, fn)` helper, and translation of
`concurrency.ErrLocked` into the family's `etcdlock.ErrLocked`
sentinel. The fencing token (`Holder.Token()` returning
`mod_revision`) is exposed cleanly without you managing the
`ResponseHeader` yourself.

## Decision: should I migrate?

You should migrate when you get one of these:

1. **You need to swap backends.** Your service started single-host
   with `gofrs/flock`; now you're going multi-host and need
   Redis. Other libraries are point solutions; `ubgo/lock` lets
   the same code path use either backend.

2. **You want fencing tokens but your library doesn't expose them.**
   Especially relevant for `bsm/redislock` (no fencing) → `ubgo/lock/redislock`.

3. **You want operator-readable markers.** `lace/filelock` and
   bare `os.Create` markers don't tell you who held the lock when
   ops finds a stale `.lock` file at 3am.

4. **You want consistent test ergonomics.** `memlock` is a drop-in
   that works with any production backend in the family.

You should NOT migrate if:

- Your existing usage is correct, mature, and unlikely to
  change scope. Migration cost > benefit.
- You depend on a feature ubgo/lock deliberately doesn't ship
  (Redlock, reentrancy outside pglock, blocking acquire). See
  [`non-goals.md`](./non-goals.md).
