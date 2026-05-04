package redislock

import "time"

// Option configures a [Lock], a [Factory], or a single Acquire / WithLock
// call. The same Option type works in all three places.
type Option func(*config)

type config struct {
	ttl           time.Duration // lock TTL on Redis key
	keyPrefix     string        // namespace for keys: "<prefix>:<name>"
	maxConcurrent int           // 1 == singleton (default), n>1 == semaphore
	observe       observeOptions
}

func defaultConfig() config {
	return config{
		ttl:           30 * time.Second,
		keyPrefix:     "redislock",
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

// WithTTL sets the lock's expiry on Redis. If the holder process
// crashes (or is paused beyond this window), the key auto-expires
// and another caller can take it. Default 30s.
//
// Sizing — TTL must be longer than the longest healthy run of the
// protected work, otherwise a still-running holder will see its
// lock TTL out from under it. For runs longer than ~30s use
// [Holder.Extend] periodically.
func WithTTL(d time.Duration) Option {
	return func(c *config) { c.ttl = d }
}

// WithKeyPrefix namespaces the Redis keys. Default `"redislock"`,
// producing keys like `redislock:nightly-import`. Set this when
// multiple services share a Redis and you want to avoid key
// collisions.
func WithKeyPrefix(prefix string) Option {
	return func(c *config) { c.keyPrefix = prefix }
}

// WithMaxConcurrent enables semaphore mode: up to n holders may hold
// the lock for the same name simultaneously. Default n=1 is singleton
// mode (a single Redis key). Values < 1 are clamped to 1.
//
// Layout — singleton mode keeps `<prefix>:<name>`; n>1 uses
// `<prefix>:<name>:<slot>` for slot in 0..n-1. Acquire iterates
// slots and returns the first one that succeeds.
//
// IMPORTANT — every caller of the same lock name must pass the same
// n. We deliberately do NOT persist n in Redis; deploy callers
// together with matching WithMaxConcurrent.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}
