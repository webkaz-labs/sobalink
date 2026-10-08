package directlan

import (
	"context"
	"crypto/tls"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// ManagedCompletionRequest is a transport-minted observation, not publication
// evidence. Its private capture binds this one synchronous request to the pinned
// ordinary Node, peer, generation, policy and nonzero transport registration.
// A zero value, a retained callback argument or a copied DTO grants nothing.
type ManagedCompletionRequest struct {
	self     *ManagedCompletionRequest
	node     *Node
	capture  *managedAuthentication
	request  endpointmeta.BoundRequest
	ctx      context.Context
	deadline time.Time
	live     atomic.Bool
}

func (r *ManagedCompletionRequest) Node() *Node {
	if r == nil {
		return nil
	}
	return r.node
}
func (r *ManagedCompletionRequest) PeerKey() string {
	if r == nil || r.capture == nil || r.capture.peer == nil {
		return ""
	}
	return r.capture.peer.peer.Key
}
func (r *ManagedCompletionRequest) Request() endpointmeta.BoundRequest {
	if r == nil {
		return endpointmeta.BoundRequest{}
	}
	return r.request
}

// Current performs only bounded current-owner checks. It never waits for a
// Node lock, invokes Core, or grants application access. Contention fails closed.
func (r *ManagedCompletionRequest) Current() bool {
	if r == nil || r.self != r || r.node == nil || r.capture == nil || r.capture.generation == nil || r.capture.peer == nil || r.capture.generation.n != r.node ||
		!r.live.Load() || r.ctx == nil || r.ctx.Err() != nil || !time.Now().Before(r.deadline) ||
		r.request.Version != 2 || (r.request.Operation != "pair-context-commit" && r.request.Operation != "pair-context-status") ||
		r.request.PairBinding != r.capture.binding || !r.node.mu.TryLock() {
		return false
	}
	defer r.node.mu.Unlock()
	n, a := r.node, r.capture
	g, p := a.generation, a.peer
	if !g.mu.TryLock() {
		return false
	}
	defer g.mu.Unlock()
	// Unlike application admission, a read-only completion needs no WG callback.
	// Observe its terminal signal without entering the WG bookkeeping lock.
	if engine := g.engine.Load(); engine != nil && channelClosed(engine.StopRequested()) {
		return false
	}
	if n.contextControl || n.closed || n.closing.Load() || n.recovery || !n.started || n.generation.Load() != g ||
		n.peers[r.PeerKey()] != p || n.deniedKey(r.PeerKey()) || g.sealed || !g.traffic.Load() ||
		g.bind == nil || p.g != g || p.session == nil || a.registration == 0 || p.session.registration.Load() != a.registration {
		return false
	}
	policy := g.bind.policy.Load()
	address, _ := OverlayAddress(p.peer.Key)
	return policy != nil && policy == a.policy && policy.generations[address] == p && p.binding == a.binding &&
		r.live.Load() && r.ctx.Err() == nil && time.Now().Before(r.deadline)

}

func (n *Node) handleManagedCompletion(ctx context.Context, c *tls.Conn, w *wire, captured *managedAuthentication, bound endpointmeta.BoundRequest) {
	if n.cfg.CompletionAdmission == nil || (bound.Operation != "pair-context-commit" && bound.Operation != "pair-context-status") {
		return
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return
	}
	r := &ManagedCompletionRequest{node: n, capture: captured, request: bound, ctx: ctx, deadline: deadline}
	r.self = r
	r.live.Store(true)
	defer r.live.Store(false)
	if !r.Current() || captured.generation != w.g {
		return
	}
	// The same wire/quota, deadline, cancellation watcher and failed-close owner
	// surround this callback. No Node lock crosses Core or the frame write.
	response, err := n.cfg.CompletionAdmission(ctx, r)
	if err != nil || !r.Current() || response.Admit == nil || !response.Epoch.Valid() {
		return
	}
	reply, ok := response.Reply.(endpointmeta.ContextReply)
	if !ok || reply.Version != 2 || !reply.OK || reply.Operation != bound.Operation || reply.PairBinding != bound.PairBinding || reply.State != "committed" {
		return
	}
	// Reuse the joined epoch watcher. With initial=false it needs no context
	// attempt and closes through the existing accounting wrapper on invalidation.
	stop := watchContextEpoch(response.Epoch, nil, false, w.raw)
	defer stop()
	// The one-use Core admission runs before serialization. The final transport
	// check is synchronous write admission; later cancellation cannot retract bytes.
	if !response.Admit() || !r.Current() || !response.Epoch.Valid() {
		return
	}
	data, err := endpointmeta.Encode(reply)
	if err != nil || !r.Current() || !response.Epoch.Valid() {
		return
	}
	_ = writeFrame(c, data, endpointmeta.MaxFrameBytes)
	// Deliberately no commitManagedSession, WG initiation or grant mutation.
}
