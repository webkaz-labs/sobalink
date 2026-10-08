package directlan

import (
	"context"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// EndpointDelivery binds one exact approved destination and transport generation.
// It is attribution for a later send, never a session or durable authority grant.
type EndpointDelivery struct {
	self    *EndpointDelivery
	node    *Node
	capture *managedAuthentication
	peer    Peer
}

func (n *Node) CaptureEndpointDelivery(key, expectedEndpoint string) (*EndpointDelivery, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.peers[key]
	if n.readyLocked() != nil || p == nil || p.binding == "" || p.peer.Endpoint.String() != expectedEndpoint {
		return nil, ErrUntrusted
	}
	captured := p.g.sessionIdentity(p)
	if captured == nil {
		return nil, ErrUntrusted
	}
	d := &EndpointDelivery{node: n, capture: captured, peer: p.peer}
	d.self = d
	return d, nil
}

// matches is data-only; actual send additionally requires the captured session
// identity to remain current and connect to admit that exact peer pointer.
func (d *EndpointDelivery) matches(n *Node) bool {
	return n != nil && d != nil && d.self == d && d.node == n && d.capture != nil && d.capture.peer != nil && d.capture.generation != nil && d.capture.generation.n == n && n.generation.Load() == d.capture.generation && d.capture.peer.peer == d.peer && d.capture.peer.g == d.capture.generation && d.capture.binding != ""
}

// DeliverEndpointUpdate never resolves a peer key again. Replacement or a
// changed endpoint invalidates the exact captured target instead of redirecting.
func (n *Node) DeliverEndpointUpdate(ctx context.Context, target *EndpointDelivery, envelope endpointmeta.Envelope) (_ *endpointmeta.UpdateReply, result error) {
	if !target.matches(n) {
		return nil, ErrUntrusted
	}
	captured := target.capture
	p := captured.peer
	key := target.peer.Key
	if !captured.current() || envelope.Update.Issuer != n.PublicKey() || envelope.Update.Recipient != key || envelope.Update.PairBinding != captured.binding {
		return nil, ErrUntrusted
	}
	pair := captured.generation.cfg.PairContexts[key]
	if err := endpointmeta.Inspect(envelope, pair, key, time.Now()); err != nil {
		return nil, err
	}
	c, w, err := n.connect(ctx, target.peer, p)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, n.removeWire(w)) }()
	stop := watchConnection(ctx, w.raw)
	defer stop()
	if c.ConnectionState().NegotiatedProtocol != contextProtocolName || !captured.current() {
		return nil, ErrUntrusted
	}
	data, err := endpointmeta.Encode(envelope)
	if err != nil {
		return nil, err
	}
	if err = endpointmeta.Inspect(envelope, pair, key, time.Now()); err != nil {
		return nil, err
	}
	if !captured.current() || w.controlContext.Err() != nil {
		return nil, ErrUntrusted
	}
	if err = writeFrame(c, data, endpointmeta.MaxFrameBytes); err != nil {
		return nil, err
	}
	data, err = readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := endpointmeta.ParseReply(data, "endpoint-update")
	if err != nil {
		return nil, err
	}
	reply, ok := parsed.(*endpointmeta.UpdateReply)
	digest, _ := envelope.Digest()
	if !ok || !captured.current() || reply.PairBinding != captured.binding || reply.Sequence != envelope.Update.Sequence || reply.UpdateDigest != digest {
		return nil, ErrUntrusted
	}
	if err = errors.Join(w.controlContext.Err(), ctx.Err()); err != nil {
		return nil, err
	}
	return reply, nil
}
