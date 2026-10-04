package core

import (
	"errors"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// The receiver publishes auto-accept only after this atomic profile commit.
// Profile mutation and a receive-policy change share the local command lock.
type receiveStore struct{ core *Core }

func (s receiveStore) LoadPolicies() ([]transfer.ReceivePolicy, error) {
	var out []transfer.ReceivePolicy
	profile := s.core.profileCopy()
	for _, p := range profile.Peers {
		if p.Network == profile.Settings.Network && p.Autosave {
			out = append(out, transfer.ReceivePolicy{Peer: transfer.Peer{ID: p.ID, Generation: p.Generation}, Destination: p.Directory, AutoAccept: true})
		}
	}
	return out, nil
}
func (s receiveStore) SavePolicies(policies []transfer.ReceivePolicy) error {
	return s.savePolicies(policies, nil)
}

// updated is the complete reviewed peer setting merged under the Core command
// lock. Manager holds its own lock until this one durable save and the combined
// policy/pause publication complete; the callback never reenters Manager.
func (s receiveStore) savePolicies(policies []transfer.ReceivePolicy, updated *Trust) error {
	p := s.core.profileCopy()
	before := privateRevision(p)
	for i := range p.Peers {
		if p.Peers[i].Network == p.Settings.Network {
			p.Peers[i].Autosave = false
		}
	}
	if updated != nil {
		found := false
		for i := range p.Peers {
			if p.Peers[i].Network == p.Settings.Network && p.Peers[i].Network == updated.Network && p.Peers[i].ID == updated.ID && p.Peers[i].Generation == updated.Generation {
				p.Peers[i] = *updated
				// The runtime policy list is authoritative after a fail-closed
				// removal. A pause-only edit must not restore an old approval.
				p.Peers[i].Autosave = false
				found = true
				break
			}
		}
		if !found {
			return errors.New("receive policy peer changed")
		}
	}
	for _, policy := range policies {
		found := false
		for i := range p.Peers {
			if p.Peers[i].Network == p.Settings.Network && p.Peers[i].ID == policy.Peer.ID && p.Peers[i].Generation == policy.Peer.Generation {
				p.Peers[i].Autosave = policy.AutoAccept
				p.Peers[i].Directory = policy.Destination
				found = true
				break
			}
		}
		if !found {
			return errors.New("receive policy peer changed")
		}
	}
	// Revocation/binding may ask the policy store to persist the profile that
	// its owner already replaced. Do not turn runtime reconciliation into a
	// second write after an uncertain commit.
	if privateRevision(p) == before {
		return nil
	}
	saveErr := s.core.saveProfile(p)
	if !atomicPublished(saveErr) {
		return saveErr
	}
	s.core.mu.Lock()
	s.core.profile = p
	s.core.mu.Unlock()
	return saveErr
}

// Called under c.op before the first backend construction, when an offline
// session explicitly selects another trust namespace. No pointer replacement or
// persistence occurs here; the profile retains inactive-network approvals.
func (c *Core) resetTransferNetwork(profile Profile) error {
	if c.attemptedNetwork != "" || c.nodeCopy() != nil {
		return errors.New("restart with --offline before changing the receive trust scope")
	}
	var peers []transfer.Peer
	var policies []transfer.ReceivePolicy
	var paused []string
	for _, p := range profile.Peers {
		if p.Network != profile.Settings.Network {
			continue
		}
		peer := transfer.Peer{ID: p.ID, Generation: p.Generation}
		peers = append(peers, peer)
		if p.Autosave {
			policies = append(policies, transfer.ReceivePolicy{Peer: peer, Destination: p.Directory, AutoAccept: true})
		}
		if p.Paused {
			paused = append(paused, p.ID)
		}
	}
	if err := c.transfers.ResetBindings(peers, policies); err != nil {
		return errors.New("receive state cannot switch while transfers exist; stop soba and start with --offline")
	}
	for _, id := range paused {
		if err := c.transfers.PausePeer(id, true); err != nil {
			return err
		}
	}
	c.transferNetwork = profile.Settings.Network
	return nil
}
