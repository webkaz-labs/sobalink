package core

import (
	"context"
	"errors"
	"sync"
)

type peerScopeFailureKey struct{}

// One serialized binding transaction owns this failure/unwind path. It closes
// logical authority and signals children immediately, but never joins a backend
// while the binding still owns a generation lease. The enclosing Core work
// registration remains charged through the later real child/server joins.
type peerScopeFailure struct {
	core     *Core
	node     *mixedBackend
	children []NetworkBackend
	once     sync.Once
	mu       sync.Mutex
	cause    error
	drains   []func() error
}

func newPeerScopeFailure(c *Core, n *mixedBackend) *peerScopeFailure {
	owner := &peerScopeFailure{core: c, node: n}
	for _, name := range n.order {
		if child := n.nodes[name]; child != nil {
			owner.children = append(owner.children, child)
		}
	}
	return owner
}

func (o *peerScopeFailure) fail(cause error) error {
	o.once.Do(func() {
		_ = o.core.failWorkerScope(nil, cause)
		o.node.mu.Lock()
		o.node.closed = true
		clear(o.node.scopes)
		clear(o.node.sources)
		o.node.mu.Unlock()
		// Signal every production child before any join. Direct LAN uses its
		// permanent generation gate; worker cancellation closes its owner
		// pipes without waiting for process/engine termination.
		for _, child := range o.children {
			switch node := child.(type) {
			case *directLANBackend:
				node.Node.RequestClose()
			case *processBackend:
				node.cancel()
			default:
				// Unknown injected backends have no generation attribution in
				// capturePeerHTTPBackend. Their mixed authority is already shut;
				// do not risk an unreviewed Close wait inside this participant.
				cause = errors.Join(cause, errors.New("backend lacks a nonjoining scope-failure stop signal"))
			}
		}
		// All stop signals precede even the owner-pipe close operations. These
		// Client closes seal IPC admission and wake calls; processBackend.Close
		// (which waits for the child) is reserved for the later join.
		for _, child := range o.children {
			if node, ok := child.(*processBackend); ok {
				_ = node.remote.Close()
			}
		}
		o.mu.Lock()
		o.cause = cause
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	// Preserve a later durable-revocation error wrapping the original scope
	// error. Failure propagation must not erase atomic-write uncertainty.
	if !errors.Is(o.cause, cause) {
		if errors.Is(cause, o.cause) {
			o.cause = cause
		} else {
			o.cause = errors.Join(o.cause, cause)
		}
	}
	return o.cause
}

func (o *peerScopeFailure) failure() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cause
}

func (o *peerScopeFailure) deferDrain(close func() error) {
	o.mu.Lock()
	o.drains = append(o.drains, close)
	o.mu.Unlock()
}

// The caller has released both its commit and application origin leases. No
// timeout or signal counts as completion: return only after every real Close.
func (o *peerScopeFailure) join(result error) error {
	if cause := o.failure(); cause != nil {
		if !errors.Is(result, cause) {
			result = errors.Join(result, cause)
		}
		for _, child := range o.children {
			result = errors.Join(result, child.Close())
		}
		for _, close := range o.drains {
			result = errors.Join(result, close())
		}
	}
	o.core, o.node, o.children, o.drains = nil, nil, nil, nil
	return result
}

func (n *mixedBackend) failTCPScopes(ctx context.Context, cause error) error {
	if owner, ok := ctx.Value(peerScopeFailureKey{}).(*peerScopeFailure); ok && owner.node == n {
		return owner.fail(cause)
	}
	_ = n.Close()
	return cause
}
