package core

import (
	"context"
	"encoding/json"
	"time"
)

// DiscoveryObservation describes only a local authenticated-query outcome.
// An unavailable reply never establishes that the peer or its application is
// offline, and a registered Tailnet peer is not evidence of a running bridge.
type DiscoveryObservation struct {
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Code      string    `json:"code,omitempty"`
	Services  int       `json:"services"`
}

func (c *Core) recordDiscoveryObservation(id string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordDiscoveryObservationLocked(id, err)
}

func (c *Core) recordDiscoveryObservationLocked(id string, err error) {
	observation := DiscoveryObservation{State: "confirmed", CheckedAt: time.Now()}
	if err != nil {
		observation.State, observation.Code = "unconfirmed", "discovery_unconfirmed"
		switch networkErrorCode(err) {
		case "discovery_unsupported":
			observation.State, observation.Code = "unsupported", "discovery_unsupported"
		case "discovery_capacity":
			observation.State, observation.Code = "limited", "discovery_capacity"
		}
	}
	if c.discoveryObservations == nil {
		c.discoveryObservations = map[string]DiscoveryObservation{}
	}
	if err != nil {
		delete(c.confirmed, id)
		delete(c.discovered, id)
	} else {
		observation.Services = len(c.discovered[id])
	}
	c.discoveryObservations[id] = observation
}

func (c *Core) discoveryObservation(id string) DiscoveryObservation {
	c.mu.RLock()
	observation, exists := c.discoveryObservations[id]
	c.mu.RUnlock()
	if !exists {
		return DiscoveryObservation{State: "pending"}
	}
	if !freshDiscoveryCheck(observation.CheckedAt, time.Now()) {
		observation.State = "stale"
	}
	return observation
}

func (c *Core) refreshDiscoveryCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		PeerID string `json:"peerId"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	st, err := c.current(ctx)
	if err != nil || !st.Snapshot.Running {
		return nil, &localCommandError{"discovery_network_unavailable", "connect the selected network before refreshing shared services"}
	}
	if in.PeerID != "" {
		if _, err := c.currentPeer(ctx, in.PeerID); err != nil {
			return nil, err
		}
		_ = c.probePeer(ctx, in.PeerID)
	} else {
		limited, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		c.refreshPeerBatch(limited, st, c.probePeer)
	}
	observations := []map[string]any{}
	partial := false
	for _, peer := range st.Snapshot.Peers {
		if peer.Expired || peer.ID == "" || in.PeerID != "" && peer.ID != in.PeerID {
			continue
		}
		o := c.discoveryObservation(peer.ID)
		partial = partial || o.State == "pending" || o.State == "stale" || o.State == "limited"
		observations = append(observations, map[string]any{"peerId": peer.ID, "state": o.State, "checkedAt": o.CheckedAt, "code": o.Code, "services": o.Services})
	}
	services := c.discoveredViews()
	if in.PeerID != "" {
		filtered := services[:0]
		for _, service := range services {
			if service["peerId"] == in.PeerID {
				filtered = append(filtered, service)
			}
		}
		services = filtered
	}
	return map[string]any{"services": services, "observations": observations, "partial": partial}, nil
}
