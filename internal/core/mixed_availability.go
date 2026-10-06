package core

import (
	"context"
	"errors"
	"net"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/identity"
)

const (
	mixedReady        = "ready"
	mixedAbsent       = "confirmed-unavailable"
	mixedAuthRequired = "authorization-required"
	mixedUnknown      = "readiness-unconfirmed"
)

var errMixedReadinessUnknown = errors.New("mixed backend readiness is unconfirmed; wait or inspect backend status before connecting")

// Only explicit availability outcomes permit trying another unopened route.
// Every leaf must be positive: joining a closed error with an authorization,
// configuration, cancellation or unknown error must never hide that failure.
func mixedUnavailable(err error) bool { return mixedAvailabilityError(err, false) }

// A child that has never acknowledged startup may have exited because of bad
// saved state, authentication or configuration. EOF alone proves none of those
// safe to bypass. Startup permits only directly observed local address absence.
func mixedStartupUnavailable(err error) bool { return mixedAvailabilityError(err, true) }

func mixedAvailabilityError(err error, startup bool) bool {
	if err == nil {
		return false
	}
	switch err {
	case directlan.ErrLocalAddressUnavailable:
		return true
	case net.ErrClosed, backendworker.ErrClosed, connectionroute.ErrUnavailable:
		return !startup
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !mixedAvailabilityError(child, startup) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return mixedAvailabilityError(wrapped.Unwrap(), startup)
	}
	return false
}

func mixedReadiness(s identity.State) string {
	if s.Backend == "NeedsLogin" || s.Backend == "NeedsMachineAuth" {
		return mixedAuthRequired
	}
	if s.Backend == "unavailable" && !s.Snapshot.Running {
		return mixedAbsent
	}
	if s.Snapshot.Running && (s.Backend == "Running" || s.Backend == "ready") {
		return mixedReady
	}
	return mixedUnknown
}

func (n *mixedBackend) routeReadiness(id string, routes map[string][]mixedPeerRoute, states map[string]identity.State) error {
	if n.bindingDenied(id, routes, states) {
		return connectionroute.ErrDenied
	}
	required := map[string]bool{}
	for _, r := range routes[id] {
		if r.peer.Expired {
			return connectionroute.ErrDenied
		}
		required[r.backend] = true
	}
	n.mu.Lock()
	for _, b := range n.bindings {
		if b.PeerID == id {
			for _, claim := range b.Identities {
				required[claim.Backend] = true
			}
		}
	}
	n.mu.Unlock()
	for name := range required {
		switch mixedReadiness(states[name]) {
		case mixedAuthRequired:
			return connectionroute.ErrDenied
		case mixedUnknown:
			return errMixedReadinessUnknown
		}
	}
	return nil
}

func (n *mixedBackend) admitMixedRoute(ctx context.Context, id string) error {
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil {
		return e
	}
	return n.routeReadiness(id, routes, states)
}
