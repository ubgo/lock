package flock

import "os"

// Option configures a [Lock], a [Factory], or a single Acquire / WithLock
// call. The same Option type works in all three places — see
// applyOptions for the precedence rules (per-call beats factory beats
// library default).
type Option func(*config)

type config struct {
	dir           string
	maxConcurrent int // 1 == singleton (default), n>1 == semaphore
	observe       observeOptions
}

func defaultConfig() config {
	return config{
		dir:           os.TempDir(),
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

// WithDir places the lock file in dir. When set on a [Factory] it
// applies to every Acquire / WithLock call made through that factory;
// a per-call WithDir overrides it. Default is [os.TempDir].
func WithDir(dir string) Option {
	return func(c *config) { c.dir = dir }
}

// WithMaxConcurrent enables semaphore mode: up to n holders may hold
// the lock for the same name simultaneously. Default n=1 is singleton
// mode. Values < 1 are clamped to 1.
//
// Layout — singleton mode keeps `<dir>/<name>.lock`; n>1 uses
// `<dir>/<name>.<slot>.lock` for slot in 0..n-1. The kernel handles
// crash safety on each slot file independently.
//
// IMPORTANT — every caller of the same lock name must pass the same
// n. We deliberately do NOT persist n in metadata; deploy callers
// together with matching WithMaxConcurrent.
func WithMaxConcurrent(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxConcurrent = n
	}
}
