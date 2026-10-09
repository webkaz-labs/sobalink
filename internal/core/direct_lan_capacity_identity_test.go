package core

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
)

// These fixtures use only synthetic Core/store values and owned temporary
// paths. They never open Core or construct a node, provider, or application.
func TestDirectLANCapacityPublicationPreservesOnlyTransferPolicyIdentity(t *testing.T) {
	for _, transition := range []struct {
		name                string
		change              func(*capacity.Policy)
		omitTransferChoices bool
		preserveIdentity    bool
	}{
		{name: "exact-no-op", preserveIdentity: true},
		{name: "exact-no-op-omitted-transfer-choices", omitTransferChoices: true, preserveIdentity: true},
		{name: "files-only", change: func(p *capacity.Policy) {
			p.Resources["transferConcurrentFiles"] = capacity.Limited(7)
		}, preserveIdentity: true},
		{name: "per-peer-only", change: func(p *capacity.Policy) {
			p.Resources["transferConcurrentPerPeer"] = capacity.Limited(3)
		}, preserveIdentity: true},
		{name: "both-transfer-choices", change: func(p *capacity.Policy) {
			p.Resources["transferConcurrentFiles"] = capacity.Limited(7)
			p.Resources["transferConcurrentPerPeer"] = capacity.Limited(3)
		}, preserveIdentity: true},
		{name: "equal-effective-transfer-choices", change: func(p *capacity.Policy) {
			p.Resources["transferConcurrentFiles"] = capacity.Default()
			p.Resources["transferConcurrentPerPeer"] = capacity.Default()
		}, preserveIdentity: true},
		{name: "changed-peer-limit", change: func(p *capacity.Policy) {
			p.Logical["trustedPeers"] = capacity.Limited(p.Number("logical", "trustedPeers") + 1)
		}},
		{name: "changed-byte-limit", change: func(p *capacity.Policy) {
			p.Resources["lanStateBytes"] = capacity.Limited(p.Number("resources", "lanStateBytes") + 1)
		}},
		{name: "equal-effective-peer-choice", change: func(p *capacity.Policy) {
			p.Logical["trustedPeers"] = capacity.Limited(p.Number("logical", "trustedPeers"))
		}},
		{name: "equal-effective-byte-choice", change: func(p *capacity.Policy) {
			p.Resources["lanStateBytes"] = capacity.Limited(p.Number("resources", "lanStateBytes"))
		}},
		{name: "other-logical-choice", change: func(p *capacity.Policy) {
			p.Logical["savedServices"] = capacity.Limited(p.Number("logical", "savedServices") + 1)
		}},
		{name: "other-resource-with-transfer-choice", change: func(p *capacity.Policy) {
			p.Resources["tcpConnections"] = capacity.Limited(p.Number("resources", "tcpConnections") + 1)
			p.Resources["transferConcurrentFiles"] = capacity.Limited(7)
		}},
	} {
		for _, outcome := range []struct {
			name      string
			err       error
			published bool
		}{
			{name: "published", published: true},
			{name: "published-uncertain", err: errors.Join(errors.New("synthetic durability failure"), config.ErrAtomicCommitted), published: true},
			{name: "not-published", err: config.ErrAtomicBusy},
		} {
			for _, initialized := range []bool{false, true} {
				initialization := "nil-limits"
				if initialized {
					initialization = "existing-limits"
				}
				t.Run(transition.name+"/"+outcome.name+"/"+initialization, func(t *testing.T) {
					current := capacity.Defaults()
					if !transition.omitTransferChoices {
						current.Resources["transferConcurrentFiles"] = capacity.Limited(current.Number("resources", "transferConcurrentFiles"))
						current.Resources["transferConcurrentPerPeer"] = capacity.Limited(current.Number("resources", "transferConcurrentPerPeer"))
					}
					proposed := current.Clone()
					if transition.change != nil {
						transition.change(&proposed)
					}
					originalProposal := proposed.Clone()
					s := &directLANStore{path: filepath.Join(t.TempDir(), "direct-lan.json"), state: directLANState{Version: directLANStateVersion}}
					if initialized {
						s.limits.Store(selectedLANLimits(current))
					}
					before := s.limits.Load()
					c := &Core{capacity: current.Clone(), directLAN: s}
					calls := 0
					err := func() error {
						c.mu.Lock()
						defer c.mu.Unlock()
						return c.applyDirectLANCapacityLocked(proposed, func() error {
							calls++
							if s.mu.TryLock() {
								s.mu.Unlock()
								t.Fatal("publication did not hold the direct LAN store lock")
							}
							if s.limits.Load() != before {
								t.Fatal("limits identity changed before publication")
							}
							if !capacityJSONEqual(c.capacity, current) || !capacityJSONEqual(proposed, originalProposal) {
								t.Fatal("policy comparison mutated its inputs")
							}
							if outcome.published {
								// Match the real publisher: comparisons made after this
								// point would mistake every policy edit for a no-op.
								c.capacity = proposed.Clone()
							}
							return outcome.err
						})
					}()
					if calls != 1 || err != outcome.err {
						t.Fatalf("publication outcome changed: calls=%d, error=%v", calls, err)
					}
					after := s.limits.Load()
					wantSame := !outcome.published || initialized && transition.preserveIdentity
					if (after == before) != wantSame {
						t.Fatal("wrong limits identity after publication")
					}
					if outcome.published && (after == nil || *after != *selectedLANLimits(proposed)) {
						t.Fatal("published limits do not match the proposed policy")
					}
					if before != nil && *before != *selectedLANLimits(current) {
						t.Fatal("publication mutated the previous limits object")
					}
					if !capacityJSONEqual(proposed, originalProposal) {
						t.Fatal("publication mutated the proposed policy")
					}
				})
			}
		}
	}
}

func TestDirectLANCapacityTransferOnlyReplacesMismatchedLimits(t *testing.T) {
	for _, field := range []string{"peers", "bytes"} {
		t.Run(field, func(t *testing.T) {
			current := capacity.Defaults()
			proposed := current.Clone()
			proposed.Resources["transferConcurrentFiles"] = capacity.Limited(7)
			s := &directLANStore{path: filepath.Join(t.TempDir(), "direct-lan.json"), state: directLANState{Version: directLANStateVersion}}
			before := selectedLANLimits(current)
			if field == "peers" {
				before.peers++
			} else {
				before.bytes++
			}
			s.limits.Store(before)
			c := &Core{capacity: current, directLAN: s}
			calls := 0
			c.mu.Lock()
			err := c.applyDirectLANCapacityLocked(proposed, func() error {
				calls++
				c.capacity = proposed.Clone()
				return nil
			})
			c.mu.Unlock()
			if err != nil || calls != 1 {
				t.Fatal("publication outcome changed", calls, err)
			}
			after := s.limits.Load()
			if after == nil || after == before || *after != *selectedLANLimits(proposed) {
				t.Fatal("transfer-only publication retained mismatched limits")
			}
		})
	}
}
