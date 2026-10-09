package directlan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"gvisor.dev/gvisor/pkg/tcpip"
)

// SOURCE-STAGED: these inert tests are not native transport acceptance. No
// NewNode/Start, WireGuard owner, OS socket, provider, persisted grant or actual
// endpoint is constructed. The embedded endpoint panics on accidental access.
type managementInertEndpoint struct{ tcpip.Endpoint }

type managementInertConn struct {
	mu             sync.Mutex
	input          *bytes.Reader
	output         bytes.Buffer
	writeDeadline  time.Time
	deadlineErr    error
	writeErr       error
	shortWrite     bool
	started, allow chan struct{}
}

func (c *managementInertConn) Read(data []byte) (int, error) { return c.input.Read(data) }
func (c *managementInertConn) Write(data []byte) (int, error) {
	if c.started != nil {
		close(c.started)
		<-c.allow
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.shortWrite {
		return len(data) - 1, nil
	}
	return c.output.Write(data)
}
func (c *managementInertConn) Close() error         { panic("raw close bypassed endpoint owner") }
func (c *managementInertConn) LocalAddr() net.Addr  { return nil }
func (c *managementInertConn) RemoteAddr() net.Addr { return nil }
func (c *managementInertConn) SetDeadline(d time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = d
	return c.deadlineErr
}
func (c *managementInertConn) SetReadDeadline(d time.Time) error  { return c.SetDeadline(d) }
func (c *managementInertConn) SetWriteDeadline(d time.Time) error { return c.SetDeadline(d) }

func managementTestRequest(action string) resourcegrant.ManagementRequest {
	request := resourcegrant.ManagementRequest{
		ManagementSelector: resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion,
			Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: strings.Repeat("a", 32)}, GrantID: strings.Repeat("b", 32), GrantRevision: 1},
		Action: action,
	}
	settings := resource.Settings{TransferConcurrentFiles: capacity.Limited(3), TransferConcurrentPerPeer: capacity.Default()}
	switch action {
	case resourcegrant.PreviewAction:
		request.Preview = &resourcegrant.ManagementPreviewRequest{Settings: settings}
	case resourcegrant.ApplyAction:
		request.Apply = &resourcegrant.ManagementApplyRequest{OperationID: strings.Repeat("c", 64), BaseRevision: strings.Repeat("d", 64), ReviewRevision: strings.Repeat("e", 64), Settings: settings}
	case resourcegrant.StatusAction:
		request.Status = &resourcegrant.ManagementStatusRequest{OperationID: strings.Repeat("c", 64)}
	}
	return request
}

func managementTestReply(request resourcegrant.ManagementRequest) resourcegrant.ManagementReply {
	reply := resourcegrant.ManagementReply{ManagementSelector: request.ManagementSelector, Action: request.Action}
	switch request.Action {
	case resourcegrant.Inspect:
		reply.Inspection = &resourcegrant.ManagementInspection{Requested: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Default()}, Effective: resource.Effective{TransferConcurrentFiles: 3, TransferConcurrentPerPeer: 2}}
	case resourcegrant.PreviewAction:
		reply.Preview = &resourcegrant.ManagementPreview{OperationID: strings.Repeat("c", 64), BaseRevision: strings.Repeat("d", 64), ReviewRevision: strings.Repeat("e", 64), Requested: request.Preview.Settings, Effective: resource.Effective{TransferConcurrentFiles: 3, TransferConcurrentPerPeer: 2}}
	case resourcegrant.ApplyAction:
		durable := true
		reply.Operation = &resourcegrant.ManagementOperation{OperationID: request.Apply.OperationID, Outcome: resource.UnknownOutcome(), EvidenceDurable: &durable}
	case resourcegrant.StatusAction:
		reply.Unavailable = &resourcegrant.ManagementStatusRequest{OperationID: request.Status.OperationID}
	}
	return reply
}

