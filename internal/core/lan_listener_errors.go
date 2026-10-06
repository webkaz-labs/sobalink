package core

import (
	"context"
	"errors"
	"net"
)

// Classify only an actual local listen error from starting the hosted relay.
// Reuse platform errno handling, but not loopback service-port advice. Never
// include the raw error: it can contain a local address or private path.
func classifyLANRelayStartError(err error) error {
	code := "lan_start_failed"
	// A joined outcome may include an unrelated or unknown failure. It must not
	// acquire a precise bind diagnosis just because one branch matches an errno.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if _, joined := cause.(interface{ Unwrap() []error }); joined {
			return lanRelayStartError(code)
		}
	}
	var op *net.OpError
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.As(err, &op) && op.Op == "listen" {
		switch listenerSystemErrorCode(err) {
		case "listener_conflict":
			code = "lan_listener_conflict"
		case "listener_permission_denied":
			code = "lan_listener_permission_denied"
		case "listener_capacity":
			code = "lan_listener_capacity"
		case "listener_address_unavailable":
			code = "lan_listener_address_unavailable"
		}
	}
	return lanRelayStartError(code)
}

func lanRelayStartError(code string) error {
	messages := map[string]string{
		"lan_listener_conflict":            "a local relay port is already in use; stop the conflicting listener separately, then retry the saved endpoint",
		"lan_listener_permission_denied":   "the operating system denied a local relay listener; review local permissions before retrying",
		"lan_listener_capacity":            "local relay listener resources are exhausted; stop unused work or review available resources before retrying",
		"lan_listener_address_unavailable": "a local relay address or address family is unavailable; reconnect the selected network or review the saved endpoint before retrying",
		"lan_start_failed":                 "could not start the LAN relay for an unclassified reason; review the saved configuration before retrying; private identity state was retained",
	}
	return &lanCommandError{code, messages[code]}
}
