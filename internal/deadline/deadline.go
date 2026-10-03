// Package deadline checks permission lifetimes against elapsed and wall time.
package deadline

import "time"

// Passed reports whether either clock has reached an optional deadline. A zero
// deadline is absent, matching legacy optional grant and lease semantics.
// UTC strips monotonic metadata: a suspend that pauses elapsed time must not
// extend a wall-time deadline, and a wall-clock rollback must not extend the
// elapsed lifetime. Restarted grants must still be recreated explicitly.
func Passed(now, until time.Time) bool {
	return passedByClocks(now, until, now.UTC(), until.UTC())
}

// Active requires a nonzero deadline that neither clock has reached. Use this
// for bounded grants and invitations, where a missing expiry must deny access.
func Active(now, until time.Time) bool {
	return !until.IsZero() && !Passed(now, until)
}

// Separate readings make disagreement testable without changing process clocks
// or relying on the private representation of time.Time's monotonic metadata.
func passedByClocks(now, until, wallNow, wallUntil time.Time) bool {
	return !until.IsZero() && (!now.Before(until) || !wallNow.Before(wallUntil))
}
