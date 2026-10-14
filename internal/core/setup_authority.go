package core

import (
	"reflect"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

// Called in local command ordering before a mutation. Revocation is never
// blocked by setup: it cancels the old session and then proceeds normally.
func (c *Core) invalidateSetupAuthority(name string) {
	mutation := name == "network.configure" || name == "network.logout" || strings.HasPrefix(name, "peer.") || name == "lan.join" || name == "lan.invite" || name == "lan.cancel" || name == "lan.revoke" || name == "lan.policy.set" || strings.HasPrefix(name, "lan.routes.") && name != "lan.routes.list" && name != "lan.routes.inspect" && name != "lan.routes.review" || name == "wan.candidates.set" || name == "mixed.bind" || name == "mixed.unbind" || name == "policy.apply" || name == "settings.update" || name == "profile.import" || strings.HasPrefix(name, "startup.") || strings.HasPrefix(name, "proxy.saved.")
	if !mutation {
		return
	}
	if s := c.setupCopy(); s != nil {
		s.mu.Lock()
		active := s.ctx.Err() == nil
		s.mu.Unlock()
		if active {
			s.stop("cancelled", "setup_authority_changed", "try_again")
		}
	}
}
func (c *Core) setupProfileReview() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return privateRevision(struct {
		Profile             Profile
		Attempted, Hostname string
		Capacity            any
		Startup, Proxies    any
	}{c.profile, c.attemptedNetwork, c.attemptedHostname, c.capacity, c.startup, c.savedProxies})
}

// Caller holds c.op. A single selected-peer addition may be produced by the
// already-authorized lanlink pairing persistence; no removal or other change
// can be explained away as setup's own work.
func (s *setupSession) checkAuthority(node lanNetworkBackend) error {
	c := s.core
	if s.ctx.Err() != nil || c.nodeCopy() != node || node.PublicKey() != s.local.Key || s.currentScope() != nil {
		return setupError("setup_authority_changed", "setup's current network or scope changed; start a new review")
	}
	store := c.lanStoreCopy()
	if store == nil || setupLANNeedsRecovery(store) {
		return setupError("setup_authority_changed", "saved LAN authority requires review")
	}
	current := store.copy()
	profile := c.setupProfileReview()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peer != nil {
		if e := s.verifySavedBasis(s.peer.Key); e != nil {
			return e
		}
	}
	if profile != s.authorityProfile {
		return setupError("setup_authority_changed", "local permissions changed during setup")
	}
	if reflect.DeepEqual(s.authority, current) {
		return nil
	}
	if !s.pairingAttempted || s.paired || s.peer == nil || !setupOnlyPairAdded(s.authority, current, s.peer.Key) {
		return setupError("setup_authority_changed", "saved LAN permissions changed during setup")
	}
	s.authority = current
	return nil
}
func setupOnlyPairAdded(before, after lanState, peer string) bool {
	for _, p := range before.Trust.Peers {
		if p.Key == peer {
			return false
		}
	}
	if len(after.Trust.Peers) != len(before.Trust.Peers)+1 || len(after.Remotes) != len(before.Remotes)+1 {
		return false
	}
	peers := []lanlink.Peer{}
	remotes := []lanlink.RemotePeer{}
	foundPeer, foundRemote := false, false
	for _, p := range after.Trust.Peers {
		if p.Key == peer {
			if foundPeer {
				return false
			}
			foundPeer = true
		} else {
			peers = append(peers, p)
		}
	}
	for _, p := range after.Remotes {
		if p.Peer.Key == peer {
			if foundRemote {
				return false
			}
			foundRemote = true
		} else {
			remotes = append(remotes, p)
		}
	}
	if !foundPeer || !foundRemote || len(peers) != len(before.Trust.Peers) || len(remotes) != len(before.Remotes) {
		return false
	}
	for i := range peers {
		if peers[i] != before.Trust.Peers[i] {
			return false
		}
	}
	for i := range remotes {
		if !reflect.DeepEqual(remotes[i], before.Remotes[i]) {
			return false
		}
	}
	after.Trust.Peers = before.Trust.Peers
	after.Remotes = before.Remotes
	return reflect.DeepEqual(before, after)
}

func setupLANNeedsRecovery(store *lanStore) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.routeRecovery || store.startUncertain
}