// managementInertOwner assembles exact in-memory membership without invoking a
// constructor or starting any native transport or endpoint supervisor.
func managementInertOwner(t *testing.T, version byte, request resourcegrant.ManagementRequest, contexts ...context.Context) (*ManagedManagement, *managementInertConn) {
	t.Helper()
	cfg, _ := managedFixtureConfig()
	n := &Node{cfg: cfg, started: true, ctx: context.Background(), peers: map[string]*peerState{}, wires: map[*wire]struct{}{}, flows: map[netip.AddrPort]map[*flow]struct{}{}, listeners: map[service]*listener{}}
	bind := &lanBind{}
	g := newRuntimeGeneration(n, bind, nil)
	bind.owner = g
	p := &peerState{peer: cfg.Peers[0], session: newPeerSession(true), g: g}
	pair := cfg.PairContexts[p.peer.Key]
	p.binding, _ = pair.Binding()
	p.session.registration.Store(7)
	address, err := OverlayAddress(p.peer.Key)
	if err != nil {
		t.Fatal(err)
	}
	policy := &bindPolicy{generations: map[netip.Addr]*peerState{address: p}}
	bind.policy.Store(policy)
	authentication := &managedAuthentication{peer: p, generation: g, policy: policy, registration: 7, binding: p.binding}
	p.authenticated.Store(authentication)
	g.peers[p.peer.Key], n.peers[p.peer.Key] = p, p
	g.peerRegistrations[device.PeerRegistration(7)] = p
	g.traffic.Store(true)
	n.generation.Store(g)
	frame, err := resourcegrant.ManagementRequestFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	hello := resourcegrant.ManagementHelloRequest()
	hello[5] = version
	input := append(append([]byte(nil), hello[:]...), frame...)
	raw := &managementInertConn{input: bytes.NewReader(input)}
	endpoint := newLiveEndpoint(g, &managementInertEndpoint{})
	endpoint.raw = raw
	g.live[endpoint.ep] = endpoint
	work := &generationWork{g: g}
	g.work[work] = struct{}{}
	owner := &listener{n: n, service: service{"tcp", ResourceInspectPort}, done: make(chan struct{}), flows: map[*flow]struct{}{}}
	f := &flow{n: n, g: g, c: endpoint, work: work, network: "tcp", inbound: true, local: netip.AddrPortFrom(address, ResourceInspectPort), remote: netip.AddrPortFrom(address, 32001), listener: owner}
	f.listenerIdentity.Store(owner)
	f.w = &wire{g: g, raw: endpoint, flow: f, peer: p, key: p.peer.Key}
	n.wires[f.w] = struct{}{}
	n.flows[f.remote] = map[*flow]struct{}{f: {}}
	n.listeners[owner.service] = owner
	owner.flows[f] = struct{}{}
	capability, ok := captureManagedManagement(f, &ManagementListener{owner: owner})
	if !ok {
		t.Fatal("inert owned capture denied")
	}
	t.Cleanup(func() { _ = capability.Close() })
	handler := context.Background()
	if len(contexts) != 0 {
		handler = contexts[0]
	}
	if err := capability.BindContext(handler); err != nil {
		t.Fatal(err)
	}
	return capability, raw
}

func managementInertReady(t *testing.T, action string) (*ManagedManagement, *managementInertConn, resourcegrant.ManagementRequest, *resourcegrant.ManagementFence) {
	t.Helper()
	request := managementTestRequest(action)
	c, raw := managementInertOwner(t, resourcegrant.ManagementProtocolVersion, request)
	if err := c.Negotiate(); err != nil {
		t.Fatal(err)
	}
	returned, err := c.ReadRequest()
	if err != nil || !c.Current(returned) {
		t.Fatalf("request capture: %v", err)
	}
	now := time.Now()
	record := resourcegrant.Record{ID: request.GrantID, Revision: request.GrantRevision, Target: request.Target, ResourceType: resource.Type,
		Relationship: c.relationship, Actions: []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction},
		Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField}, IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), State: resourcegrant.Active}
	fence, err := resourcegrant.NewManagementFenceBefore(resourcegrant.ManagementRecord{Scope: resourcegrant.Management, Record: record}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return c, raw, returned, fence
}

func managementBorrowCount(c *ManagedManagement) int {
	c.flow.g.mu.Lock()
	defer c.flow.g.mu.Unlock()
	return c.flow.c.borrows
}

func TestManagedManagementRejectsUnownedAndZeroCapabilities(t *testing.T) {
	for _, connection := range []net.Conn{nil, claimedInspectionConnection{}, (*flow)(nil), &flow{}} {
		if c, ok := captureManagedManagement(connection, nil); ok || c != nil {
			t.Fatal("unowned connection minted management capability")
		}
	}
	for _, c := range []*ManagedManagement{nil, {}} {
		if _, _, ok := c.Snapshot(); ok || c.Current(managementTestRequest(resourcegrant.Inspect)) || c.AdmitProvider(nil) {
			t.Fatal("zero capability admitted authority")
		}
		if _, err := c.PrepareReply(nil, resourcegrant.ManagementReply{}); !errors.Is(err, ErrUntrusted) {
			t.Fatal("zero response admitted")
		}
	}
}

