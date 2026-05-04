package pglock

import "hash/fnv"

// hashKey converts a string lock name into the int64 key that
// pg_advisory_lock takes. FNV-1a is used because it's fast, has
// good distribution for short strings, and is in the stdlib. The
// 64-bit space (~1.8e19) makes accidental collisions effectively
// impossible for any realistic lock-name count.
//
// We cast through uint64 → int64 (allowing negative values) so the
// full 64-bit space is usable. pg_advisory_lock accepts negative
// integers without complaint.
func hashKey(name string, offset int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64()) + offset //nolint:gosec // intentional bit-cast
}
