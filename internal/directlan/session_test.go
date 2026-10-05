package directlan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

func TestSessionReadinessRequiresResponderConfirmation(t *testing.T) {
	for _, initiator := range []bool{false, true} {
		s := newPeerSession(initiator)
		if s.ready() {
			t.Fatal("fresh session ready")
		}
		s.transition(device.PeerSessionEstablished)
		if s.ready() != initiator {
			t.Fatal("derived responder key treated as confirmed")
		}
		s.confirm()
		if !s.ready() {
			t.Fatal("authenticated confirmation not observed")
		}
		for _, state := range []device.PeerSessionState{device.PeerSessionExpired, device.PeerSessionNone, device.PeerSessionHandshake} {
			s.transition(state)
			s.confirm()
			if s.ready() {
				t.Fatal("confirmation resurrected non-established session")
			}
			s.transition(device.PeerSessionEstablished)
			if s.ready() != initiator {
				t.Fatal("new session inherited old confirmation")
			}
		}
	}
}
func TestSessionRoleGenerationAndControlBudgets(t *testing.T) {
	a, b := memoryNode(t, 61), memoryNode(t, 62)
	if a.PublicKey() > b.PublicKey() {
		a, b = b, a
	}
	peer := Peer{Key: b.PublicKey(), Endpoint: b.Endpoint(), TunnelKey: b.cfg.Identity.TunnelKey()}
	old := a.newPeerState(peer)
	a.peers[peer.Key] = old
	if !old.session.initiator || b.newPeerState(Peer{Key: a.PublicKey()}).session.initiator {
		t.Fatal("roles disagree")
	}
	a.peers[peer.Key] = a.newPeerState(peer)
	old.session.transition(device.PeerSessionEstablished)
	if e := a.initiateSession(old); !errors.Is(e, ErrUntrusted) {
		t.Fatal("stale generation", e)
	}
	if e := a.ensureSession(context.Background(), old); !errors.Is(e, ErrUntrusted) {
		t.Fatal("stale ready generation", e)
	}
	wrong := b.newPeerState(Peer{Key: a.PublicKey()})
	b.peers[a.PublicKey()] = wrong
	if e := b.initiateSession(wrong); !errors.Is(e, ErrUntrusted) {
		t.Fatal("wrong initiation role", e)
	}
	a.dials[&pendingDial{}] = struct{}{}
	a.dials[&pendingDial{control: true}] = struct{}{}
	if a.flowUsageLocked() != 1 || a.controlUsageLocked() != 1 {
		t.Fatal("control/data budgets overlap")
	}
	c := testConfig(1)
	c.InvitationLimit = 3
	if c.withDefaults().ControlLimit != 3 {
		t.Fatal("control default not tied to selected invitation budget")
	}
	c.ControlLimit = -1
	if !errors.Is(c.Validate(), ErrCapacity) {
		t.Fatal("negative control budget")
	}
	// The synthetic pending entries have no cancellation callbacks.
	clear(a.dials)
}
func TestSessionWaitCancellationAndRevocation(t *testing.T) {
	n := memoryNode(t, 64)
	peer := Peer{Key: testIdentity(65).PublicKey(), Endpoint: n.Endpoint(), TunnelKey: testIdentity(65).TunnelKey()}
	p := n.newPeerState(peer)
	n.peers[peer.Key] = p
	p.session.gate <- struct{}{} // Hold activation before any socket operation.
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, e := n.DialPeer(ctx, peer.Key, "tcp", 42000); result <- e }()
	deadline := time.After(time.Second)
	for {
		n.mu.Lock()
		pending := len(n.dials)
		n.mu.Unlock()
		if pending > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("dial did not enter session gate")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if e := n.Revoke(peer.Key); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel waiting dial")
	}
	cancel()
	<-p.session.gate
	canceled, stop := context.WithCancel(context.Background())
	stop()
	for _, initiator := range []bool{false, true} {
		fresh := n.newPeerState(peer)
		fresh.session.initiator = initiator
		n.mu.Lock()
		n.peers[peer.Key] = fresh
		n.mu.Unlock()
		if e := n.ensureSession(canceled, fresh); !errors.Is(e, context.Canceled) {
			t.Fatal("cancelled activation", initiator, e)
		}
	}
}