func TestManagementListenerRejectsV1BeforeSelectors(t *testing.T) {
	request := managementTestRequest(resourcegrant.ApplyAction)
	c, raw := managementInertOwner(t, resourcegrant.ProtocolVersion, request)
	if err := c.Negotiate(); err != resourcegrant.ErrUnsupported {
		t.Fatalf("legacy negotiation: %v", err)
	}
	frame, _ := resourcegrant.ManagementRequestFrame(request)
	if raw.input.Len() != len(frame) || c.requestReady.Load() {
		t.Fatal("legacy request selectors consumed")
	}
	if err := resourcegrant.ReadHelloResponse(bytes.NewReader(raw.output.Bytes())); err != resourcegrant.ErrUnsupported {
		t.Fatal("legacy client did not receive fixed unsupported hello")
	}
}

func TestManagementRequestImmutableAndProviderOneShot(t *testing.T) {
	c, _, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	*request.Apply.Settings.TransferConcurrentFiles.Value = 8
	if c.Current(request) || *c.request.Apply.Settings.TransferConcurrentFiles.Value != 3 {
		t.Fatal("returned nested choice replaced captured request")
	}
	if !c.AdmitProvider(fence) || c.AdmitProvider(fence) || managementBorrowCount(c) != 0 {
		t.Fatal("provider admission reused or retained endpoint borrow")
	}
	// Acquiring both locks after admission documents return without transport
	// locks; this is deliberately no provider callback or execution fixture.
	c.flow.n.mu.Lock()
	c.flow.g.mu.Lock()
	c.flow.g.mu.Unlock()
	c.flow.n.mu.Unlock()
	fence.Close()
	if _, err := c.PrepareReply(fence, managementTestReply(c.request)); err == nil || managementBorrowCount(c) != 0 {
		t.Fatal("provider admission became disclosure authority")
	}
}

func TestManagementProviderRequiresApplyAndCurrentMembership(t *testing.T) {
	for _, action := range []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.StatusAction} {
		c, _, _, fence := managementInertReady(t, action)
		if c.AdmitProvider(fence) {
			t.Fatal("non-apply admitted provider")
		}
	}
	for _, mutate := range []func(*ManagedManagement){
		func(c *ManagedManagement) { c.flow.n.closing.Store(true) },
		func(c *ManagedManagement) { c.flow.g.traffic.Store(false) },
		func(c *ManagedManagement) { c.flow.g.bind.policy.Store(&bindPolicy{}) },
		func(c *ManagedManagement) { c.flow.w.peer.session.registration.Add(1) },
		func(c *ManagedManagement) { c.flow.w.peer.authenticated.Store(nil) },
		func(c *ManagedManagement) { c.flow.w.peer.binding = strings.Repeat("f", 64) },
		func(c *ManagedManagement) { delete(c.flow.n.wires, c.flow.w) },
		func(c *ManagedManagement) { delete(c.flow.n.flows[c.flow.remote], c.flow) },
		func(c *ManagedManagement) { delete(c.flow.n.listeners, c.owner.service) },
		func(c *ManagedManagement) { c.flow.listenerIdentity.Store(nil) },
		func(c *ManagedManagement) { delete(c.flow.g.live, c.flow.c.ep) },
		func(c *ManagedManagement) { c.flow.c.closing = true },
		func(c *ManagedManagement) { c.flow.w.peer.managementEpoch.digest = strings.Repeat("f", 64) },
	} {
		c, _, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
		mutate(c) // no concurrent runtime exists in this inert owner
		if c.Current(request) || c.AdmitProvider(fence) || c.providerUsed || managementBorrowCount(c) != 0 {
			t.Fatal("stale exact ownership admitted")
		}
	}
}

func TestManagementResponseCloseBeforeWriteAndFrozenFrame(t *testing.T) {
	c, raw, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	reply := managementTestReply(request)
	response, err := c.PrepareReply(fence, reply)
	if err != nil || managementBorrowCount(c) != 1 || raw.writeDeadline.IsZero() {
		t.Fatalf("response prepare: %v", err)
	}
	reply.Operation.OperationID = strings.Repeat("f", 64)
	frozen, err := resourcegrant.ReadManagementReply(bytes.NewReader(response.frame))
	if err != nil || frozen.Operation.OperationID != request.Apply.OperationID {
		t.Fatal("caller changed frozen response")
	}
	_ = response.Close()
	_ = response.Close()
	if response.Write() == nil || managementBorrowCount(c) != 0 {
		t.Fatal("closed response wrote or released twice")
	}
}

