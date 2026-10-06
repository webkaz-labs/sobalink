package directlan

import (
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"sync"
)

// tcpAdmissions captures the current paired generation in the synchronous
// packet handler, before gVisor's TCP Forwarder starts its own goroutine.
// A retransmission cannot replace a pending request's original authorization.
type tcpAdmission struct{ peer *peerState }
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
