package endpointmeta

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestOversizedEncodingRejectsBeforeAllocation(t *testing.T) {
	p, e, _ := fixture(t)
	prefixes := make([]string, MaxFrameBytes)
	for n := range prefixes {
		prefixes[n] = fmt.Sprintf("127.%d.%d.1/32", n/256, n%256)
	}
	sort.Strings(prefixes)
	scope := Scope{Family: "ipv4", Prefixes: prefixes}
	peer := PeerWire{Key: strings.Repeat("a", MaxFrameBytes+1), Endpoint: p.HostEndpoint, TunnelKey: p.HostTunnelKey}
	request := PairRequest{Version: 2, Operation: "pair", Token: p.HostNonce, Peer: PeerWire{p.JoinerKey, "", p.JoinerEndpoint, p.JoinerTunnelKey}, JoinerNonce: p.JoinerNonce, JoinerScope: scope}
	e.Signature = strings.Repeat("a", MaxFrameBytes+1)
	for _, input := range []wireValue{&scope, &peer, &request, &e} {
		allocs := testing.AllocsPerRun(20, func() {
			if _, err := Encode(input); err != ErrCapacity {
				t.Fatal("oversized value", err)
			}
		})
		if allocs != 0 {
			t.Fatalf("oversized %T allocated %g objects before rejection", input, allocs)
		}
	}
	if err := scope.validate(); err != ErrCapacity {
		t.Fatal("direct scope validation omitted count bound", err)
	}
	if allocs := testing.AllocsPerRun(20, func() {
		if validHex(peer.Key) {
			t.Fatal("oversized hex key")
		}
	}); allocs != 0 {
		t.Fatal("hex validation decoded before checking length", allocs)
	}
}

func TestEncodingAtExactFrameBudget(t *testing.T) {
	p, _, _ := fixture(t)
	req := PairRequest{Version: 2, Operation: "pair", Token: p.HostNonce, Peer: PeerWire{p.JoinerKey, "", p.JoinerEndpoint, p.JoinerTunnelKey}, JoinerNonce: p.JoinerNonce, JoinerScope: Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}}
	baseLength := 0
	for n := 0; n < 600; n++ {
		req.JoinerScope.Prefixes = append(req.JoinerScope.Prefixes, fmt.Sprintf("127.%d.%d.1/32", n/256, n%256))
		sort.Strings(req.JoinerScope.Prefixes)
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > MaxFrameBytes-128 && len(raw) <= MaxFrameBytes {
			baseLength = len(raw)
			break
		}
	}
	if baseLength == 0 {
		t.Fatal("could not construct boundary fixture")
	}
	remaining := MaxFrameBytes - baseLength
	for _, name := range []string{strings.Repeat("a", remaining), strings.Repeat("<", remaining/6) + strings.Repeat("a", remaining%6)} {
		req.Peer.Name = name
		encoded, err := Encode(req)
		if err != nil || len(encoded) != MaxFrameBytes {
			t.Fatal("exact budget", len(encoded), err)
		}
		if _, err := ParseRequest(encoded); err != nil {
			t.Fatal("exact budget parse", err)
		}
		req.Peer.Name += "a"
		if _, err := Encode(req); err != ErrCapacity {
			t.Fatal("over budget", err)
		}
	}
}

func TestSizerMatchesEveryWireShape(t *testing.T) {
	p, e, _ := fixture(t)
	binding, _ := p.Binding()
	peer := PeerWire{p.JoinerKey, "<>&\"\\\b\f\n\r\t\x01\u2028\u2029日本語\ufffd", p.JoinerEndpoint, p.JoinerTunnelKey}
	values := []interface{ measure(*wireSizer) bool }{
		Scope{Family: "ipv4"}, p.HostScope, peer, p, e.Update, e,
		Invitation{2, peer, p.HostKey, p.HostNonce, e.Update.Expires, p.JoinerNonce, p.JoinerScope},
		PairRequest{2, "pair", p.HostNonce, peer, p.JoinerNonce, p.JoinerScope},
		PrepareRequest{2, "pair-context-prepare", p.HostKey, p.JoinerKey, p.HostTunnelKey, p.JoinerTunnelKey, p.HostNonce, p.HostEndpoint, p.JoinerEndpoint, p.HostScope},
		BoundRequest{2, "session", binding}, PairReply{2, "pair", true, peer, p, binding},
		PrepareReply{2, "pair-context-prepare", true, p, binding}, ContextReply{2, "pair-context-status", true, binding, "prepared"},
		SessionReply{2, "session", true, binding}, UpdateReply{2, "endpoint-update", true, binding, "7", mustDigest(t, e), "review_required"},
		ErrorReply{2, "pair", false, "pair_rejected"}, ErrorReply{-123, "unknown", false, "unsupported_operation"},
	}
	for _, v := range values {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		w := wireSizer{left: MaxFrameBytes}
		if !v.measure(&w) || MaxFrameBytes-w.left != len(raw) {
			t.Fatalf("%T size: got %d, want %d", v, MaxFrameBytes-w.left, len(raw))
		}
	}
}
