package directlan

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

var ErrInspectionConflict = errors.New("inspection port already has an owner")

const ResourceInspectPort uint16 = resourcegrant.ProtocolPort
const inspectionIOTimeout = 5 * time.Second
const (
	inspectionNew uint32 = iota
	inspectionNegotiating
	inspectionReady
	inspectionClosed
)

// InspectionListener owns exactly one opt-in userspace application entrance.
// It never replaces a service listener or changes global reserved-port policy.
// Creation belongs only to the owned confirmed grant coordinator.
type InspectionListener struct{ owner *listener }

func (n *Node) ListenInspection(ctx context.Context) (*InspectionListener, error) {
	if n == nil {
		return nil, ErrUnavailable
	}
	raw, err := n.listenPeer(ctx, "tcp", ResourceInspectPort, ErrInspectionConflict)
	if err != nil {
		return nil, err
	}
	owner, ok := raw.(*listener)
	if !ok {
		_ = raw.Close()
		return nil, ErrUnavailable
	}
	return &InspectionListener{owner: owner}, nil
}
func (l *InspectionListener) Close() error {
	if l == nil || l.owner == nil {
		return nil
	}
	return l.owner.Close()
}
func (l *InspectionListener) Accept() (*ManagedInspection, error) {
	if l == nil || l.owner == nil {
		return nil, ErrUnavailable
	}
	raw, err := l.owner.Accept()
	if err != nil {
		return nil, err
	}
	capability, ok := captureManagedInspection(raw, l.owner)
	if !ok {
		_ = raw.Close()
		return nil, ErrUntrusted
	}
	return capability, nil
}

// ManagedInspection exposes no raw connection or general Write. A fixed hello
// must finish before exactly one request and one grant-fenced inspection reply.
type ManagedInspection struct {
	flow           *flow
	owner          *listener
	authentication *managedAuthentication
	relationship   resourcegrant.Relationship
	phase          atomic.Uint32
	requestRead    atomic.Bool
	requestReady   atomic.Bool
	request        resourcegrant.InspectRequest
	used           atomic.Bool
}

func captureManagedInspection(connection net.Conn, owner *listener) (*ManagedInspection, bool) {
	f, ok := connection.(*flow)
	if !ok || f == nil || f.n == nil || f.g == nil || f.c == nil || f.w == nil || f.w.peer == nil || owner == nil || owner.n != f.n || owner.service != (service{"tcp", ResourceInspectPort}) || !f.inbound || f.network != "tcp" || f.local.Port() != ResourceInspectPort {
		return nil, false
	}
	// deliver writes this pointer under flow.mu. Never acquire listener.mu while
	// holding Node.mu: the existing listener/flow ownership order stays intact.
	f.mu.Lock()
	matches := f.listener == owner && !f.closed
	f.mu.Unlock()
	if !matches {
		return nil, false
	}
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if f.resourceInspectionCaptured || f.resourceManagementCaptured || f.n.listeners[owner.service] != owner || inspectionListenerClosed(owner) || !f.n.validFlowLocked(f) || f.w.peer.binding == "" {
		return nil, false
	}
	captured := f.g.sessionIdentity(f.w.peer)
	authenticated := f.w.peer.authenticated.Load()
	if captured == nil || authenticated == nil || *captured != *authenticated {
		return nil, false
	}
	relationship := resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: f.g.cfg.Identity.PublicKey(), PeerKey: f.w.key, PairBinding: captured.binding}
	if relationship.Validate() != nil {
		return nil, false
	}
	f.resourceInspectionCaptured = true
	return &ManagedInspection{flow: f, owner: owner, authentication: captured, relationship: relationship}, true
}
func inspectionListenerClosed(owner *listener) bool {
	select {
	case <-owner.done:
		return true
	default:
		return false
	}
}
func (c *ManagedInspection) currentLocked() bool {
	return c != nil && c.flow != nil && c.owner != nil && c.authentication != nil && c.phase.Load() != inspectionClosed &&
		c.flow.n.listeners[c.owner.service] == c.owner && !inspectionListenerClosed(c.owner) && c.flow.n.validFlowLocked(c.flow) && c.authentication.current()
}
func (c *ManagedInspection) current() bool {
	if c == nil || c.flow == nil || c.flow.n == nil {
		return false
	}
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	return c.currentLocked()
}

// Relationship is an immutable identity snapshot, never continued authority.
func (c *ManagedInspection) Relationship() (resourcegrant.Relationship, bool) {
	if c == nil || c.flow == nil || c.authentication == nil {
		return resourcegrant.Relationship{}, false
	}
	return c.relationship, true
}
func (c *ManagedInspection) Close() error {
	if c == nil || c.flow == nil || c.flow.n == nil {
		return nil
	}
	c.flow.n.mu.Lock()
	c.phase.Store(inspectionClosed)
	c.flow.n.mu.Unlock()
	return c.flow.Close()
}
func (c *ManagedInspection) fail(err error) error { _ = c.Close(); return err }

