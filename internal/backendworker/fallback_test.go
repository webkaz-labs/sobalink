package backendworker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

type scopedTestEngine struct{ pipeEngine }

func (*scopedTestEngine) RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	return func() {}, nil
}
func (*scopedTestEngine) ValidateTCPScopes(context.Context, []TCPPolicy) error { return nil }
func TestFallbackScopeReductionClosesAndRejectsLateStreams(t *testing.T) {
	h := &engineHost{engine: &scopedTestEngine{}, handles: map[uint64]io.Closer{}, limits: DefaultLimits()}
	defer h.close()
	ctx := context.Background()
	call := func(method string, q engineRequest) engineResponse {
		t.Helper()
		raw, _ := json.Marshal(q)
		result, e := h.handle(ctx, method, raw)
		if e != nil {
			t.Fatal(e)
		}
		var out engineResponse
		if e = json.Unmarshal(result, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	call("fallback-enable", engineRequest{})
	src := netip.MustParseAddrPort("100.64.0.2:32000")
	dst := netip.MustParseAddrPort("100.64.0.1:4567")
	if _, ok := h.fallback.selector(src, dst); ok {
		t.Fatal("empty grant accepted a port")
	}
	call("fallback-scopes", engineRequest{Scopes: []TCPPolicy{{Address: dst.Addr(), Ports: [][2]uint16{{4567, 4567}}, Peers: []netip.Addr{src.Addr()}, UntilRevoked: true}}})
	handler, ok := h.fallback.selector(src, dst)
	if !ok {
		t.Fatal("exact permission missing")
	}
	if _, ok = h.fallback.selector(src, netip.AddrPortFrom(dst.Addr(), 4568)); ok {
		t.Fatal("ungranted port accepted")
	}
	local, remote := net.Pipe()
	defer remote.Close()
	handler(local)
	out := call("fallback-accept", engineRequest{})
	if out.Handle == 0 {
		t.Fatal("accepted stream has no owned handle")
	}
	call("fallback-scopes", engineRequest{})
	if _, e := remote.Write([]byte{1}); e == nil {
		t.Fatal("old child stream survived reduction acknowledgement")
	}
	late, other := net.Pipe()
	defer other.Close()
	handler(late)
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	raw, _ := json.Marshal(engineRequest{})
	if _, e := h.handle(short, "fallback-accept", raw); e == nil {
		t.Fatal("late old-generation accept survived")
	}
	if _, e := other.Read(make([]byte, 1)); e != io.EOF {
		t.Fatal("late child socket retained", e)
	}
}
func TestWorkerFrameBudgetIsNotStreamLimit(t *testing.T) {
	if e := (Limits{FrameBytes: 128 << 10, Requests: 8, Handles: 1}).Validate(); e != nil {
		t.Fatal(e)
	}
	if e := (Limits{FrameBytes: 8192, Requests: 8, Handles: 1}).Validate(); e == nil {
		t.Fatal("frame unable to preserve one full datagram accepted")
	}
}
