package etcdlock

import "time"

// Option configures a [Lock], a [Factory], or a single Acquire / WithLock
// call.
type Option func(*config)

type config struct {
	ttl           time.Duration
	prefix        string
	maxConcurrent int // 1 == singleton (default), n>1 == semaphore
	observe       observeOptions
}

func defaultConfig() config {
	return config{
		ttl:           30 * time.Second,
		prefix:        "/ubgo/etcdlock",
		maxConcurrent: 1,
	}
}

func applyOptions(base config, opts []Option) config {
	cfg := base
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithTTL sets the etcd lease TTL. The client auto-keep-alives the
// lease for the duration of the lock, so healthy holders never lose
// the lock to timeout. If the holder crashes or partitions away, the
// lease expires after this much time and the lock becomes available
// to others.
//
// Default 30s. Keep this longer than the max network partition you
// want to tolerate. Minimum 5s — etcd doesn't accept TTLs below.
func WithTTL(d time.Duration) Option {
	return func(c *config) {
		if d < 5*time.Second {
			d = 5 * time.Second
		}
		c.ttl = d
	}
}

// WithKeyPrefix namespaces the etcd keys. Default `/ubgo/etcdlock`,
// producing keys like `/ubgo/etcdlock/import-orders/<lease-id>`.
// Override when multiple services share the same etcd cluster.
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.prefix = prefix }
}

// WithMaxConcurrent enables semaphore mode: up to n holders may hold
// the lock for the same name simultaneously. Default n=1 is singleton
// mode. Values < 1 are clamped to 1.
//
// Implementation — each slot uses a different mutex prefix
// (`<prefix>/<name>/<slot>/`). Acquire iterates slots and returns
// the first one that succeeds.
//
// IMPORTANT — every caller of the same lock name must pass the same
// n. We deliberately do NOT persist n; deploy callers together with
// matching WithMaxConcurrent.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}
