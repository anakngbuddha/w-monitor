package collector

// counterDelta computes the increase between two readings of a monotonic
// counter (network bytes, disk operations, etc).
//
// Kernel counters reset on reboot, NIC reset, or driver reload, and can wrap.
// The previous implementation treated `cur < prev` as `delta = cur`, which
// reported the entire since-boot total as if it had happened in one interval:
// a single sample claiming gigabytes per second. That one bogus sample then
// poisoned every average and maximum computed over the window.
//
// Emitting 0 on reset loses at most one interval of real traffic, which is far
// cheaper than injecting a fabricated spike into the history.
func counterDelta(cur, prev uint64) (delta uint64, reset bool) {
	if cur < prev {
		return 0, true
	}
	return cur - prev, false
}
