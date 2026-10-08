//go:build endpoint_following_acceptance

package core

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"sync"
)

// Bounded observation only. No callbacks, authority setters, network/file I/O,
// key/proof/address payloads, or error text are retained by this diagnostic.
type endpointAcceptanceEvent struct{ Stage, Code string }

var endpointAcceptanceEvents struct {
	sync.Mutex
	cores   map[*Core][]endpointAcceptanceEvent
	dropped map[*Core]int
}

func endpointAcceptanceErrorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, endpointmeta.ErrReview):
		return "review"
	case errors.Is(err, endpointmeta.ErrExpired):
		return "expired"
	case errors.Is(err, endpointmeta.ErrIdentity):
		return "identity"
	case errors.Is(err, endpointmeta.ErrPolicy), errors.Is(err, directlan.ErrPolicy):
		return "policy"
	case errors.Is(err, endpointmeta.ErrCapacity), errors.Is(err, directlan.ErrCapacity):
		return "capacity"
	case errors.Is(err, endpointmeta.ErrRecovery), errors.Is(err, directlan.ErrRecovery):
		return "recovery"
	case errors.Is(err, endpointmeta.ErrStale):
		return "stale"
	case errors.Is(err, endpointmeta.ErrConflict):
		return "conflict"
	case errors.Is(err, directlan.ErrUntrusted):
		return "untrusted"
	case errors.Is(err, directlan.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, directlan.ErrRetirementIncomplete):
		return "retirement_incomplete"
	default:
		return "other"
	}
}
func enableEndpointAcceptanceTrace(cores ...*Core) {
	endpointAcceptanceEvents.Lock()
	defer endpointAcceptanceEvents.Unlock()
	if endpointAcceptanceEvents.cores == nil {
		endpointAcceptanceEvents.cores = make(map[*Core][]endpointAcceptanceEvent)
		endpointAcceptanceEvents.dropped = make(map[*Core]int)
	}
	for _, c := range cores {
		endpointAcceptanceEvents.cores[c] = nil
		endpointAcceptanceEvents.dropped[c] = 0
	}
}
func disableEndpointAcceptanceTrace(cores ...*Core) {
	endpointAcceptanceEvents.Lock()
	defer endpointAcceptanceEvents.Unlock()
	for _, c := range cores {
		delete(endpointAcceptanceEvents.cores, c)
		delete(endpointAcceptanceEvents.dropped, c)
	}
}
func observeEndpointAcceptance(c *Core, stage string, err error) {
	endpointAcceptanceEvents.Lock()
	defer endpointAcceptanceEvents.Unlock()
	events, enabled := endpointAcceptanceEvents.cores[c]
	if !enabled {
		return
	}
	if len(events) >= 64 {
		endpointAcceptanceEvents.dropped[c]++
		return
	}
	endpointAcceptanceEvents.cores[c] = append(events, endpointAcceptanceEvent{Stage: stage, Code: endpointAcceptanceErrorCode(err)})
}
func readEndpointAcceptanceTrace(c *Core) ([]endpointAcceptanceEvent, int) {
	endpointAcceptanceEvents.Lock()
	defer endpointAcceptanceEvents.Unlock()
	return append([]endpointAcceptanceEvent(nil), endpointAcceptanceEvents.cores[c]...), endpointAcceptanceEvents.dropped[c]
}
