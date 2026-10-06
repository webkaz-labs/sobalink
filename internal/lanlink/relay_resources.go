package lanlink

import (
	"fmt"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

const DefaultRelayCandidateAttempts = 4
const DefaultRelayTLSConnections = 64
const DefaultRelayAdmissionConnections = 16

// RelayResources is immutable per runtime. The counts are resource budgets,
// not limits on saved candidates, permission, or what NATs can support.
type RelayResources struct {
	PresenceConnections  int `json:"presenceConnections"`
	CandidateAttempts    int `json:"candidateAttempts"`
	TLSConnections       int `json:"tlsConnections"`
	AdmissionConnections int `json:"admissionConnections"`
}

func (r RelayResources) WithDefaults() (RelayResources, error) {
	if r.PresenceConnections == 0 {
		r.PresenceConnections = tailcat.DefaultRelayPresenceConnections
	}
	if r.CandidateAttempts == 0 {
		r.CandidateAttempts = DefaultRelayCandidateAttempts
	}
	if r.TLSConnections == 0 {
		r.TLSConnections = DefaultRelayTLSConnections
	}
	if r.AdmissionConnections == 0 {
		r.AdmissionConnections = DefaultRelayAdmissionConnections
	}
	if r.PresenceConnections < 1 || r.PresenceConnections > tailcat.RelayRegionNamespace || r.CandidateAttempts < 1 || r.CandidateAttempts > tailcat.RelayRegionNamespace || r.TLSConnections < 1 || r.AdmissionConnections < 1 {
		return RelayResources{}, fmt.Errorf("relay resource budgets must be positive; relayPresenceConnections and relayCandidateAttempts must fit the nonzero 16-bit DERP identifier space")
	}

	return r, nil
}

type RouteEnvelopeCapacityError struct{ Bytes, Limit int }

func (e *RouteEnvelopeCapacityError) Error() string {
	return fmt.Sprintf("route update needs %d plaintext bytes; the existing exchange protocol supports %d bytes; choose a smaller explicitly offered candidate set", e.Bytes, e.Limit)
}
func (*RouteEnvelopeCapacityError) ErrorCode() string { return "lan_route_envelope_capacity" }
