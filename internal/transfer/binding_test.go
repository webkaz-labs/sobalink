package transfer

import (
	"errors"
	"testing"
)

func TestValidatePeerBindingChecksCapacityAndGenerationWithoutMutation(t *testing.T) {
	m, err := NewManager(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	peer := Peer{ID: "peer-a", Generation: 4}
	if err := m.ValidatePeerBinding(peer); err != nil || len(m.peers) != 0 {
		t.Fatalf("preflight mutated binding state: %v", err)
	}
	if err := m.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	if err := m.RevokePeer(peer.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ValidatePeerBinding(peer); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("revoked generation accepted: %v", err)
	}
	peer.Generation++
	if err := m.ValidatePeerBinding(peer); err != nil {
		t.Fatal(err)
	}
	if !m.peers[peer.ID].revoked || m.peers[peer.ID].peer.Generation != 4 {
		t.Fatal("generation preflight reactivated receiver binding")
	}
	m.limits.MaxPeers = 1
	other := Peer{ID: "peer-b", Generation: 1}
	if err := m.ValidatePeerBinding(other); !errors.Is(err, ErrLimit) {
		t.Fatalf("capacity preflight accepted new identity: %v", err)
	}
	if len(m.peers) != 1 {
		t.Fatal("capacity preflight changed bindings")
	}
}

func TestResetBindingsValidatesWholeScopeAndRefusesExistingBatches(t *testing.T) {
	m, peer := testManager(t, Options{})
	destination := t.TempDir()
	policy := ReceivePolicy{Peer: peer, Destination: destination, AutoAccept: true}
	if err := m.SetReceivePolicy(policy); err != nil {
		t.Fatal(err)
	}
	other := Peer{ID: "other", Generation: 2}
	if err := m.ResetBindings([]Peer{other}, []ReceivePolicy{policy}); !errors.Is(err, ErrPeerChanged) {
		t.Fatalf("unmatched replacement policy accepted: %v", err)
	}
	if _, ok := m.peers[peer.ID]; !ok || len(m.policies) != 1 {
		t.Fatal("invalid replacement changed existing scope")
	}
	if err := m.ResetBindings([]Peer{other}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.policies) != 0 || m.peers[peer.ID] != nil || m.peers[other.ID] == nil {
		t.Fatal("old scope survived reset")
	}
	if _, err := m.Offer(other, testManifest("pending", testEntry("file", "file.txt", "payload"))); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetBindings(nil, nil); !errors.Is(err, ErrState) {
		t.Fatalf("reset discarded a pending batch: %v", err)
	}
	if m.peers[other.ID] == nil || len(m.batches) != 1 {
		t.Fatal("failed reset changed active scope")
	}
}
