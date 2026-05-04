package pglock

// Option configures a [Lock], a [Factory], or a single Acquire / WithLock
// call.
type Option func(*config)

type config struct {
	keyOffset     int64 // added to the hash of the lock name
	maxConcurrent int   // 1 == singleton (default), n>1 == semaphore
	observe       observeOptions
}

func defaultConfig() config {
	return config{
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

// WithKeyOffset adds a constant offset to every lock-name hash before
// passing it to pg_try_advisory_lock. Use this when other code in the
// same database also uses pg_advisory_lock and you want to guarantee
// no key collisions — e.g. WithKeyOffset(0x1000000000000000) gives
// ubgo/pglock a private upper-bit-prefixed keyspace.
//
// All callers of the same lock name MUST pass the same offset.
// It's a deploy-time constant, not a per-call value.
func WithKeyOffset(off int64) Option {
	return func(c *config) { c.keyOffset = off }
}

// WithMaxConcurrent enables semaphore mode: up to n holders may hold
// the lock for the same name simultaneously. Default n=1 is singleton
// mode. Values < 1 are clamped to 1.
//
// Implementation — each slot uses a different int64 key derived from
// the name's hash + slot index. So `name="x"` with n=3 hashes to
// keys k+0, k+1, k+2 (where k = FNV1a(name) + keyOffset). Acquire
// iterates slots and returns the first one that succeeds.
//
// IMPORTANT — every caller of the same lock name must pass the same
// n. We deliberately do NOT persist n; deploy callers together with
// matching WithMaxConcurrent.
//
// Beware: if other code in the same database also uses
// pg_advisory_lock with keys near our hashed range, semaphore mode
// can collide with that code. Use WithKeyOffset to namespace.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}