func TestManagementResponseConcurrentCloseRetainsWriteBorrow(t *testing.T) {
	c, raw, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	response, err := c.PrepareReply(fence, managementTestReply(request))
	if err != nil {
		t.Fatal(err)
	}
	raw.started, raw.allow = make(chan struct{}), make(chan struct{})
	var allowOnce sync.Once
	allowWrite := func() { allowOnce.Do(func() { close(raw.allow) }) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = response.Write()
	}()
	// Register before any assertion: capability.Close cannot unblock the inert
	// writer. Every exit must first release it, join it, then close the owner.
	t.Cleanup(func() {
		allowWrite()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("writer cleanup did not join")
		}
		_ = response.Close()
	})
	select {
	case <-raw.started:
	case <-time.After(time.Second):
		t.Fatal("write did not begin")
	}
	_ = response.Close()
	if managementBorrowCount(c) != 1 {
		t.Fatal("concurrent close released active writer's borrow")
	}
	select {
	case <-c.flow.c.closeRequested:
	default:
		t.Fatal("concurrent close did not cancel endpoint")
	}
	allowWrite()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer did not return")
	}
	if managementBorrowCount(c) != 0 || response.Write() == nil {
		t.Fatal("completed response retained borrow or wrote twice")
	}
}

func TestManagementResponseSeparateFenceDeadlineAndWriteFailures(t *testing.T) {
	for _, kind := range []string{"fence-before", "fence-after", "deadline", "write-error", "short-write", "generation-seal", "endpoint-close", "capability-close"} {
		t.Run(kind, func(t *testing.T) {
			c, raw, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
			if kind == "fence-before" {
				fence.Close()
			}
			response, err := c.PrepareReply(fence, managementTestReply(request))
			if kind == "fence-before" {
				if err == nil || response != nil || managementBorrowCount(c) != 0 {
					t.Fatal("closed fence admitted response")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Close()
			switch kind {
			case "fence-after":
				fence.Close() // preadmitted fixed frame may finish afterward
			case "deadline":
				response.deadline = time.Now().Add(-time.Second)
			case "write-error":
				raw.writeErr = io.ErrUnexpectedEOF
			case "short-write":
				raw.shortWrite = true
			case "generation-seal":
				c.flow.g.seal(ErrRecovery)
			case "endpoint-close":
				c.flow.c.requestClose()
			case "capability-close":
				_ = c.Close()
			}
			err = response.Write()
			if (kind == "fence-after") != (err == nil) || managementBorrowCount(c) != 0 {
				t.Fatalf("write/release mismatch: %v", err)
			}
		})
	}
}

func TestManagementBindContextSingleUseAndCancellation(t *testing.T) {
	c, _, _, fence := managementInertReady(t, resourcegrant.ApplyAction)
	if c.BindContext(context.Background()) == nil || c.AdmitProvider(fence) {
		t.Fatal("rebound context extended authority")
	}
	c, _, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	cancelled := make(chan struct{})
	close(cancelled)
	c.flow.n.mu.Lock()
	c.requestDone = cancelled
	c.flow.n.mu.Unlock()
	if c.Current(request) || c.AdmitProvider(fence) {
		t.Fatal("captured cancellation ignored")
	}
}

func TestManagementReplyCorrespondence(t *testing.T) {
	for _, action := range []string{resourcegrant.Inspect, resourcegrant.PreviewAction, resourcegrant.ApplyAction, resourcegrant.StatusAction} {
		request := managementTestRequest(action)
		reply := managementTestReply(request)
		if !managementReplyMatches(request, reply) {
			t.Fatal("exact response denied")
		}
		reply.GrantRevision++
		if managementReplyMatches(request, reply) {
			t.Fatal("other selector accepted")
		}
	}
	for _, action := range []string{resourcegrant.ApplyAction, resourcegrant.StatusAction} {
		request := managementTestRequest(action)
		reply := managementTestReply(request)
		if reply.Operation != nil {
			reply.Operation.OperationID = strings.Repeat("f", 64)
		} else {
			reply.Unavailable.OperationID = strings.Repeat("f", 64)
		}
		if managementReplyMatches(request, reply) {
			t.Fatal("other operation accepted")
		}
	}
}

func TestManagementCaptureCannotMintSecondProtocolCapability(t *testing.T) {
	c, _ := managementInertOwner(t, resourcegrant.ManagementProtocolVersion, managementTestRequest(resourcegrant.Inspect))
	if other, ok := captureManagedInspection(c.flow, c.owner); ok || other != nil {
		t.Fatal("management flow minted legacy inspection capability")
	}
	if other, ok := captureManagedManagement(c.flow, &ManagementListener{owner: c.owner}); ok || other != nil {
		t.Fatal("management flow minted second management capability")
	}
	// A synthetic prior inspection capture also denies management. This flag
	// narrows admission only and cannot create any authentication by itself.
	c.flow.n.mu.Lock()
	c.flow.resourceManagementCaptured = false
	c.flow.resourceInspectionCaptured = true
	c.flow.n.mu.Unlock()
	if other, ok := captureManagedManagement(c.flow, &ManagementListener{owner: c.owner}); ok || other != nil {
		t.Fatal("inspection flow minted management capability")
	}
}

func managementAdditionalCapture(t *testing.T, prior *ManagedManagement) *ManagedManagement {
	t.Helper()
	n, g, owner, p := prior.flow.n, prior.flow.g, prior.owner, prior.flow.w.peer
	endpoint := newLiveEndpoint(g, &managementInertEndpoint{})
	endpoint.raw = &managementInertConn{input: bytes.NewReader(nil)}
	g.live[endpoint.ep] = endpoint
	work := &generationWork{g: g}
	g.work[work] = struct{}{}
	f := &flow{n: n, g: g, c: endpoint, work: work, network: "tcp", inbound: true, local: prior.flow.local,
		remote: netip.AddrPortFrom(prior.flow.remote.Addr(), prior.flow.remote.Port()+1), listener: owner}
	f.listenerIdentity.Store(owner)
	f.w = &wire{g: g, raw: endpoint, flow: f, peer: p, key: p.peer.Key}
	n.wires[f.w] = struct{}{}
	n.flows[f.remote] = map[*flow]struct{}{f: {}}
	owner.flows[f] = struct{}{}
	next, ok := captureManagedManagement(f, &ManagementListener{owner: owner})
	if !ok {
		t.Fatal("second owned flow rejected")
	}
	t.Cleanup(func() { _ = next.Close() })
	if err := next.BindContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestManagementConcreteEpochStableThenReplaced(t *testing.T) {
	first, _, request, _ := managementInertReady(t, resourcegrant.ApplyAction)
	second := managementAdditionalCapture(t, first)
	if first.epoch != second.epoch || !first.Current(request) {
		t.Fatal("new TCP connection replaced stable authentication epoch")
	}
	p := first.flow.w.peer
	copy := *p.authenticated.Load()
	p.authenticated.Store(&copy)
	third := managementAdditionalCapture(t, second)
	if third.epoch != first.epoch {
		t.Fatal("repeated equal authentication replaced epoch")
	}
	p.session.registration.Add(1)
	copy.registration++
	p.authenticated.Store(&copy)
	first.flow.g.peerRegistrations[device.PeerRegistration(copy.registration)] = p
	fourth := managementAdditionalCapture(t, third)
	if fourth.epoch.digest == first.epoch.digest || first.Current(request) || p.managementEpoch != fourth.epoch {
		t.Fatal("replacement did not invalidate old epoch/capability")
	}
}

func TestManagementPreparedCancellationAndDeadlineFailureRelease(t *testing.T) {
	c, _, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	cancelled := make(chan struct{})
	c.flow.n.mu.Lock()
	c.requestDone = cancelled
	c.flow.n.mu.Unlock()
	response, err := c.PrepareReply(fence, managementTestReply(request))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Close()
	close(cancelled)
	if response.Write() == nil || managementBorrowCount(c) != 0 {
		t.Fatal("prepared cancellation wrote or retained borrow")
	}
	c, raw, request, fence := managementInertReady(t, resourcegrant.ApplyAction)
	raw.deadlineErr = io.ErrClosedPipe
	if response, err := c.PrepareReply(fence, managementTestReply(request)); err == nil || response != nil || managementBorrowCount(c) != 0 {
		t.Fatal("failed deadline retained or admitted response")
	}
}

func TestManagementBoundCancellationChannelDeniesImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := managementInertOwner(t, resourcegrant.ManagementProtocolVersion, managementTestRequest(resourcegrant.Inspect), ctx)
	if err := c.Negotiate(); err != nil {
		t.Fatal(err)
	}
	request, err := c.ReadRequest()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// The final gate observes the pre-captured Done channel independently of
	// whether context.AfterFunc has had an opportunity to close the endpoint.
	if c.Current(request) || c.AdmitProvider(nil) {
		t.Fatal("cancelled context retained authority")
	}
}