// Negotiate exchanges only eight fixed protocol bytes in each direction. It
// reveals no selector, grant availability, settings or resource existence.
// Failure, unsupported version and a repeated call are terminal; no fallback.
func (c *ManagedInspection) Negotiate() error {
	if c == nil || c.flow == nil || !c.phase.CompareAndSwap(inspectionNew, inspectionNegotiating) {
		return c.fail(ErrUntrusted)
	}
	if !c.current() {
		return c.fail(ErrUntrusted)
	}
	f := c.flow
	if err := f.SetDeadline(time.Now().Add(inspectionIOTimeout)); err != nil {
		return c.fail(resourcegrant.ErrTransport)
	}
	version, err := resourcegrant.ReadHelloRequest(f)
	if err != nil {
		return c.fail(err)
	}
	reply, supported, err := resourcegrant.HelloResponse(version)
	if err != nil {
		return c.fail(err)
	}
	f.n.mu.Lock()
	if c.phase.Load() != inspectionNegotiating || !c.currentLocked() {
		f.n.mu.Unlock()
		return c.fail(ErrUntrusted)
	}
	raw, ok := f.c.borrow(false)
	f.n.mu.Unlock()
	if !ok {
		return c.fail(resourcegrant.ErrTransport)
	}
	n, writeErr := raw.Write(reply[:])
	f.c.release(false)
	if writeErr != nil || n != len(reply) {
		return c.fail(resourcegrant.ErrTransport)
	}
	if !supported {
		return c.fail(resourcegrant.ErrUnsupported)
	}
	f.n.mu.Lock()
	ready := c.currentLocked() && c.phase.CompareAndSwap(inspectionNegotiating, inspectionReady)
	f.n.mu.Unlock()
	if !ready {
		return c.fail(ErrUntrusted)
	}
	return nil
}

// ReadRequest owns one deadline across both frame header and body. It exposes
// only a strict typed request, never the flow or another local command channel.
func (c *ManagedInspection) ReadRequest() (resourcegrant.InspectRequest, error) {
	if c == nil || c.flow == nil || c.phase.Load() != inspectionReady || !c.requestRead.CompareAndSwap(false, true) {
		return resourcegrant.InspectRequest{}, c.fail(ErrUntrusted)
	}
	if !c.current() {
		return resourcegrant.InspectRequest{}, c.fail(ErrUntrusted)
	}
	if err := c.flow.SetReadDeadline(time.Now().Add(inspectionIOTimeout)); err != nil {
		return resourcegrant.InspectRequest{}, c.fail(resourcegrant.ErrTransport)
	}
	request, err := resourcegrant.ReadInspectRequest(c.flow)
	if err != nil {
		if err == resourcegrant.ErrTransport {
			return resourcegrant.InspectRequest{}, c.fail(err)
		}
		return resourcegrant.InspectRequest{}, c.fail(resourcegrant.ErrProtocol)
	}
	c.flow.n.mu.Lock()
	if !c.currentLocked() || c.phase.Load() != inspectionReady {
		c.flow.n.mu.Unlock()
		return resourcegrant.InspectRequest{}, c.fail(ErrUntrusted)
	}
	c.request = request
	c.requestReady.Store(true)
	c.flow.n.mu.Unlock()
	return request, nil
}

// WriteInspection admits exactly one bounded reply against the current flow
// and terminal grant fence. It closes on every outcome. A write admitted before
// revocation may finish afterward; no bytes-after-expiry guarantee is implied.
func (c *ManagedInspection) WriteInspection(fence *resourcegrant.DisclosureFence, request resourcegrant.InspectRequest, inspection resourcegrant.Inspection) error {
	if c == nil || c.flow == nil {
		return ErrUntrusted
	}
	defer c.Close()
	if c.authentication == nil || c.phase.Load() != inspectionReady || !c.requestReady.Load() || request != c.request || request.Validate() != nil || inspection.Target != request.Target {
		return ErrUntrusted
	}
	frame, err := resourcegrant.InspectionFrame(inspection)
	if err != nil {
		return ErrUnavailable
	}
	if !c.used.CompareAndSwap(false, true) {
		return ErrUntrusted
	}
	f := c.flow
	if err := f.SetWriteDeadline(time.Now().Add(inspectionIOTimeout)); err != nil {
		return err
	}
	f.n.mu.Lock()
	if c.phase.Load() != inspectionReady || !c.currentLocked() || !fence.Admit(request, c.relationship) {
		f.n.mu.Unlock()
		return ErrUntrusted
	}
	raw, ok := f.c.borrow(false)
	f.n.mu.Unlock()
	if !ok {
		return net.ErrClosed
	}
	defer f.c.release(false)
	n, err := raw.Write(frame)
	if err == nil && n != len(frame) {
		return io.ErrShortWrite
	}
	return err
}
