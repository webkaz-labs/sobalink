package core

import (
	"errors"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
)

type mixedError struct {
	code, message string
	cause         error
}

func (e *mixedError) Error() string     { return e.message }
func (e *mixedError) ErrorCode() string { return e.code }
func (e *mixedError) Unwrap() error     { return e.cause }
func (c *Core) codedMixedError(err error) error {
	if err == nil || networkErrorCode(err) != "" {
		return err
	}
	c.mu.RLock()
	recovery := c.networkFatal != ""
	c.mu.RUnlock()
	if recovery {
		return &mixedError{"mixed_recovery_required", "Mixed networking is paused because permission state could not be confirmed; inspect saved approvals before restarting", err}
	}
	if errors.Is(err, connectionroute.ErrBinding) || errors.Is(err, connectionroute.ErrDenied) {
		return &mixedError{"mixed_binding_unverified", "Identity binding was not verified; check the exact authenticated peer routes and permissions on both devices", err}
	}
	return &mixedError{"mixed_operation_failed", "Mixed operation did not complete; check selected backend readiness and review saved settings before retrying", err}
}
