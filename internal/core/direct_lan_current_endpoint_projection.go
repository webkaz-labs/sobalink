package core

import (
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// projectManagedCurrentEndpoint is deliberately separate from initial fixed
// activation. Its Config is inert: NewNode rejects current-endpoint projections.
// This function cannot mint a receipt, alter PairContext, or discard evidence.
func projectManagedCurrentEndpoint(state directLANState, now time.Time) (directlan.Config, error) {
	if err := validateDirectLANState(state); err != nil {
		return directlan.Config{}, err
	}
	cfg, err := directLANConfig(state)
	if err != nil {
		return directlan.Config{}, err
	}
	if state.Metadata == nil {
		cfg.Peers = append([]directlan.Peer(nil), cfg.Peers...)
		return cfg, nil
	} // Genuine legacy v2 unchanged.
	return directlan.ProjectCurrentEndpointConfig(cfg, *state.Metadata, now)
}

// managedCurrentEndpointProjectionLocked requires Core.op, exclusive profile
// ownership and store.mu. Preserve existing file, recovery, wall and monotonic
// observations; saved-model eligibility alone is never an activation gate.
func (s *directLANStore) managedCurrentEndpointProjectionLocked(now time.Time) (directlan.Config, error) {
	state := s.state
	if state.Metadata == nil {
		if err := s.endpointFileCurrentLocked(); err != nil {
			return directlan.Config{}, err
		}
		if err := s.observeEndpointTimeLocked(now); err != nil {
			return directlan.Config{}, err
		}
		if s.recovery {
			return directlan.Config{}, directlan.ErrRecovery
		}
	} else {
		model, err := s.endpointModelLocked(now, false)
		if err != nil {
			return directlan.Config{}, err
		}
		for _, record := range model.Peers {
			if record.PairRevocation != nil {
				continue
			}
			if err := s.endpointDeadlineErrorLocked(record.EndpointState, "project-current", now); err != nil {
				return directlan.Config{}, err
			}
		}
		state.Metadata = model
	}
	return projectManagedCurrentEndpoint(state, now)
}
