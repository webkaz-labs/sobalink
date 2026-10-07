//go:build lanlink_integration

package core

import (
	"bytes"
	"os"
	"testing"
)

// This proves guarded startup of a stopped saved participant-host using the
// real TLS-pinned loopback backend, ordinary pairing and an acknowledged text
// message. The saved endpoint, certificate and policy stay fixed throughout.
func TestLANCoreSavedHostStartIntegration(t *testing.T) {
	f := newNativeLANCoreFixture(t)
	host, guest := f.open(), f.open()
	f.must(host, "network.configure", map[string]any{"mode": "none", "hostname": "native-saved-host"})
	// Save a host without constructing or starting a network backend.
	if err := host.configureLAN(&LANSelection{Kind: "host", Address: f.relayAddress().String()}); err != nil {
		t.Fatal("could not save isolated host settings")
	}
	store := host.lanStoreCopy()
	saved := store.copy()
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal("could not read saved host settings")
	}
	loaded, err := readLANStore(store.path)
	if err != nil || loaded == nil || privateRevision(loaded.copy()) != privateRevision(saved) {
		t.Fatal("saved host settings differ from durable state")
	}
	policy, err := saved.DestinationPolicy.Canonical()
	if err != nil {
		t.Fatal("saved host destination policy is invalid")
	}
	snapshot, err := host.Snapshot(f.ctx)
	if err != nil {
		t.Fatal("could not review saved host")
	}
	status := snapshot["lan"].(map[string]any)
	review, ok := status["savedStart"].(*LANStartReview)
	if !ok || review == nil || len(review.Revision) != 64 || review.Relay != *saved.Selection || review.PublicKey != saved.Identity.PublicKey() || privateRevision(review.Policy) != privateRevision(policy) {
		t.Fatal("review does not describe the saved host scope")
	}
	afterReview, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(before, afterReview) || host.nodeCopy() != nil || status["listenerReady"] != false || status["relayReady"] != false {
		t.Fatal("review changed saved settings or started the host")
	}
	f.must(host, "network.configure", map[string]any{"mode": "lan", "expectedLANStartRevision": review.Revision})
	if _, ok := host.nodeCopy().(*lanBackend); !ok {
		t.Fatal("guarded saved-host start did not construct the real LAN backend")
	}
	if privateRevision(store.copy()) != privateRevision(saved) {
		t.Fatal("guarded saved-host start changed saved LAN settings")
	}
	status = host.lanStatus()
	if status["savedStart"] != nil || status["listenerReady"] != true || status["relayReady"] != true {
		t.Fatal("guarded saved-host start did not publish listener readiness")
	}
	hostKey := review.PublicKey
	guestKey := f.must(guest, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	selection := review.Relay
	selection.Kind = "relay"
	f.must(guest, "network.configure", map[string]any{"mode": "lan", "hostname": "native-guest", "lan": selection})
	issued := f.must(host, "lan.invite", map[string]any{"recipientPublicKey": guestKey, "name": "native-guest", "ttlSeconds": 120}).(map[string]any)
	f.must(guest, "lan.join", map[string]any{"invitation": issued["invitation"]})
	for _, pair := range []struct {
		c    *Core
		peer string
	}{{host, guestKey}, {guest, hostKey}} {
		state, err := pair.c.current(f.ctx)
		if err != nil || len(state.Snapshot.Peers) != 1 || state.Snapshot.Peers[0].ID != pair.peer || len(state.Snapshot.Peers[0].IPs) < 2 {
			t.Fatal("saved-host pairing did not publish the canonical identity and verified role address")
		}
		if _, trusted := pair.c.trust(pair.peer); trusted {
			t.Fatal("saved-host transport pairing implicitly granted application trust")
		}
	}
	f.waitPeerAPI(guest, hostKey)
	f.must(host, "peer.trust", map[string]any{"peerId": guestKey, "trusted": true})
	f.must(guest, "peer.trust", map[string]any{"peerId": hostKey, "trusted": true})
	message := f.must(guest, "message.send", map[string]any{"peerId": hostKey, "text": "Saved host / こんにちは"}).(Message)
	if message.Status != "sent" {
		t.Fatal("saved host did not acknowledge text receipt")
	}
	host.mu.RLock()
	matched := len(host.messages) == 1 && host.messages[0].ID == message.ID && host.messages[0].PeerID == guestKey && host.messages[0].Text == message.Text
	host.mu.RUnlock()
	if !matched {
		t.Fatal("saved host did not persist the acknowledged text under canonical identity")
	}
	current := store.copy()
	if *current.Selection != *saved.Selection || privateRevision(current.Identity) != privateRevision(saved.Identity) || privateRevision(current.RelayIdentity) != privateRevision(saved.RelayIdentity) || privateRevision(current.DestinationPolicy) != privateRevision(saved.DestinationPolicy) {
		t.Fatal("saved-host application use changed the endpoint, identity, certificate or policy")
	}
}
