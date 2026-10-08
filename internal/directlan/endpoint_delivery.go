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
	epoch   *ContextEpoch
}

func (n *Node) CaptureEndpointDelivery(key, expectedEndpoint string, epoch *ContextEpoch) (_ *EndpointDelivery, result error) {
	defer func() { observeAcceptanceEndpoint(n, "delivery-capture", result) }()
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.peers[key]
	if !epoch.Valid() {
		return nil, ErrUntrusted
	}
	if n.readyLocked() != nil || p == nil || p.binding == "" || p.peer.Endpoint.String() != expectedEndpoint {
		return nil, ErrUntrusted
	}
	captured := p.g.sessionIdentity(p)
	if captured == nil {
		return nil, ErrUntrusted
	}
	d := &EndpointDelivery{node: n, capture: captured, peer: p.peer, epoch: epoch}
	d.self = d
	return d, nil
}

// matches is data-only; actual send additionally requires the captured session
// identity to remain current and connect to admit that exact peer pointer.
func (d *EndpointDelivery) matches(n *Node) bool {
	return n != nil && d != nil && d.self == d && d.epoch.Valid() && d.node == n && d.capture != nil && d.capture.peer != nil && d.capture.generation != nil && d.capture.generation.n == n && n.generation.Load() == d.capture.generation && d.capture.peer.peer == d.peer && d.capture.peer.g == d.capture.generation && d.capture.binding != ""
}

// DeliverEndpointUpdate never resolves a peer key again. Replacement or a
// changed endpoint invalidates the exact captured target instead of redirecting.
func (n *Node) DeliverEndpointUpdate(ctx context.Context, target *EndpointDelivery, envelope endpointmeta.Envelope) (_ *endpointmeta.UpdateReply, result error) {
	stage := "delivery-target"
	defer func() { observeAcceptanceEndpoint(n, stage, result) }()
	if !target.matches(n) {
		return nil, ErrUntrusted
	}
	captured := target.capture
	defer func() {
		if result != nil && (!target.matches(n) || !captured.current()) {
			result = errors.Join(result, ErrUntrusted)
		}
	}()
	p := captured.peer
	key := target.peer.Key
	if !target.matches(n) || !captured.current() || envelope.Update.Issuer != n.PublicKey() || envelope.Update.Recipient != key || envelope.Update.PairBinding != captured.binding {
		return nil, ErrUntrusted
	}
	stage = "delivery-proof"
	pair := captured.generation.cfg.PairContexts[key]
	if err := endpointmeta.Inspect(envelope, pair, key, time.Now()); err != nil {
		return nil, err
	}
	stage = "delivery-connect"
	c, w, err := n.connect(ctx, target.peer, p)
	if err != nil {
		// A failed dial/TLS cleanup seals its owner synchronously. Do not
		// classify an earlier EOF as retryable after that cleanup failure.
		if !target.matches(n) || !captured.current() {
			return nil, errors.Join(err, ErrUntrusted)
		}
		return nil, err
	}
	defer func() {
		if closeErr := n.removeWire(w); closeErr != nil {
			result = errors.Join(result, ErrRetirementIncomplete, closeErr)
		}
	}()
	stop := watchConnection(ctx, w.raw)
	defer stop()
	stopEpoch := watchContextEpoch(target.epoch, nil, false, w.raw)
	defer stopEpoch()
	if c.ConnectionState().NegotiatedProtocol != contextProtocolName || !target.matches(n) || !captured.current() {
		return nil, ErrUntrusted
	}
	data, err := endpointmeta.Encode(envelope)
	if err != nil {
		return nil, err
	}
	if err = endpointmeta.Inspect(envelope, pair, key, time.Now()); err != nil {
		return nil, err
	}
	if !target.matches(n) || !captured.current() || w.controlContext.Err() != nil {
		return nil, ErrUntrusted
	}
	stage = "delivery-write"
	if err = writeFrame(c, data, endpointmeta.MaxFrameBytes); err != nil {
		return nil, err
	}
	stage = "delivery-read"
	data, err = readFrame(c, endpointmeta.MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	stage = "delivery-reply"
	parsed, err := endpointmeta.ParseReply(data, "endpoint-update")
	if err != nil {
		return nil, err
	}
	if !target.matches(n) || !captured.current() {
		return nil, ErrUntrusted
	}
	if refusal, ok := parsed.(*endpointmeta.ErrorReply); ok {
		switch refusal.Code {
		case "unavailable":
			return nil, ErrUnavailable
		case "capacity":
			return nil, ErrCapacity
		default:
			return nil, ErrUntrusted
		}
	}
	reply, ok := parsed.(*endpointmeta.UpdateReply)
	digest, _ := envelope.Digest()
	if !ok || !target.matches(n) || !captured.current() || reply.PairBinding != captured.binding || reply.Sequence != envelope.Update.Sequence || reply.UpdateDigest != digest {
		return nil, ErrUntrusted
	}
	if err = errors.Join(w.controlContext.Err(), ctx.Err()); err != nil {
		return nil, err
	}
	return reply, nil
}
