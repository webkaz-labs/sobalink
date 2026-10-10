package directlan

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// InspectRemote performs one managed, bounded userspace exchange. The expected
// relationship is oriented as the remote grant: TargetKey is remote, PeerKey
// is local. It only narrows authenticated transport authority. No selector is
// sent until the fixed supported hello has completed; there is no fallback.
func (n *Node) InspectRemote(ctx context.Context, expected resourcegrant.Relationship, request resourcegrant.InspectRequest) (resourcegrant.Inspection, error) {
	return n.inspectRemote(ctx, expected, request, nil)
}

// InspectRemoteCaptured shares the legacy one-shot body but cannot recapture a
// replacement origin when its caller's original selection has become stale.
func (n *Node) InspectRemoteCaptured(ctx context.Context, captured *ResourcePeerCapture, expected resourcegrant.Relationship, request resourcegrant.InspectRequest) (resourcegrant.Inspection, error) {
	if captured == nil || captured.relationship != expected {
		return resourcegrant.Inspection{}, ErrUntrusted
	}
	return n.inspectRemote(ctx, expected, request, captured)
}

func (n *Node) inspectRemote(ctx context.Context, expected resourcegrant.Relationship, request resourcegrant.InspectRequest, captured *ResourcePeerCapture) (resourcegrant.Inspection, error) {
	observation := beginInspectionObservation(ctx)
	phase := "validate"
	run := ctx
	// Snapshot the outcome before any connection/work cleanup can cancel run.
	finish := func(value resourcegrant.Inspection, err error) (resourcegrant.Inspection, error) {
		var contextErr error
		if run != nil {
			contextErr = run.Err()
		}
		observation.finish(phase, err, contextErr)
		return value, err
	}
	var empty resourcegrant.Inspection
	if n == nil || ctx == nil || expected.Validate() != nil || request.Validate() != nil {
		return finish(empty, resourcegrant.ErrInvalid)
	}
	if ctx.Err() != nil {
		return finish(empty, resourcegrant.ErrTransport)
	}
	var cancel context.CancelFunc
	run, cancel = context.WithTimeout(ctx, handshakeTimeout+inspectionIOTimeout)
	defer cancel()
	phase = "capture_peer"
	var selected *PeerCapability
	var err error
	if captured != nil {
		selected = captured.peer
	} else {
		selected, err = n.CapturePeer(expected.TargetKey)
	}
	if err != nil || selected == nil || selected.origin == nil {
		return finish(empty, ErrUntrusted)
	}
	phase = "capture_origin"
	g, err := selected.origin.capture()
	if err != nil {
		return finish(empty, ErrUntrusted)
	}
	defer selected.origin.callDone()
	if g.n != n {
		return finish(empty, ErrUntrusted)
	}
	phase = "acquire_work"
	work, err := g.acquireWork(cancel, false)
	if err != nil {
		return finish(empty, ErrUntrusted)
	}
	defer work.finish()
	g.mu.Lock()
	peer := g.peerRegistrations[selected.registration]
	g.mu.Unlock()
	phase = "capture_authentication"
	authentication := n.captureManagedSession(peer)
	if authentication == nil || authentication.registration != uint64(selected.registration) || authentication.generation != g || authentication.peer != peer || expected != (resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: peer.peer.Key, PeerKey: g.cfg.Identity.PublicKey(), PairBinding: authentication.binding}) || !n.resourcePeerCaptureCurrent(captured, g, peer, authentication) {
		return finish(empty, ErrUntrusted)
	}
	// The exact captured dial refreshes managed control and bounds one session
	// recovery wakeup. No selectors are sent before the post-dial checks and hello.
	phase = "dial"
	connection, err := n.dialInspectionPeer(run, g, peer, authentication)
	if err != nil {
		return finish(empty, resourcegrant.ErrTransport)
	}
	defer connection.Close()
	stop := watchConnection(run, connection)
	defer stop()
	phase = "capture_client"
	client, ok := captureResourceInspectionClient(run, connection, authentication, captured)
	if !ok {
		return finish(empty, ErrUntrusted)
	}
	phase = "deadline"
	deadline, _ := run.Deadline()
	if err := client.setDeadline(deadline); err != nil {
		return finish(empty, err)
	}
	phase = "hello_write"
	hello := resourcegrant.HelloRequest()
	if _, err := client.Write(hello[:]); err != nil {
		return finish(empty, err)
	}
	phase = "hello_read"
	if err := resourcegrant.ReadHelloResponse(client); err != nil {
		return finish(empty, err)
	}
	phase = "request_encode"
	frame, err := resourcegrant.InspectRequestFrame(request)
	if err != nil {
		return finish(empty, resourcegrant.ErrInvalid)
	}
	phase = "request_write"
	if _, err := client.Write(frame); err != nil {
		return finish(empty, err)
	}
	phase = "response_read"
	response, err := resourcegrant.ReadInspection(client)
	if err != nil {
		return finish(empty, err)
	}
	phase = "final_admission"
	if run.Err() != nil || !client.current() {
		return finish(empty, ErrUntrusted)
	}
	if response.Target != request.Target || response.ProtocolVersion != request.ProtocolVersion {
		return finish(empty, resourcegrant.ErrProtocol)
	}
	phase = "complete"
	return finish(response, nil)
}

