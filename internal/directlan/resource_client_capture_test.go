package directlan

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/tailscale/wireguard-go/device"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// In-memory ownership only: no Node constructor, engine, listener, endpoint,
// supervisor or native transport is created. Accidental dialing has no backend.
func resourceCaptureFixture(t *testing.T) (*Node, *runtimeGeneration, *peerState, *ResourcePeerCapture, resourcegrant.Relationship) {
	t.Helper()
	cfg, _ := managedFixtureConfig()
	n := &Node{cfg: cfg, started: true, ctx: context.Background(), peers: map[string]*peerState{}}
	bind := &lanBind{}
	g := newRuntimeGeneration(n, bind, nil)
	bind.owner = g
	peer := &peerState{peer: cfg.Peers[0], session: newPeerSession(true), g: g}
	peer.binding, _ = cfg.PairContexts[peer.peer.Key].Binding()
	peer.session.registration.Store(7)
	address, _ := OverlayAddress(peer.peer.Key)
	policy := &bindPolicy{generations: map[netip.Addr]*peerState{address: peer}}
	bind.policy.Store(policy)
	g.peers[peer.peer.Key], n.peers[peer.peer.Key] = peer, peer
	g.peerRegistrations[device.PeerRegistration(7)] = peer
	g.traffic.Store(true)
	n.generation.Store(g)
	expected := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: peer.peer.Key, PeerKey: cfg.Identity.PublicKey(), PairBinding: peer.binding}
	selected := &PeerCapability{origin: g.origin, registration: 7}
	captured, err := n.captureResourcePeerSelected(selected, expected, strings.Repeat("a", 64))
	if err != nil || !n.ResourcePeerCurrent(captured, expected) {
		t.Fatal("inert exact capture failed", err)
	}
	return n, g, peer, captured, expected
}

func TestResourcePeerCaptureDetachedAndIndependentEpoch(t *testing.T) {
	n, _, peer, captured, expected := resourceCaptureFixture(t)
	before := peer.managementEpoch
	again, err := n.captureResourcePeerSelected(captured.peer, expected, strings.Repeat("b", 64))
	if err != nil || again.epoch != captured.epoch || peer.managementEpoch != before {
		t.Fatal("stable client capture changed or coupled inbound epoch")
	}
	data, err := json.Marshal(captured)
	if err != nil || string(data) != "{}" {
		t.Fatal("private capture exported authority or origin data")
	}
	if _, err := n.captureResourcePeerSelected(captured.peer, expected, ""); err == nil {
		t.Fatal("failed random candidate reused stable capture")
	}
}

func TestResourcePeerCaptureRejectsChangedTuple(t *testing.T) {
	for _, change := range []string{"node", "generation", "peer", "registration", "policy", "binding", "registry", "sealed", "detached", "relationship"} {
		t.Run(change, func(t *testing.T) {
			n, g, peer, captured, expected := resourceCaptureFixture(t)
			switch change {
			case "node":
				n = &Node{}
			case "generation":
				n.generation.Store(nil)
			case "peer":
				n.peers[peer.peer.Key] = &peerState{}
			case "registration":
				peer.session.registration.Store(8)
			case "policy":
				address, _ := OverlayAddress(peer.peer.Key)
				g.bind.policy.Store(&bindPolicy{generations: map[netip.Addr]*peerState{address: peer}})
			case "binding":
				peer.binding = strings.Repeat("d", 64)
			case "registry":
				delete(g.peerRegistrations, 7)
			case "sealed":
				g.seal(ErrUnavailable)
			case "detached":
				g.origin.detach()
			case "relationship":
				expected.PairBinding = strings.Repeat("e", 64)
			}
			if n.ResourcePeerCurrent(captured, expected) {
				t.Fatal("original capture accepted replacement", change)
			}
		})
	}
}

func TestResourcePeerCaptureRejectsReplacementDigestReuse(t *testing.T) {
	n, g, peer, old, expected := resourceCaptureFixture(t)
	address, _ := OverlayAddress(peer.peer.Key)
	g.bind.policy.Store(&bindPolicy{generations: map[netip.Addr]*peerState{address: peer}})
	if _, err := n.captureResourcePeerSelected(old.peer, expected, old.epoch); err == nil {
		t.Fatal("new policy reused old digest")
	}
	if n.ResourcePeerCurrent(old, expected) {
		t.Fatal("rejected replacement refreshed old capture")
	}
	next, err := n.captureResourcePeerSelected(old.peer, expected, strings.Repeat("b", 64))
	if err != nil || next.epoch == old.epoch || !n.ResourcePeerCurrent(next, expected) || n.ResourcePeerCurrent(old, expected) {
		t.Fatal("new correlation revived old authority")
	}
}

