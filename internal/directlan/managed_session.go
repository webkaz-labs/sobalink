package directlan

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

func clonePairContexts(input map[string]endpointmeta.PairContext) map[string]endpointmeta.PairContext {
	if input == nil {
		return nil
	}
	out := make(map[string]endpointmeta.PairContext, len(input))
	for key, pair := range input {
		pair.HostScope.Prefixes = append([]string(nil), pair.HostScope.Prefixes...)
		pair.JoinerScope.Prefixes = append([]string(nil), pair.JoinerScope.Prefixes...)
		out[key] = pair
	}
	return out
}

func (c Config) validatePairContexts() error {
	scope := endpointmeta.Scope{Family: "ipv6"}
	if c.Listen.Addr().Is4() {
		scope.Family = "ipv4"
	}
	for _, prefix := range c.AllowedPrefixes {
		scope.Prefixes = append(scope.Prefixes, prefix.String())
	}
	sort.Strings(scope.Prefixes)
	expectedScope, err := endpointmeta.Encode(scope)
	if len(c.PairContexts) != 0 && err != nil {
		return ErrPolicy
	}
	peers := make(map[string]Peer, len(c.Peers))
	for _, peer := range c.Peers {
		peers[peer.Key] = peer
	}
	for key, pair := range c.PairContexts {
		peer, ok := peers[key]
		if !ok {
			return ErrIdentity
		}
		if _, err := pair.Binding(); err != nil {
			return err
		}
		localKey, remoteKey := pair.HostKey, pair.JoinerKey
		localTunnel, remoteTunnel := pair.HostTunnelKey, pair.JoinerTunnelKey
		localEndpoint, remoteEndpoint := pair.HostEndpoint, pair.JoinerEndpoint
		localScope := pair.HostScope
		if localKey != c.Identity.PublicKey() {
			localKey, remoteKey = remoteKey, localKey
			localTunnel, remoteTunnel = remoteTunnel, localTunnel
			localEndpoint, remoteEndpoint = remoteEndpoint, localEndpoint
			localScope = pair.JoinerScope
		}
		encodedScope, err := endpointmeta.Encode(localScope)
		if err != nil || !bytes.Equal(expectedScope, encodedScope) || localKey != c.Identity.PublicKey() || remoteKey != key || localTunnel != c.Identity.TunnelKey() || remoteTunnel != peer.TunnelKey || localEndpoint != c.Listen.String() || remoteEndpoint != peer.Endpoint.String() {
			return ErrIdentity
		}
	}
	return nil
}

// Classification belongs to immutable constructor input, even after retirement.
func (n *Node) managedKey(key string) bool { _, ok := n.cfg.PairContexts[key]; return ok }

type managedAuthentication struct {
	peer         *peerState
	generation   *runtimeGeneration
	policy       *bindPolicy
	registration uint64
	binding      string
}

// sessionIdentity is shared by control commit and application admission. It is
// free of Node.mu on the synchronous receive path; TLS never holds Node.mu.
func (g *runtimeGeneration) sessionIdentity(p *peerState) *managedAuthentication {
	if g == nil || p == nil || p.g != g || p.session == nil || !g.trafficOpen() || g.n.closing.Load() || g.n.generation.Load() != g {
		return nil
	}
	policy := g.bind.policy.Load()
	address, _ := OverlayAddress(p.peer.Key)
	registration := p.session.registration.Load()
	if policy == nil || policy.generations[address] != p || registration == 0 {
		return nil
	}
	return &managedAuthentication{p, g, policy, registration, p.binding}
}
func (a *managedAuthentication) current() bool {
	if a == nil {
		return false
	}
	current := a.generation.sessionIdentity(a.peer)
	return current != nil && *current == *a
}
func (g *runtimeGeneration) applicationPeer(p *peerState) bool {
	current := g.sessionIdentity(p)
	if current == nil {
		return false
	}
	if p.binding == "" {
		return true
	}
	authenticated := p.authenticated.Load()
	return authenticated != nil && *authenticated == *current
}
func (n *Node) captureManagedSession(p *peerState) *managedAuthentication {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readyLocked() != nil || p == nil || p.binding == "" || n.peers[p.peer.Key] != p {
		return nil
	}
	return p.g.sessionIdentity(p)
}
func (n *Node) commitManagedSession(ctx context.Context, captured *managedAuthentication) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ctx.Err() != nil || n.readyLocked() != nil || captured == nil || n.peers[captured.peer.peer.Key] != captured.peer || !captured.current() {
		return ErrUntrusted
	}
	captured.peer.authenticated.Store(captured)
	captured.peer.session.notify()
	return nil
}

// Only these pinned-TLS paths commit authentication. An exported binding or
// decoded reply alone cannot mutate per-generation application authority.
func (n *Node) requestManagedSession(ctx context.Context, p *peerState) (result error) {
	captured := n.captureManagedSession(p)
	if captured == nil {
		return ErrUntrusted
	}
	c, w, err := n.connect(ctx, p.peer, p)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, n.removeWire(w)) }()
	stop := watchConnection(ctx, w.raw)
	defer stop()
	if c.ConnectionState().NegotiatedProtocol != contextProtocolName {
		return ErrUntrusted
	}
	request, err := endpointmeta.Encode(endpointmeta.BoundRequest{Version: 2, Operation: "session", PairBinding: captured.binding})
	if err != nil {
		return err
	}
	if err = writeFrame(c, request, endpointmeta.MaxFrameBytes); err != nil {
		return err
	}
	data, err := readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return err
	}
	reply, err := endpointmeta.ParseReply(data, "session")
	if err != nil {
		return err
	}
	session, ok := reply.(*endpointmeta.SessionReply)
	if !ok || session.PairBinding != captured.binding {
		return ErrUntrusted
	}
	return n.commitManagedSession(w.controlContext, captured)
}
func (n *Node) handleManagedSession(ctx context.Context, c *tls.Conn, w *wire) {
	captured := n.captureManagedSession(w.peer)
	if captured == nil || captured.generation != w.g {
		return
	}
	data, err := readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return
	}
	request, err := endpointmeta.ParseRequest(data)
	if err != nil {
		return
	}
	bound, ok := request.(*endpointmeta.BoundRequest)
	if !ok || bound.Operation != "session" || bound.PairBinding != captured.binding || !captured.current() || ctx.Err() != nil {
		return
	}
	reply, err := endpointmeta.Encode(endpointmeta.SessionReply{Version: 2, Operation: "session", OK: true, PairBinding: captured.binding})
	if err != nil || writeFrame(c, reply, endpointmeta.MaxFrameBytes) != nil {
		return
	}
	// Acknowledgement precedes local authentication and WG initiation. Inbound
	// processing never takes the outgoing gate, including simultaneous callers.
	if n.commitManagedSession(ctx, captured) != nil {
		return
	}
	if w.peer.session.initiator {
		_ = n.initiateSession(w.peer)
	}
}
