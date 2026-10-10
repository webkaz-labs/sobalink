package directlan

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// ResourcePeerCapture narrows a later typed resource exchange to one previously
// selected managed tuple. It grants no permission and is never serialized. Like
// PeerCapability, it retains a detachable origin, not a raw Node/generation graph.
type ResourcePeerCapture struct {
	peer           *PeerCapability
	epoch          string
	relationship   resourcegrant.Relationship
	authorityEpoch *ContextEpoch // inert terminal invalidation only; never authentication
}

// CaptureResourcePeerWithEpoch additionally pins the caller's original managed
// publication epoch. A replacement epoch can never rehabilitate this capture.
func (n *Node) CaptureResourcePeerWithEpoch(expected resourcegrant.Relationship, epoch *ContextEpoch) (*ResourcePeerCapture, error) {
	if epoch == nil || !epoch.Valid() {
		return nil, ErrUntrusted
	}
	captured, err := n.CaptureResourcePeer(expected)
	if err != nil {
		return nil, err
	}
	captured.authorityEpoch = epoch
	if !n.ResourcePeerCurrent(captured, expected) {
		return nil, ErrUntrusted
	}
	return captured, nil
}

// CaptureResourcePeer performs no exchange. Randomness is obtained outside all
// transport locks; failure cannot silently reuse an earlier epoch.
func (n *Node) CaptureResourcePeer(expected resourcegrant.Relationship) (*ResourcePeerCapture, error) {
	if n == nil || expected.Validate() != nil {
		return nil, ErrUntrusted
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, ErrUnavailable
	}
	selected, err := n.CapturePeer(expected.TargetKey)
	if err != nil {
		return nil, err
	}
	return n.captureResourcePeerSelected(selected, expected, hex.EncodeToString(random[:]))
}

func (n *Node) captureResourcePeerSelected(selected *PeerCapability, expected resourcegrant.Relationship, candidate string) (*ResourcePeerCapture, error) {
	if n == nil || selected == nil || selected.origin == nil || expected.Validate() != nil || !resource.ValidDigest(candidate) {
		return nil, ErrUntrusted
	}
	g, err := selected.origin.capture()
	if err != nil {
		return nil, ErrUntrusted
	}
	defer selected.origin.callDone()
	if g.n != n {
		return nil, ErrUntrusted
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	g.mu.Lock()
	peer := g.peerRegistrations[selected.registration]
	g.mu.Unlock()
	identity := g.sessionIdentity(peer)
	if !n.resourcePeerSelectionCurrentLocked(selected, expected, g, peer, identity) {
		return nil, ErrUntrusted
	}
	slot, ok := selectManagementEpoch(peer.resourceClientEpoch, *identity, candidate)
	if !ok {
		return nil, ErrUntrusted
	}
	peer.resourceClientEpoch = slot
	return &ResourcePeerCapture{peer: selected, epoch: slot.digest, relationship: expected}, nil
}

// ResourcePeerCurrent rechecks the original capture only. It cannot refresh a
// stale slot, recapture a replacement peer or turn an epoch into authority.
func (n *Node) ResourcePeerCurrent(captured *ResourcePeerCapture, expected resourcegrant.Relationship) bool {
	if n == nil || captured == nil || captured.peer == nil || captured.peer.origin == nil || captured.relationship != expected {
		return false
	}
	g, err := captured.peer.origin.capture()
	if err != nil {
		return false
	}
	defer captured.peer.origin.callDone()
	if g.n != n {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	g.mu.Lock()
	peer := g.peerRegistrations[captured.peer.registration]
	g.mu.Unlock()
	return n.resourcePeerCaptureCurrentLocked(captured, g, peer, g.sessionIdentity(peer))
}

// Caller holds Node.mu. The registration registry remains generation-owned;
// no stale caller-provided peer or tuple is replaced by a current lookup.
func (n *Node) resourcePeerSelectionCurrentLocked(selected *PeerCapability, expected resourcegrant.Relationship, g *runtimeGeneration, peer *peerState, identity *managedAuthentication) bool {
	if selected == nil || selected.origin == nil || selected.registration == 0 || g == nil || g.n != n || g.origin != selected.origin || n.generation.Load() != g || !g.open() || n.readyLocked() != nil || peer == nil || n.peers[peer.peer.Key] != peer || peer.g != g || identity == nil || identity.peer != peer || identity.generation != g || identity.registration != uint64(selected.registration) || !identity.current() {
		return false
	}
	g.mu.Lock()
	exact := g.peerRegistrations[selected.registration] == peer
	g.mu.Unlock()
	return exact && expected == (resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: peer.peer.Key, PeerKey: g.cfg.Identity.PublicKey(), PairBinding: identity.binding})
}

func (n *Node) resourcePeerCaptureCurrentLocked(captured *ResourcePeerCapture, g *runtimeGeneration, peer *peerState, identity *managedAuthentication) bool {
	return captured != nil && (captured.authorityEpoch == nil || captured.authorityEpoch.Valid()) && resource.ValidDigest(captured.epoch) && n.resourcePeerSelectionCurrentLocked(captured.peer, captured.relationship, g, peer, identity) && peer.resourceClientEpoch.digest == captured.epoch && peer.resourceClientEpoch.identity == *identity
}

func (n *Node) resourcePeerCaptureCurrent(captured *ResourcePeerCapture, g *runtimeGeneration, peer *peerState, identity *managedAuthentication) bool {
	if captured == nil {
		return true // Existing public one-shot exchange, with its ordinary capture.
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.resourcePeerCaptureCurrentLocked(captured, g, peer, identity)
}
