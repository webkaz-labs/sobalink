package core

import (
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// The complete current projection has already observed the protected model.
// Copy original process bounds used by positively projected authority only;
// terminal/inactive evidence cannot expire unrelated active peers.
func (s *directLANStore) activeEndpointDeadlinesLocked(cfg directlan.Config, now time.Time) (map[directLANEndpointDeadlineKey]directLANEndpointDeadline, error) {
	bounds := make(map[directLANEndpointDeadlineKey]directLANEndpointDeadline)
	if s.state.Metadata == nil {
		return bounds, nil
	}
	for _, record := range s.state.Metadata.Peers {
		if _, active := cfg.PairContexts[record.Peer.Key]; !active {
			continue
		}
		state := record.EndpointState
		if state == nil || state.Approval == nil {
			continue
		}
		for key, absolute := range endpointDeadlineKeys(state) {
			if key.kind == "follow" && state.Approval.Kind != "follow" {
				continue
			}
			bound, ok := s.endpointDeadlines[key]
			if !ok || bound.abs != absolute || bound.expired || bound.monotonic.IsZero() || !now.Before(bound.monotonic) {
				return nil, endpointmeta.ErrExpired
			}
			bounds[key] = bound
		}
	}
	return bounds, nil
}

func earliestEndpointDeadline(bounds map[directLANEndpointDeadlineKey]directLANEndpointDeadline) time.Time {
	var deadline time.Time
	for _, bound := range bounds {
		if deadline.IsZero() || bound.monotonic.Before(deadline) {
			deadline = bound.monotonic
		}
	}
	return deadline
}
