package redislock

import "github.com/redis/go-redis/v9"

// releaseScript deletes the lock key only if the value still matches
// our holder's unique value. Without this guard, a Release from a
// holder whose TTL already expired could blow away a SUCCESSOR's
// lock — silently breaking the mutex.
//
// KEYS[1] = lock key
// ARGV[1] = expected holder value
//
// Returns 1 if the key was deleted (we held it), 0 if not (lock lost).
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`)

// extendScript extends the TTL on the lock key only if the value
// still matches our holder. Used for periodic auto-renewal so a
// long-running healthy job doesn't lose its lock to TTL expiry.
//
// KEYS[1] = lock key
// ARGV[1] = expected holder value
// ARGV[2] = new TTL in milliseconds
//
// Returns 1 if extended, 0 if the lock is no longer ours.
var extendScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`)
