package app

import (
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"io"
	"net"
	"testing"
	"time"
)

func TestObservedShareRevocationRequiresExplicitRestart(t *testing.T) {
	s, n := ruleFixture(t)
	node := &inboundFake{fakeNode: n}
	s.Node = node
	service, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		c, e := service.Accept()
		if e == nil {
			defer c.Close()
			accepted <- struct{}{}
			io.Copy(c, c)
		}
	}()
	r := config.Rule{Name: "api", Purpose: "custom", Direction: "share", Network: "tcp", ListenPort: 8000, TargetHost: "127.0.0.1", TargetPort: service.Addr().(*net.TCPAddr).Port, AllowedPeers: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}}
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{"api"}, TTLSeconds: 60})
	n.mu.Lock()
	original := n.state.Snapshot
	n.state.Snapshot = policy.Snapshot{Running: true}
	n.mu.Unlock()
	denied, e := net.Dial("tcp", node.bound.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	denied.SetReadDeadline(time.Now().Add(15 * time.Second))
	b := make([]byte, 1)
	_, e = denied.Read(b)
	denied.Close()
	if e == nil {
		t.Fatal("not revoked")
	}
	n.mu.Lock()
	n.state.Snapshot = original
	n.mu.Unlock()
	resumed, e := net.Dial("tcp", node.bound.Addr().String())
	if e != nil {
		s.check(t.Context())
		if s.rules.entries["api"].status.State != "failed" {
			t.Fatal("revocation was not terminal")
		}
		return
	}
	defer resumed.Close()
	resumed.SetDeadline(time.Now().Add(15 * time.Second))
	resumed.Write([]byte("x"))
	if n, e := resumed.Read(b); n > 0 || e == nil {
		t.Fatal("observed identity revocation resumed without explicit restart")
	}
}
