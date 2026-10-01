package policy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func fixture() (*Policy, *Snapshot) {
	s := &Snapshot{Running: true, Peers: []Peer{{ID: "node1", DNSName: "server.example.ts.net.", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}}, {ID: "node2", DNSName: "other.example.ts.net.", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}}}}
	p := &Policy{Rules: []Rule{{Host: "server.example.ts.net", Port: 21116, Network: "tcp"}}, Source: func(context.Context) (Snapshot, error) { return *s, nil }}
	return p, s
}
func TestAllowlistResolvesOnlyPeer(t *testing.T) {
	p, _ := fixture()
	for _, h := range []string{"server:21116", "SERVER.EXAMPLE.TS.NET.:21116", "100.64.1.2:21116"} {
		a, e := p.Resolve(context.Background(), "tcp", h)
		if e != nil || a.String() != "100.64.1.2:21116" {
			t.Fatal(h, a, e)
		}
	}
	for _, h := range []string{"other:21116", "server:80", "127.0.0.1:21116", "100.100.100.100:21116", "example.com:21116", "server:0", "server:65536", "192.168.1.1:21116"} {
		if _, e := p.Resolve(context.Background(), "tcp", h); e == nil {
			t.Errorf("accepted %s", h)
		}
	}
	if _, e := p.Resolve(context.Background(), "udp", "server:21116"); e == nil {
		t.Fatal("allowed UDP")
	}
}
func TestRevocationAndAmbiguity(t *testing.T) {
	p, s := fixture()
	s.Peers[0].Expired = true
	if e := p.Validate(context.Background(), "tcp", "server:21116"); e == nil {
		t.Fatal("expired")
	}
	s.Peers[0].Expired = false
	s.Peers = append(s.Peers, Peer{ID: "other", DNSName: "server.second.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.2.3")}})
	if e := p.Validate(context.Background(), "tcp", "server:21116"); e == nil {
		t.Fatal("ambiguous short name")
	}
	s.Running = false
	if e := p.Validate(context.Background(), "tcp", "server.example.ts.net:21116"); e == nil {
		t.Fatal("offline")
	}
}
func TestDialGetsIPAndNoFallback(t *testing.T) {
	p, s := fixture()
	calls := 0
	p.DialIP = func(ctx context.Context, n string, a netip.AddrPort) (net.Conn, error) {
		calls++
		s.Peers = nil
		if a.String() != "100.64.1.2:21116" {
			t.Fatal(a)
		}
		return nil, errors.New("peer removed")
	}
	if _, e := p.Dial(context.Background(), "tcp", "server:21116"); e == nil {
		t.Fatal("error lost")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	p.Dial(context.Background(), "tcp", "server:21116")
	if calls != 1 {
		t.Fatal("dial after removal")
	}
}
func TestIPMustBelongToAllowedPeer(t *testing.T) {
	p, s := fixture()
	s.Peers[0].IPs = []netip.Addr{netip.MustParseAddr("8.8.8.8")}
	if e := p.Validate(context.Background(), "tcp", "server:21116"); e == nil {
		t.Fatal("public IP")
	}
}
