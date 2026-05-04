// Package etcdlock is an etcd-backed distributed advisory lock for
// cooperating processes that need strong consistency (Raft-backed)
// across multiple hosts.
//
// Mechanism: an etcd lease + a uniquely-keyed PUT under a shared
// prefix. The lease auto-expires after a TTL the client controls;
// concurrent.Mutex (from etcd's concurrency package) does the
// queue-and-acquire dance that gives FIFO fairness across processes.
// We use TryLock for non-blocking semantics so Acquire never waits —
// matches the family contract.
//
// # When to choose etcdlock vs the rest of the family
//
//	┌──────────────────────────┬────────────────────────────────────────┐
//	│ ubgo/etcdlock            │ Multi-host. Need strong consistency    │
//	│                          │ (Raft). Can run an etcd cluster.       │
//	│ ubgo/redislock           │ Multi-host. AP semantics OK.           │
//	│ ubgo/pglock              │ Multi-host. Already running Postgres.  │
//	│ ubgo/flock               │ Single-host. Kernel-fenced.            │
//	│ ubgo/filelock            │ Single-host. Operator-readable marker. │
//	└──────────────────────────┴────────────────────────────────────────┘
//
// # Crash safety via lease auto-expiry
//
// Each Acquire grants the holder an etcd lease with a configurable
// TTL ([WithTTL], default 30s). The etcd client transparently
// keep-alives the lease while the holder runs, so healthy processes
// never lose their lock to TTL timeout. If the process crashes (or
// is partitioned from etcd long enough), the lease expires and etcd
// deletes the holder's key automatically — the next Acquire wins.
//
// # Fencing tokens — etcd's mod_revision
//
// etcd's strong consistency gives us a free monotonic fencing token:
// the mod_revision of the holder's key. Token() returns it as
// uint64. mod_revision is monotonic across the entire etcd cluster
// (not just this lock's name) so it's a globally-ordered fence —
// stronger than redislock's per-name INCR.
//
// # Two ways to use it
//
//	cli, _ := clientv3.New(clientv3.Config{Endpoints: []string{"etcd:2379"}})
//	defer cli.Close()
//
//	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(60*time.Second))
//	err := locks.WithLock(ctx, "import-orders", importOrders)
package etcdlock

import "errors"

// ErrLocked is returned when the lock is held by another holder.
var ErrLocked = errors.New("etcdlock: already locked")
