package directlan

import (
	"context"
	"crypto/tls"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// ManagedEndpointRequest is an exact pinned TLS observation, not a durable
// receipt or session grant. Its release signal is owned solely by handle.
type ManagedEndpointRequest struct {
	self     *ManagedEndpointRequest
	node     *Node
	capture  *managedAuthentication
	envelope endpointmeta.Envelope
	ctx      context.Context
	deadline time.Time
	released <-chan error
	live     atomic.Bool
}

func (r *ManagedEndpointRequest) Node() *Node {
	if r == nil {
		return nil
	}
	return r.node
}
func (r *ManagedEndpointRequest) PeerKey() string {
	if r == nil || r.capture == nil || r.capture.peer == nil {
		return ""
	}
	return r.capture.peer.peer.Key
}
func (r *ManagedEndpointRequest) Envelope() endpointmeta.Envelope {
	if r == nil {
		return endpointmeta.Envelope{}
	}
	return r.envelope
}
func (r *ManagedEndpointRequest) Origin() transportorigin.Origin {
	if r == nil || r.capture == nil || r.capture.generation == nil {
		return nil
	}
	return r.capture.generation.origin
}
func (r *ManagedEndpointRequest) Released() <-chan error {
	if r == nil {
		return nil
	}
	return r.released
}
func (r *ManagedEndpointRequest) Current() bool {
	return r != nil && r.self == r && r.live.Load() && r.capture != nil && r.envelope.Update.Issuer == r.PeerKey() &&
		r.envelope.Update.PairBinding == r.capture.binding && managedObservationCurrent(r.node, r.capture, r.ctx, r.deadline)
}
func (n *Node) handleManagedEndpoint(ctx context.Context, c *tls.Conn, w *wire, captured *managedAuthentication, envelope endpointmeta.Envelope) {
	if w == nil || w.g == nil || captured == nil || captured.generation != w.g || w.g.cfg.EndpointAdmission == nil {
		return
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return
	}
	w.endpointReleased = make(chan error, 1)
	r := &ManagedEndpointRequest{node: n, capture: captured, envelope: envelope, ctx: ctx, deadline: deadline, released: w.endpointReleased}
	r.self = r
	r.live.Store(true)
	defer r.live.Store(false)
	if !r.Current() {
		return
	}
	reply, err := w.g.cfg.EndpointAdmission(ctx, r)
	// An accepted transition has no early success reply: closing this old wire
	// releases its counted work before Core waits for exact generation retirement.
	// A lost reply is resolved by the unchanged signed proof on a later connection.
	if err != nil || reply == nil || !r.Current() {
		return
	}
	digest, err := envelope.Digest()
	if err != nil || reply.PairBinding != captured.binding || reply.Sequence != envelope.Update.Sequence || reply.UpdateDigest != digest {
		return
	}
	data, err := endpointmeta.Encode(*reply)
	if err != nil || !r.Current() {
		return
	}
	_ = writeFrame(c, data, endpointmeta.MaxFrameBytes)
}

// ParseRequest deliberately returns signed envelopes by value. Keep this pure
// discriminator shared with codec-only regression coverage.
func endpointRequestEnvelope(request endpointmeta.Request) (endpointmeta.Envelope, bool) {
	envelope, ok := request.(endpointmeta.Envelope)
	return envelope, ok
}
