package lanlink

import (
	"context"
	"syscall"
)

// Failover requires positively identified availability, never an unknown proof
// failure or a timeout that may conceal an asynchronous pin/admission failure.
// Every joined cause must independently allow retry; one denial stops the join.
func relayAvailabilityError(err error) bool { return relayAvailabilityCause(err, 0) }
func relayAvailabilityCause(err error, depth int) bool {
	// A malformed/cyclic diagnostic chain never authorizes fallback.
	if depth > 32 {
		return false
	}
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded || err == ErrRoutePermission || err == ErrUntrusted || err == ErrRelayMismatch {
		return false
	}
	if staged, ok := err.(interface{ DERPFailurePhase() string }); ok && staged.DERPFailurePhase() != "dial" {
		return false
	}
	if _, coded := err.(interface{ ErrorCode() string }); coded {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !relayAvailabilityCause(cause, depth+1) {
				return false
			}
		}
		return true
	}
	if wrapper, ok := err.(interface{ Unwrap() error }); ok {
		return relayAvailabilityCause(wrapper.Unwrap(), depth+1)
	}
	cause, ok := err.(syscall.Errno)
	if !ok {
		return false
	}
	switch cause {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ETIMEDOUT:
		return true
	}
	return relayPlatformAvailability(cause)
}