func TestResourcePeerCapturedCallsRejectBeforeDial(t *testing.T) {
	for _, change := range []string{"cancel", "wrong_node", "policy", "registration", "relationship", "nil_capture", "nil_peer", "detached", "invalid_epoch"} {
		t.Run(change, func(t *testing.T) {
			n, g, peer, captured, expected := resourceCaptureFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "cancel":
				cancel()
			case "wrong_node":
				n = &Node{}
			case "policy":
				address, _ := OverlayAddress(peer.peer.Key)
				g.bind.policy.Store(&bindPolicy{generations: map[netip.Addr]*peerState{address: peer}})
			case "registration":
				peer.session.registration.Store(8)
			case "relationship":
				expected.PairBinding = strings.Repeat("f", 64)
			case "nil_capture":
				captured = nil
			case "nil_peer":
				captured.peer = nil
			case "detached":
				g.origin.detach()
			case "invalid_epoch":
				captured.authorityEpoch = NewContextEpoch()
				captured.authorityEpoch.Invalidate()
			}
			request := managementTestRequest(resourcegrant.ApplyAction)
			if _, err := n.ManageRemoteCaptured(ctx, captured, expected, request); err == nil {
				t.Fatal("stale apply passed pre-dial gate")
			}
			legacy := resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: request.Target, GrantID: request.GrantID, GrantRevision: request.GrantRevision}
			if _, err := n.InspectRemoteCaptured(ctx, captured, expected, legacy); err == nil {
				t.Fatal("stale legacy inspection passed pre-dial gate")
			}
			if len(g.work) != 0 || g.origin.calls != 0 {
				t.Fatal("failed exchange retained work or origin borrow")
			}
		})
	}
}

func TestResourcePeerCaptureOriginalEpochCannotReviveOnSameTuple(t *testing.T) {
	n, g, peer, captured, expected := resourceCaptureFixture(t)
	original := NewContextEpoch()
	captured.authorityEpoch = original
	identity := g.sessionIdentity(peer)
	if !n.ResourcePeerCurrent(captured, expected) {
		t.Fatal("exact live epoch rejected")
	}
	original.Invalidate()
	replacement := NewContextEpoch()
	if !replacement.Valid() || n.ResourcePeerCurrent(captured, expected) {
		t.Fatal("replacement epoch revived old capture")
	}
	n.mu.Lock()
	admitted := n.resourcePeerCaptureCurrentLocked(captured, g, peer, identity)
	n.mu.Unlock()
	if admitted {
		t.Fatal("pre-frame/final tuple gate omitted original epoch")
	}
	// The new entry cannot fall back to the ordinary capture when no live
	// original epoch was supplied; the empty Node has no transport to use.
	for _, epoch := range []*ContextEpoch{nil, {}, original} {
		if _, err := (&Node{}).CaptureResourcePeerWithEpoch(expected, epoch); err == nil {
			t.Fatal("missing or dead epoch recaptured")
		}
	}
}

func TestResourcePeerCapturedAdmissionChecksSuppliedAuthentication(t *testing.T) {
	n, g, peer, captured, _ := resourceCaptureFixture(t)
	identity := g.sessionIdentity(peer)
	n.mu.Lock()
	valid := n.resourcePeerCaptureCurrentLocked(captured, g, peer, identity)
	changed := *identity
	changed.policy = &bindPolicy{}
	wrongPolicy := n.resourcePeerCaptureCurrentLocked(captured, g, peer, &changed)
	wrongEpoch := *captured
	wrongEpoch.epoch = strings.Repeat("c", 64)
	wrongCorrelation := n.resourcePeerCaptureCurrentLocked(&wrongEpoch, g, peer, identity)
	n.mu.Unlock()
	if !valid || wrongPolicy || wrongCorrelation {
		t.Fatal("pre-frame/final leaf gate substituted current tuple or epoch")
	}
}
