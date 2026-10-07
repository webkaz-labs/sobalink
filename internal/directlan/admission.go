package directlan

import (
	"context"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"sync"
)

// tcpAdmissions captures the current paired generation in the synchronous
// packet handler, before gVisor's TCP Forwarder starts its own goroutine.
// A retransmission cannot replace a pending request's original authorization.
type tcpAdmission struct {
	peer    *peerState
	creator *endpointCreator
}
type tcpAdmissions struct {
	mu      sync.Mutex
	limit   int
	pending map[stack.TransportEndpointID]*tcpAdmission
}

func newTCPAdmissions(limit int) *tcpAdmissions {
	return &tcpAdmissions{limit: limit, pending: map[stack.TransportEndpointID]*tcpAdmission{}}
}
func (q *tcpAdmissions) begin(id stack.TransportEndpointID, peer *peerState) (*tcpAdmission, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if existing := q.pending[id]; existing != nil {
		if existing.peer != peer {
			return nil, false
		}
		return existing, false
	}
	if peer == nil || len(q.pending) >= q.limit {
		return nil, false
	}
	entry := &tcpAdmission{peer: peer}
	q.pending[id] = entry
	return entry, true
}
func (q *tcpAdmissions) get(id stack.TransportEndpointID) *tcpAdmission {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending[id]
}
func (q *tcpAdmissions) retire(id stack.TransportEndpointID, entry *tcpAdmission) {
	q.mu.Lock()
	if q.pending[id] == entry {
		delete(q.pending, id)
	}
	q.mu.Unlock()
}

// tcpRequestLifecycle serializes the short synchronous HandlePacket admission
// with its AcquireRequest callback. This supplies exact attribution without
// changing the upstream lifecycle API or leaving a policy entry behind when
// the forwarder consumes a packet but refuses to create a request.
type tcpRequestLifecycle struct {
	g         *runtimeGeneration
	mu        sync.Mutex
	admitting *tcpAdmission
}

func (l *tcpRequestLifecycle) AcquireRequest() (tcp.ForwarderRequestLease, bool) {
	if l.admitting == nil {
		return nil, false
	}
	creator, e := l.g.acquireCreator(context.Background(), true)
	if e != nil {
		return nil, false
	}
	l.admitting.creator = creator
	return creator, true
}
func (l *tcpRequestLifecycle) handle(f *tcp.Forwarder, id stack.TransportEndpointID, pkt *stack.PacketBuffer, peer *peerState) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, added := l.g.incoming.begin(id, peer)
	if entry == nil {
		return true
	}
	l.admitting = entry
	handled := f.HandlePacket(id, pkt)
	l.admitting = nil
	if added && (!handled || entry.creator == nil) {
		l.g.incoming.retire(id, entry)
	}
	return handled
}

var _ tcp.ForwarderLifecycle = (*tcpRequestLifecycle)(nil)

// complete keeps the forwarder's ID release and the policy-entry retirement in
// the same short admission transaction. A new SYN at the same tuple must obtain
// a fresh entry, never one still awaiting an old handler's deferred removal.
func (l *tcpRequestLifecycle) complete(r *tcp.ForwarderRequest, id stack.TransportEndpointID, entry *tcpAdmission, reset bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r.Complete(reset)
	l.g.incoming.retire(id, entry)
}
