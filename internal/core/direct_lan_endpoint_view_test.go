package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Pure presentation only: no Core, store, constructor, signatures or transport.
func TestEndpointPeerViewSeparatesSignedProposal(t *testing.T) {
	proof := endpointmeta.Envelope{Update: endpointmeta.UpdateBody{Endpoint: "127.0.0.2:48444", Operation: "set"}}
	model := endpointmeta.Snapshot{LocalScope: endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}, Peers: []endpointmeta.PeerRecord{{Peer: endpointmeta.PeerWire{Key: "synthetic-peer", Endpoint: "127.0.0.1:48444"}, PairContext: &endpointmeta.PairContext{}, EndpointState: &endpointmeta.EndpointState{ReceivedProof: &proof, ReceiveStatus: "expired"}}}}
	views := endpointPeerViews(model)
	if len(views) != 1 || views[0]["endpoint"] != "127.0.0.1:48444" || views[0]["signedEndpoint"] != "127.0.0.2:48444" {
		t.Fatal(views)
	}
	digest, _ := model.LocalScope.Digest()
	if views[0]["scopeDigest"] != digest {
		t.Fatal(views)
	}
	raw, _ := json.Marshal(views)
	for _, key := range []string{`"update"`, `"signature"`, `"seed"`} {
		if strings.Contains(string(raw), key) {
			t.Fatal("view exposed private proof material")
		}
	}
	if model.Peers[0].Peer.Endpoint != "127.0.0.1:48444" {
		t.Fatal("presentation mutated route")
	}
}
