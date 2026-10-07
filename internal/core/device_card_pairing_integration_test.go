//go:build lanlink_integration

package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/devicecard"
)

// This is Core command composition, not a browser/file-import E2E test. The
// recipient draft below is test-local: production React owns the real draft.
// All subsequent commands use the real Core and TLS-pinned loopback backend.
// The selected endpoint, certificate and policy remain fixed throughout.
func TestLANCoreDeviceCardPairingCompositionIntegration(t *testing.T) {
	f := newNativeLANCoreFixture(t)
	host, guest := f.open(), f.open()
	hostKey := f.must(host, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	guestKey := f.must(guest, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	f.must(host, "network.configure", map[string]any{"mode": "lan", "hostname": "synthetic-card-host", "lan": LANSelection{Kind: "host", Address: f.relayAddress().String()}})
	selection := *host.lanStoreCopy().copy().Selection
	selection.Kind = "relay"
	f.must(guest, "network.configure", map[string]any{"mode": "lan", "hostname": "synthetic-card-guest", "lan": selection})
	for _, c := range []*Core{host, guest} {
		if _, ok := c.nodeCopy().(*lanBackend); !ok {
			t.Fatal("composition requires the real LAN backend")
		}
	}

	// Capture protected state for boolean comparisons only; never emit its
	// contents, identities, endpoint, private invitation or raw command errors.
	type checkpoint struct {
		core    *Core
		node    NetworkBackend
		state   lanState
		profile string
		file    []byte
	}
	capture := func() []checkpoint {
		t.Helper()
		var result []checkpoint
		for _, c := range []*Core{host, guest} {
			store := c.lanStoreCopy()
			data, err := os.ReadFile(store.path)
			if err != nil {
				t.Fatal("could not capture isolated saved LAN state")
			}
			result = append(result, checkpoint{c, c.nodeCopy(), store.copy(), privateRevision(c.profileCopy()), data})
		}
		return result
	}
	assertUnpairedUnchanged := func(before []checkpoint) {
		t.Helper()
		for _, saved := range before {
			c := saved.core
			store := c.lanStoreCopy()
			data, err := os.ReadFile(store.path)
			if err != nil || !bytes.Equal(data, saved.file) || privateRevision(store.copy()) != privateRevision(saved.state) || privateRevision(c.profileCopy()) != saved.profile || c.nodeCopy() != saved.node {
				t.Fatal("read or local draft changed saved transport, identity, policy, trust or backend")
			}
			state, err := c.current(f.ctx)
			if err != nil || !state.Snapshot.Running || len(state.Snapshot.Peers) != 0 || len(store.copy().Trust.Peers) != 0 || len(store.copy().Remotes) != 0 {
				t.Fatal("read or local draft changed listener readiness or established a transport pair")
			}
			for _, key := range []string{hostKey, guestKey} {
				if _, trusted := c.trust(key); trusted {
					t.Fatal("read or local draft granted application trust")
				}
			}
			c.mu.RLock()
			empty := len(c.messages) == 0
			c.mu.RUnlock()
			if !empty {
				t.Fatal("read or local draft sent an application message")
			}
		}
	}

	initial := capture()
	assertUnpairedUnchanged(initial)
	const alias = "Synthetic card recipient / こんにちは"
	exported := f.must(guest, "device-card.export", map[string]any{"mode": "lan", "name": alias, "includeEndpointHint": true}).(DeviceCardExportView)
	if exported.PublicKey != guestKey || exported.Name != alias || exported.Relay == nil || exported.Relay.Address != selection.Address || exported.Relay.CertificateSHA256 != selection.CertificateSHA256 || exported.Verification != "unverified" || exported.Freshness != "unknown" {
		t.Fatal("public card export did not describe the synthetic recipient and unverified hint")
	}
	assertUnpairedUnchanged(initial)
	// Import here means supplying exported text to production card inspection.
	// Browser text/file/PNG ingestion and the UI draft callback are separate tests.
	imported := f.must(host, "device-card.inspect", map[string]any{"card": exported.Text, "expectedMode": "lan"}).(devicecard.Inspection)
	digest := sha256.Sum256([]byte(exported.Text))
	if privateRevision(imported.Card) != privateRevision(exported.Card) || imported.ContentDigest != hex.EncodeToString(digest[:]) || imported.Verification != "unverified" || imported.Freshness != "unknown" {
		t.Fatal("card inspection changed recipient data or implied verified identity")
	}
	assertUnpairedUnchanged(initial)
	// An explicit key/name-only projection models Use recipient in draft. The
	// type cannot carry endpoints, relay pins, permissions or pairing tokens.
	draft := struct {
		PublicKey string `json:"recipientPublicKey"`
		Name      string `json:"name"`
	}{imported.PublicKey, imported.Name}
	requireCardKeys(t, draft, "recipientPublicKey", "name")
	if draft.PublicKey != guestKey || draft.Name != alias {
		t.Fatal("recipient draft does not match the reviewed card exactly")
	}
	assertUnpairedUnchanged(initial)

	// Invitation creation is a separate explicit action, with an explicit TTL.
	issued := f.must(host, "lan.invite", map[string]any{"recipientPublicKey": draft.PublicKey, "name": draft.Name, "ttlSeconds": 300}).(map[string]any)
	if issued["recipientPublicKey"] != draft.PublicKey {
		t.Fatal("invitation targets a different recipient")
	}
	beforeReview := capture()
	assertUnpairedUnchanged(beforeReview)
	review := f.must(guest, "lan.inspect", map[string]any{"invitation": issued["invitation"]}).(map[string]any)
	if review["recipientMatches"] != true || review["recipientPublicKey"] != guestKey || review["hostPublicKey"] != hostKey || review["hostName"] != "synthetic-card-host" || review["relay"] != selection || privateRevision(review["expires"]) != privateRevision(issued["expires"]) {
		t.Fatal("production invitation review does not match the fixed host and recipient")
	}
	assertUnpairedUnchanged(beforeReview)

	joined := f.must(guest, "lan.join", map[string]any{"invitation": issued["invitation"]}).(map[string]any)
	if joined["paired"] != true || joined["trusted"] != false || joined["peerId"] != hostKey {
		t.Fatal("explicit join did not acknowledge an untrusted transport pair")
	}
	for _, pair := range []struct {
		c    *Core
		peer string
	}{{host, guestKey}, {guest, hostKey}} {
		state, err := pair.c.current(f.ctx)
		if err != nil || len(state.Snapshot.Peers) != 1 || state.Snapshot.Peers[0].ID != pair.peer || len(state.Snapshot.Peers[0].IPs) < 2 {
			t.Fatal("explicit pairing did not publish the canonical identity and verified role address")
		}
		if _, trusted := pair.c.trust(pair.peer); trusted {
			t.Fatal("transport pairing implicitly granted application trust")
		}
	}
	pairedRecipient := host.lanStoreCopy().copy().Trust.Peers
	if len(pairedRecipient) != 1 || pairedRecipient[0].Key != draft.PublicKey || pairedRecipient[0].Name != draft.Name {
		t.Fatal("paired recipient key and alias differ from the reviewed draft")
	}
	f.waitPeerAPI(guest, hostKey)
	f.must(host, "peer.trust", map[string]any{"peerId": guestKey, "trusted": true})
	f.must(guest, "peer.trust", map[string]any{"peerId": hostKey, "trusted": true})
	message := f.must(guest, "message.send", map[string]any{"peerId": hostKey, "text": "Synthetic card pairing / こんにちは"}).(Message)
	if message.Status != "sent" {
		t.Fatal("paired host did not acknowledge text receipt")
	}
	host.mu.RLock()
	matched := len(host.messages) == 1 && host.messages[0].ID == message.ID && host.messages[0].PeerID == guestKey && host.messages[0].Text == message.Text
	host.mu.RUnlock()
	if !matched {
		t.Fatal("host did not persist the acknowledged text under the card recipient identity")
	}
	for _, saved := range initial {
		now := saved.core.lanStoreCopy().copy()
		if *now.Selection != *saved.state.Selection || privateRevision(now.Identity) != privateRevision(saved.state.Identity) || privateRevision(now.RelayIdentity) != privateRevision(saved.state.RelayIdentity) || privateRevision(now.DestinationPolicy) != privateRevision(saved.state.DestinationPolicy) {
			t.Fatal("card pairing composition changed the fixed endpoint, identity, certificate or policy")
		}
	}
}
