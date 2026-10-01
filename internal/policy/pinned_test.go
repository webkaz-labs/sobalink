package policy

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestRulePinsIdentityAcrossNewDials(t *testing.T) {
	p, s := fixture()
	p.Rules[0].PeerID = "node1"
	if e := p.Validate(t.Context(), "tcp", "server:21116"); e != nil {
		t.Fatal(e)
	}
	s.Peers[0].ID = "replacement"
	if e := p.Validate(t.Context(), "tcp", "server:21116"); e == nil {
		t.Fatal("same name silently changed identity")
	}
}
func TestLifetimeGuardClosesTCPBeforeAndAfterRead(t *testing.T) {
	p, _ := fixture()
	blocked := false
	p.Guard = func() error {
		if blocked {
			return errors.New("expired")
		}
		return nil
	}
	if e := p.Validate(t.Context(), "tcp", "server:21116"); e != nil {
		t.Fatal(e)
	}
	blocked = true
	if e := p.Validate(t.Context(), "tcp", "server:21116"); e == nil {
		t.Fatal("guard ignored")
	}
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := &liveConn{Conn: a, policy: p, network: "tcp", ctx: ctx, cancel: cancel}
	if n, e := c.Write([]byte("x")); n != 0 || e == nil {
		t.Fatal(n, e)
	}
	if ctx.Err() == nil {
		t.Fatal("guard did not close socket")
	}
}

func TestObservedPinRevocationNotifiesButOutageDoesNot(t *testing.T) {
	p, s := fixture()
	p.Rules[0].PeerID = "node1"
	calls := 0
	p.OnRevoked = func() { calls++ }
	s.Running = false
	p.Validate(t.Context(), "tcp", "server:21116")
	if calls != 0 {
		t.Fatal("common outage latched revocation")
	}
	s.Running = true
	s.Peers = nil
	if e := p.Validate(t.Context(), "tcp", "server:21116"); e == nil || calls != 1 {
		t.Fatal("observed revocation not reported", e, calls)
	}
}
