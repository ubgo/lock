package redislock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lock is a single-name distributed lock. Construct one via [New].
type Lock struct {
	rdb  redis.Scripter
	name string
	cfg  config
}

// New returns a [Lock] for the given name. rdb is any
// `github.com/redis/go-redis/v9` client (Client, ClusterClient, or a
// redis.Scripter mock for tests).
func New(rdb redis.Scripter, name string, opts ...Option) *Lock {
	return &Lock{
		rdb:  rdb,
		name: name,
		cfg:  applyOptions(defaultConfig(), opts),
	}
}

// Name returns the lock name.
func (l *Lock) Name() string {
	return l.name
}

// Key returns the full Redis key derived from prefix + name.
// In semaphore mode this is the singleton-style key (no slot suffix);
// the actual slot key for a specific holder is reachable via
// [Holder.Key].
func (l *Lock) Key() string {
	return lockKey(l.cfg.keyPrefix, l.name)
}

// Acquire performs SET key value NX EX <ttl>. Returns a [Holder] on
// success; returns [ErrLocked] when no slot is available; any other
// error is from the Redis client.
//
// In semaphore mode (cfg.maxConcurrent > 1) acquire iterates slots
// 0..n-1 and returns the first one that succeeds.
//
// Per-call opts override the Lock's construction-time configuration.
func (l *Lock) Acquire(ctx context.Context, opts ...Option) (*Holder, error) {
	cfg := applyOptions(l.cfg, opts)
	return acquire(ctx, l.rdb, l.name, cfg)
}

// lockKey returns the Redis key for singleton mode.
func lockKey(prefix, name string) string {
	return prefix + ":" + name
}

// slotLockKey returns the per-slot key used in semaphore mode.
func slotLockKey(prefix, name string, slot int) string {
	return fmt.Sprintf("%s:%s:%d", prefix, name, slot)
}

// fenceKey returns the Redis key for a lock's fencing-token counter.
// Per-name (not per-slot) so a single monotonic sequence covers all
// holders of that lock name.
func fenceKey(prefix, name string) string {
	return prefix + ":" + name + ":fence"
}

// acquire is the internal worker shared by Lock.Acquire and
// Factory.Acquire.
//
// Algorithm:
//
//  1. Generate a unique random holder identifier.
//  2. If a TraceID extractor is configured, encode it into the
//     holder value as `<random>|trace:<id>` so operators reading
//     `redis-cli GET <key>` see the originating trace.
//  3. SET key value NX EX <ttl> — atomic acquire-or-fail. In
//     semaphore mode iterate slots 0..n-1.
//  4. INCR fenceKey for the monotonic token (per-name).
//  5. Return a Holder.
func acquire(ctx context.Context, rdb redis.Scripter, name string, cfg config) (h *Holder, err error) {
	start := time.Now()
	traceID := cfg.observe.extractTraceID(ctx)

	ctx, finishSpan := cfg.observe.startSpan(ctx, "redislock.Acquire")
	defer func() {
		finishSpan(err)
		outcome := outcomeFor(err)
		cfg.observe.recordAcquire(name, outcome, time.Since(start), traceID, err)
		if outcome == OutcomeAcquired {
			cfg.observe.recordActive(name, +1)
			if h != nil {
				h.observe = cfg.observe
				h.name = name
				h.acquiredAt = time.Now()
			}
		}
	}()

	if err = ctx.Err(); err != nil {
		return nil, err
	}

	cmdable, ok := rdb.(redis.Cmdable)
	if !ok {
		return nil, errors.New("redislock: rdb must implement redis.Cmdable")
	}

	if cfg.maxConcurrent <= 1 {
		h, err = acquireSlot(ctx, cmdable, name, cfg, -1, traceID)
		return h, err
	}
	for slot := 0; slot < cfg.maxConcurrent; slot++ {
		h, err = acquireSlot(ctx, cmdable, name, cfg, slot, traceID)
		if err == nil {
			return h, nil
		}
		if errors.Is(err, ErrLocked) {
			continue
		}
		return nil, err
	}
	return nil, ErrLocked
}

// acquireSlot tries to SET-NX one slot key. Pass slot=-1 for
// singleton mode; slot>=0 for the per-slot key.
func acquireSlot(ctx context.Context, cmdable redis.Cmdable, name string, cfg config, slot int, traceID string) (*Holder, error) {
	identifier, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("redislock: random: %w", err)
	}
	value := identifier
	if traceID != "" {
		value = identifier + "|trace:" + traceID
	}

	var key string
	if slot < 0 {
		key = lockKey(cfg.keyPrefix, name)
	} else {
		key = slotLockKey(cfg.keyPrefix, name, slot)
	}

	gotIt, err := cmdable.SetNX(ctx, key, value, cfg.ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("redislock: setnx: %w", err)
	}
	if !gotIt {
		return nil, ErrLocked
	}

	// Fence token via INCR on a sibling key (per-name; shared
	// across all slots of the same name). If INCR fails (network
	// blip) we still return a valid Holder — Token() will be 0.
	tok, _ := cmdable.Incr(ctx, fenceKey(cfg.keyPrefix, name)).Result()

	return &Holder{
		rdb:   cmdable.(redis.Scripter),
		key:   key,
		value: value,
		token: uint64(tok), //nolint:gosec // INCR returns non-negative
		ttl:   cfg.ttl,
	}, nil
}

// HolderIdentifier returns just the random part of a holder's value
// (without the optional |trace:<id> suffix). Useful for log lines
// that want the identifier without the trace ID.
func HolderIdentifier(value string) string {
	if i := strings.IndexByte(value, '|'); i >= 0 {
		return value[:i]
	}
	return value
}

// randomToken returns a random hex string used as the holder's
// unique identity for this acquire. 16 bytes = 128 bits = collision-
// proof under any realistic acquire rate.
func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// outcomeFor maps an Acquire error into the stable Outcome string.
func outcomeFor(err error) string {
	switch {
	case err == nil:
		return OutcomeAcquired
	case errors.Is(err, ErrLocked):
		return OutcomeErrLocked
	default:
		return OutcomeError
	}
}
