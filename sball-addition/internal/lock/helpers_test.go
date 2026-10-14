package lock

import "time"

// timeAfter is a tiny indirection so the tests can keep the lock for a moment
// without sleeping in the hot path.
func timeAfter() <-chan time.Time { return time.After(150 * time.Millisecond) }
