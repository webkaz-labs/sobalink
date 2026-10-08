package directlan

import (
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"net/netip"
	"testing"
	"time"
)

// Capability-negative tests only: no Node constructor, wire or generation.
func TestManagedEndpointObservationRejectsUnminted(t *testing.T) {
	var nilRequest *ManagedEndpointRequest
	if nilRequest.Current() || nilRequest.Node() != nil || nilRequest.Origin() != nil || nilRequest.Released() != nil || nilRequest.PeerKey() != "" {
		t.Fatal("nil observation granted authority")
	}
	r := &ManagedEndpointRequest{deadline: time.Now().Add(time.Hour)}
	r.live.Store(true)
	if r.Current() {
		t.Fatal("unminted observation admitted")
	}
	r.self = r
	copy := &ManagedEndpointRequest{self: r, deadline: r.deadline}
	copy.live.Store(true)
	if copy.Current() {
		t.Fatal("copied observation admitted")
	}
	r.live.Store(false)
	if r.Current() {
		t.Fatal("retained observation admitted")
	}
}

func TestManagedEndpointCodecDispatchValue(t *testing.T) {
	_, model, remote, now := currentProjectionFixture(t)
	model = currentProjectionSet(t, model, remote, "2", "127.0.0.2:45108", now, false)
	proof := *model.Peers[0].EndpointState.ReceivedProof
	encoded, err := endpointmeta.Encode(proof)
	if err != nil {
		t.Fatal(err)
	}
	request, err := endpointmeta.ParseRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := endpointRequestEnvelope(request)
	if !ok || got != proof {
		t.Fatal("signed codec output bypassed endpoint dispatch")
	}
	if _, ok := endpointRequestEnvelope(&endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: proof.Update.PairBinding}); ok {
		t.Fatal("bound session confused with signed update")
	}
}

// Pointer/data admission only. No generation is started, retired, published or
// connected; this cannot exercise the held runtime replacement scenario.
func TestEndpointDeliveryTargetRejectsChangedAttribution(t *testing.T) {
	n := &Node{}
	g := &runtimeGeneration{n: n}
	n.generation.Store(g)
	peer := Peer{Key: "synthetic", Endpoint: netip.MustParseAddrPort("127.0.0.2:12345")}
	p := &peerState{g: g, peer: peer, binding: "synthetic-binding"}
	a := &managedAuthentication{generation: g, peer: p, binding: p.binding}
	target := &EndpointDelivery{node: n, capture: a, peer: peer, epoch: NewContextEpoch()}
	target.self = target
	if !target.matches(n) {
		t.Fatal("unchanged attribution rejected")
	}
	copy := &EndpointDelivery{self: target, node: n, capture: a, peer: peer, epoch: target.epoch}
	if copy.matches(n) || target.matches(&Node{}) {
		t.Fatal("copied or wrong-node target accepted")
	}
	p.peer.Endpoint = netip.MustParseAddrPort("127.0.0.3:12345")
	if target.matches(n) {
		t.Fatal("reviewed destination changed")
	}
	p.peer = peer
	target.epoch.Invalidate()
	if target.matches(n) {
		t.Fatal("changed receipt epoch accepted")
	}
	target.epoch = NewContextEpoch()
	n.generation.Store(&runtimeGeneration{n: n})
	if target.matches(n) {
		t.Fatal("target rediscovered a later generation")
	}
}

func TestEndpointAdmissionTransientRefusalUsesExistingError(t *testing.T) {
	for _, err := range []error{ErrUnavailable, ErrCapacity} {
		refusal := endpointAdmissionRefusal(err)
		if refusal == nil || refusal.OK || refusal.Operation != "endpoint-update" {
			t.Fatal("transient refusal became acceptance")
		}
		data, encodeErr := endpointmeta.Encode(*refusal)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		parsed, parseErr := endpointmeta.ParseReply(data, "endpoint-update")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if _, ok := parsed.(*endpointmeta.ErrorReply); !ok {
			t.Fatal("refusal became success reply")
		}
	}
	for _, err := range []error{nil, ErrUntrusted, ErrRecovery, endpointmeta.ErrExpired, endpointmeta.ErrPolicy} {
		if endpointAdmissionRefusal(err) != nil {
			t.Fatal("terminal authority mislabeled transient")
		}
	}
}