// This private adapter is minted only from the exact outbound dial above. All
// I/O admission pins the captured registration/policy, rather than allowing a
// later reauthentication to substitute the current flow authentication.
type inspectionClient struct {
	ctx             context.Context
	flow            *flow
	authentication  *managedAuthentication
	resourceCapture *ResourcePeerCapture
}

func captureInspectionClient(ctx context.Context, connection net.Conn, authentication *managedAuthentication) (*inspectionClient, bool) {
	return captureResourceInspectionClient(ctx, connection, authentication, nil)
}

func captureResourceInspectionClient(ctx context.Context, connection net.Conn, authentication *managedAuthentication, captured *ResourcePeerCapture) (*inspectionClient, bool) {
	f, ok := connection.(*flow)
	if ctx == nil || ctx.Err() != nil || !ok || f == nil || f.n == nil || f.g == nil || f.c == nil || f.w == nil || f.w.peer == nil || authentication == nil || f.inbound || f.network != "tcp" || f.remote.Port() != ResourceInspectPort || authentication.generation != f.g || authentication.peer != f.w.peer || authentication.binding == "" {
		return nil, false
	}
	client := &inspectionClient{ctx: ctx, flow: f, authentication: authentication, resourceCapture: captured}
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	if f.resourceInspectionCaptured || !client.currentLocked() {
		return nil, false
	}
	f.resourceInspectionCaptured = true
	return client, true
}
func (c *inspectionClient) currentLocked() bool {
	if c.ctx.Err() != nil || !c.flow.n.validFlowLocked(c.flow) || !c.authentication.current() || c.resourceCapture != nil && !c.flow.n.resourcePeerCaptureCurrentLocked(c.resourceCapture, c.flow.g, c.flow.w.peer, c.authentication) {
		return false
	}
	current := c.flow.w.peer.authenticated.Load()
	return current != nil && *current == *c.authentication
}
func (c *inspectionClient) current() bool {
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	return c.currentLocked()
}
func (c *inspectionClient) borrow(deadline bool) (net.Conn, bool) {
	c.flow.n.mu.Lock()
	defer c.flow.n.mu.Unlock()
	if !c.currentLocked() {
		return nil, false
	}
	return c.flow.c.borrow(deadline)
}
func (c *inspectionClient) setDeadline(deadline time.Time) error {
	raw, ok := c.borrow(true)
	if !ok {
		return ErrUntrusted
	}
	defer c.flow.c.release(true)
	if raw.SetDeadline(deadline) != nil {
		return resourcegrant.ErrTransport
	}
	return nil
}
func (c *inspectionClient) Write(data []byte) (int, error) {
	raw, ok := c.borrow(false)
	if !ok {
		return 0, ErrUntrusted
	}
	defer c.flow.c.release(false)
	n, err := raw.Write(data)
	if err != nil || n != len(data) {
		return n, resourcegrant.ErrTransport
	}
	return n, nil
}
func (c *inspectionClient) Read(data []byte) (int, error) {
	raw, ok := c.borrow(false)
	if !ok {
		return 0, ErrUntrusted
	}
	n, err := raw.Read(data)
	c.flow.c.release(false)
	if !c.current() {
		clear(data[:n])
		return 0, ErrUntrusted
	}
	if err != nil && err != io.EOF {
		return n, resourcegrant.ErrTransport
	}
	return n, err
}
