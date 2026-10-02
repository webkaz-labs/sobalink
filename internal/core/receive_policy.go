package core

import (
	"errors"
	"github.com/webkaz-labs/tsnet-bridge/internal/transfer"
)

// The receiver publishes auto-accept only after this atomic profile commit.
// Profile mutation and a receive-policy change share the local command lock.
type receiveStore struct{ core *Core }

func (s receiveStore) LoadPolicies() ([]transfer.ReceivePolicy, error) {
	var out []transfer.ReceivePolicy
	for _, p := range s.core.profileCopy().Peers {
		if p.Autosave {
			out = append(out, transfer.ReceivePolicy{Peer: transfer.Peer{ID: p.ID, Generation: p.Generation}, Destination: p.Directory, AutoAccept: true})
		}
	}
	return out, nil
}
func (s receiveStore) SavePolicies(policies []transfer.ReceivePolicy) error {
	p := s.core.profileCopy()
	for i := range p.Peers {
		p.Peers[i].Autosave = false
	}
	for _, policy := range policies {
		found := false
		for i := range p.Peers {
			if p.Peers[i].ID == policy.Peer.ID && p.Peers[i].Generation == policy.Peer.Generation {
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
	if e := s.core.saveProfile(p); e != nil {
		return e
	}
	s.core.mu.Lock()
	s.core.profile = p
	s.core.mu.Unlock()
	return nil
}
