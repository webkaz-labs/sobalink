//go:build resource_inspection_native && directlan_activation_native

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Execution requires the exact opt-in and selector below. This fixture uses
// the activation helper's retained-directory mode for uncertain cleanup.
// Each test creates only synthetic temporary profiles and loopback peers. No
// UI/IPC listener, subprocess, real profile, external service or OS policy is
// used. Full restart below means Core.Close/Open, not an OS-process restart.
const resourceInspectionNativeSelector = "^TestResourceInspectionNative(FirstRemoteInspect|RestartPreservesOriginalExpiry|ReverseRestartPreservesOriginalExpiry|RevokeDeniesNewInspection|PortCollisionPreservesOwner)$"

// Own the lifecycle lock until this exact Core.Close has returned. A timeout
// never releases it early. once also joins a pending restart close on failure.
type resourceInspectionNativeOwner struct {
	core *Core
	lock *config.Lock
	once sync.Once
	done chan struct{}
	err  error
}

func (o *resourceInspectionNativeOwner) startClose() {
	o.once.Do(func() {
		go func() {
			if o.core != nil {
				o.err = o.core.Close()
			}
			if o.lock != nil {
				o.err = errors.Join(o.err, o.lock.Close())
			}
			close(o.done)
		}()
	})
}

type resourceInspectionNativePair struct {
	*activationNativePair
	owners      [2]*resourceInspectionNativeOwner
	initialized bool
}

func newResourceInspectionNativePair(t *testing.T) *resourceInspectionNativePair {
	t.Helper()
	// Every guard precedes the existing helper's temp state, sockets and Core.
	if os.Getenv("SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE") != "reviewed-production-loopback-v1" {
		t.Skip("requires separately reviewed native inspection execution")
	}
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" {
		t.Fatal("explicit native activation prerequisite is missing")
	}
	run := flag.Lookup("test.run")
	if run == nil || run.Value.String() != resourceInspectionNativeSelector && run.Value.String() != "^"+t.Name()+"$" {
		t.Fatal("exact native inspection selector is required")
	}
	if deadline, ok := t.Deadline(); !ok || time.Until(deadline) > 8*time.Minute {
		t.Fatal("a nonzero test-run timeout of at most eight minutes is required")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY"} {
		if os.Getenv(name) != "" {
			t.Fatal("native fixture requires an isolated environment without proxy overrides")
		}
	}
	return newResourceNativeOwnedPair(t)
}

// Private setup body shared only after each native suite's independent guard.
// Keep the inspection entry guard above unchanged when adding another suite.
func newResourceNativeOwnedPair(t *testing.T) *resourceInspectionNativePair {
	t.Helper()
	return newResourceNativeOwnedPairWithPolicy(t, false)
}

// Only the separately guarded management suite aligns this synthetic fixture
// with Core's actual policy before activation. Inspection keeps its old setup.
func newResourceNativeOwnedPairWithPolicy(t *testing.T, alignPolicy bool) *resourceInspectionNativePair {
	t.Helper()
	base := newActivationNativePairDirectories(t, true) // 90-second total pair context; no address reselection after this allocation.
	f := &resourceInspectionNativePair{activationNativePair: base}
	for i, c := range base.cores {
		f.owners[i] = &resourceInspectionNativeOwner{core: c, done: make(chan struct{})}
	}
	// This is registered after all helper directory cleanups. It consumes the helper's
	// close-once owner so its earlier cleanups cannot release the wrong lifetime.
	// Removal is conditional on joined cleanup; failures retain synthetic state.
	t.Cleanup(f.closeOwned)
	for i, c := range base.cores {
		if base.endpoints[i].Addr() != netip.MustParseAddr("127.0.0.1") {
			t.Fatal("fixture endpoint left the exact loopback address")
		}
		owner, err := config.AcquireLock(c.dir)
		if err != nil {
			t.Fatal("synthetic lifecycle lock acquisition failed")
		}
		f.owners[i].lock = owner
		c.op.Lock()
		if alignPolicy {
			// Reload the SAME fixture file through the normal loader with the
			// policy that the real provider will later use. No authority exists
			// yet, and this never sets a limits pointer, epoch or receipt itself.
			limits := selectedLANLimits(c.capacityPolicy())
			store, err := readDirectLANStore(filepath.Join(c.dir, "direct-lan.json"), limits.bytes, limits.peers)
			if err != nil || store == nil || store.limits.Load() == nil || *store.limits.Load() != *limits {
				c.op.Unlock()
				t.Fatal("synthetic policy-aligned production store load failed")
			}
			c.mu.Lock()
			offline := c.node == nil && c.contextControl == nil && c.contextUpgrade == nil
			if offline {
				c.directLAN = store
			}
			c.mu.Unlock()
			if !offline {
				c.op.Unlock()
				t.Fatal("synthetic policy alignment attempted after activation")
			}
		}
		c.initializeResourceIdentity(owner)
		c.initializeResourceGrants()
		valid := c.resourceIdentity != "" && c.resourceLock == owner && c.resourceGrants != nil && !c.resourceGrants.frozen && c.resourceGrants.firstUse
		// The activation helper changes this profile only in memory. Persist the
		// exact synthetic network choice using the owned production writer so a
		// later normal Open cannot silently restart as network:none.
		err = owner.WithOwnership(c.dir, func() error { return c.saveProfile(c.profileCopy()) })
		c.op.Unlock()
		if !valid || err != nil {
			t.Fatal("owned resource initialization or synthetic profile save failed")
		}
	}
	deadline := time.Now().Add(35 * time.Second)
	a, b := base.run(0, deadline), base.run(1, deadline)
	base.await(a)
	base.await(b)
	base.assertOrdinary(0, a)
	base.assertOrdinary(1, b)
	f.initialized = true
	return f
}

func (f *resourceInspectionNativePair) closeOwned() {
	f.once.Do(func() {
		f.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		joined := f.initialized
		for _, o := range f.owners {
			if o != nil {
				o.startClose()
			}
		}
		for _, o := range f.owners {
			if o == nil {
				continue
			}
			select {
			case <-o.done:
				if o.err != nil {
					joined = false
					f.t.Error("native inspection Core/lock cleanup failed")
				}
			case <-ctx.Done():
				joined = false
				f.t.Error("native inspection cleanup did not join; lock is retained until Core.Close returns")
			}
		}
		for _, r := range f.reservations {
			if r != nil && r.Close() != nil {
				joined = false
				f.t.Error("native inspection endpoint reservation cleanup failed")
			}
		}
		if !joined {
			f.cleanupFailed.Store(true)
		}
		f.cleanupJoined.Store(joined && !f.cleanupFailed.Load())
	})
}

func (f *resourceInspectionNativePair) local(i int, name string, input any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		f.t.Fatal("synthetic command encoding failed")
	}
	budget := 8 * time.Second
	if name == "resource.remote.inspect" {
		budget = 20 * time.Second // Production retains its fixed total 15-second bound.
	}
	ctx, cancel := context.WithTimeout(f.ctx, budget)
	defer cancel()
	if name == "resource.remote.inspect" {
		observed, log := directlan.WithInspectionObservation(ctx)
		value, err := f.cores[i].Command(observed, webui.Command{RequestID: "synthetic-resource-inspection", Name: name, Payload: raw})
		if event, seen := log.Result(); seen {
			f.t.Logf("inspection phase=%s code=%s elapsed=%s dial=%s ready_before=%t authenticated_before=%t", event.Phase, event.Code, event.Elapsed, event.DialPhase, event.ReadyBeforeEnsure, event.AuthenticatedBeforeEnsure)
		} else {
			f.t.Log("inspection phase=before_transport")
		}
		return value, err
	}
	return f.cores[i].Command(ctx, webui.Command{RequestID: "synthetic-resource-inspection", Name: name, Payload: raw})
}

func (f *resourceInspectionNativePair) mustLocal(i int, name string, input any) any {
	f.t.Helper()
	value, err := f.local(i, name, input)
	if err != nil {
		f.t.Fatalf("%s failed: code=%s", name, networkErrorCode(err))
	}
	return value
}

func (f *resourceInspectionNativePair) descriptor(i int) resource.Descriptor {
	f.t.Helper()
	catalog, ok := f.mustLocal(i, "resource.list", struct{}{}).(resource.Catalog)
	if !ok || catalog.SchemaVersion != resource.SchemaVersion || len(catalog.Resources) != 1 {
		f.t.Fatal("local resource catalog did not expose exactly the synthetic resource")
	}
	return catalog.Resources[0]
}

func (f *resourceInspectionNativePair) view(i int) resourcegrant.LocalGrantView {
	f.t.Helper()
	value := f.mustLocal(i, "resource.grant.inspect", map[string]any{"target": f.descriptor(i).Target})
	view, ok := value.(resourcegrant.LocalGrantView)
	if !ok || view.TimeUncertain {
		f.t.Fatal("local grant view unavailable or clock uncertainty observed")
	}
	return view
}

func (f *resourceInspectionNativePair) confirm(i int, lifetime time.Duration) resourcegrant.Record {
	f.t.Helper()
	input := resourcegrant.GrantInputs{
		Target: f.descriptor(i).Target, PeerKey: f.cores[1-i].directLAN.copy().Identity.PublicKey(),
		ExpiresAt: time.Now().Add(lifetime).Unix(), Actions: []string{resourcegrant.Inspect},
		Fields: []string{resourcegrant.FilesField, resourcegrant.PerPeerField},
	}
	value := f.mustLocal(i, "resource.grant.preview", input)
	review, ok := value.(resourcegrant.GrantReview)
	if !ok || !review.InitializesState || review.Grant.Validate() != nil || review.Grant.ExpiresAt != input.ExpiresAt || review.Grant.Target != input.Target ||
		review.Grant.Relationship.PeerKey != input.PeerKey || !reflect.DeepEqual(review.Grant.Actions, input.Actions) || !reflect.DeepEqual(review.Grant.Fields, input.Fields) {
		f.t.Fatal("synthetic first-use preview did not retain exact reviewed scope")
	}
	if _, err := os.Stat(filepath.Dir(resourceGrantStatePath(f.cores[i].dir))); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal("preview created grant storage")
	}
	value = f.mustLocal(i, "resource.grant.confirm", resourcegrant.GrantConfirmation{Review: review, Confirm: true})
	view, ok := value.(resourcegrant.LocalGrantView)
	if !ok || view.InitializesState || len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], review.Grant) {
		f.t.Fatal("confirmation did not preserve the exact reviewed grant")
	}
	f.assertSaved(i, review.Grant)
	return review.Grant
}

func (f *resourceInspectionNativePair) assertSaved(i int, want resourcegrant.Record) {
	f.t.Helper()
	data, err := os.ReadFile(resourceGrantStatePath(f.cores[i].dir))
	if err != nil {
		f.t.Fatal("synthetic saved grant could not be read")
	}
	state, err := resourcegrant.Decode(data)
	if err != nil || len(state.Records) != 1 || !reflect.DeepEqual(state.Records[0], want) || *state.HighWater != want.Revision {
		f.t.Fatal("durable grant differs from the expected exact record")
	}
}

// Poll only local listener retirement/reconciliation, which is asynchronous.
// This never retries the outbound inspection, creates new consent, changes an
// endpoint, or calls private runtime reconciliation directly.
func (f *resourceInspectionNativePair) awaitListening(i int, original resourcegrant.Record) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		view := f.view(i)
		if len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], original) {
			f.t.Fatal("listener readiness changed the original grant")
		}
		if view.ListenerReady && view.Activation == "listening" {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("listener did not become ready before its bounded readiness deadline")
		case <-tick.C:
		}
	}
}

func resourceInspectionNativeInput(record resourcegrant.Record) resourcegrant.RemoteInspectInput {
	return resourcegrant.RemoteInspectInput{PeerKey: record.Relationship.TargetKey, Request: resourcegrant.InspectRequest{
		ProtocolVersion: resourcegrant.ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision,
	}}
}

func (f *resourceInspectionNativePair) inspectOnce(requester, target int, record resourcegrant.Record) {
	f.t.Helper()
	local := f.descriptor(target)
	// Exactly one outbound inspection, without a deliberate session warm-up.
	// A first-call handshake timeout is a failure; no retry or fallback.
	value := f.mustLocal(requester, "resource.remote.inspect", resourceInspectionNativeInput(record))
	got, ok := value.(resourcegrant.Inspection)
	if !ok || got.Validate() != nil || got.ProtocolVersion != resourcegrant.ProtocolVersion || got.Target != local.Target ||
		!reflect.DeepEqual(got.Requested, local.Requested) || got.Effective != local.Effective {
		f.t.Fatal("remote inspection did not return the exact local two-field projection")
	}
	data, err := json.Marshal(got)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &fields) != nil || len(fields) != 4 || fields["protocolVersion"] == nil || fields["target"] == nil || fields["requested"] == nil || fields["effective"] == nil {
		f.t.Fatal("remote inspection exposed an unexpected top-level field")
	}
	for _, name := range []string{"requested", "effective"} {
		var projection map[string]json.RawMessage
		if json.Unmarshal(fields[name], &projection) != nil || len(projection) != 2 || projection[resourcegrant.FilesField] == nil || projection[resourcegrant.PerPeerField] == nil {
			f.t.Fatal("remote inspection exposed an unexpected settings field")
		}
	}
	f.cores[requester].requestMu.Lock()
	cached := len(f.cores[requester].requests)
	f.cores[requester].requestMu.Unlock()
	if cached != 0 {
		f.t.Fatal("inspection response was retained in local request history")
	}
}

func (f *resourceInspectionNativePair) denyOnce(requester int, record resourcegrant.Record) {
	f.t.Helper()
	value, err := f.local(requester, "resource.remote.inspect", resourceInspectionNativeInput(record))
	if value != nil || networkErrorCode(err) != "resource_remote_unavailable" {
		f.t.Fatal("denied grant returned inspection data or a nongeneric remote result")
	}
}

// Compare retained permission/identity data, not incidental observation or
// publication bookkeeping. These are read-only snapshots; no store is changed.
func resourceInspectionNativeSameAuthority(a, b directLANState) bool {
	if validateDirectLANState(a) != nil || validateDirectLANState(b) != nil || a.Version != b.Version || a.Identity != b.Identity ||
		!reflect.DeepEqual(a.Selection, b.Selection) || !reflect.DeepEqual(a.Peers, b.Peers) || a.Metadata == nil || b.Metadata == nil {
		return false
	}
	x, y := a.Metadata, b.Metadata
	if x.Version != y.Version || x.LocalPeer != y.LocalPeer || !reflect.DeepEqual(x.LocalScope, y.LocalScope) ||
		x.PreviousLocalEndpoint != y.PreviousLocalEndpoint || !reflect.DeepEqual(x.PendingChange, y.PendingChange) || len(x.Peers) != len(y.Peers) {
		return false
	}
	for i, left := range x.Peers {
		right := y.Peers[i]
		if left.Peer != right.Peer || left.ContextConfirmed != right.ContextConfirmed ||
			!reflect.DeepEqual(left.PairContext, right.PairContext) || !reflect.DeepEqual(left.EndpointState, right.EndpointState) ||
			!reflect.DeepEqual(left.PairRevocation, right.PairRevocation) || !reflect.DeepEqual(left.UpgradePending, right.UpgradePending) {
			return false
		}
	}
	return true
}

func resourceInspectionNativeSameProfile(a, b Profile) bool {
	return a.Version == b.Version && a.Settings.Network == b.Settings.Network && a.Settings.Hostname == b.Settings.Hostname &&
		a.Settings.ReceiveDirectory == b.Settings.ReceiveDirectory && reflect.DeepEqual(a.Peers, b.Peers) &&
		reflect.DeepEqual(a.Services, b.Services) && reflect.DeepEqual(a.Groups, b.Groups)
}

func (f *resourceInspectionNativePair) reopen(i int) {
	f.t.Helper()
	old := f.cores[i]
	oldBackend := old.nodeCopy().(*directLANBackend)
	oldCompletion := oldBackend.currentCompletion()
	profile, state := old.profileCopy(), old.directLAN.copy()
	dir, nonce, resourceID := old.dir, old.lanStartNonce, f.descriptor(i).ResourceID
	owner := f.owners[i]
	owner.startClose()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	select {
	case <-owner.done:
		if owner.err != nil {
			f.t.Fatal("target close or old lifecycle-lock release failed; no reopen attempted")
		}
	case <-ctx.Done():
		f.t.Fatal("target close did not join; no endpoint or lock handoff attempted")
	}
	// Reacquire the same directory; never replace saved bytes or choose another
	// endpoint. The ordinary production Open owns fresh runtime certification.
	lock, err := config.AcquireLock(dir)
	if err != nil {
		f.t.Fatal("same-directory lifecycle lock could not be reacquired")
	}
	freshOwner := &resourceInspectionNativeOwner{lock: lock, done: make(chan struct{})}
	f.owners[i], f.cores[i] = freshOwner, nil
	reopened, err := Open(f.ctx, Options{
		Directory: dir, Version: "synthetic-resource-inspection", LifecycleLock: lock, EnableResourceInspection: true,
		NodeFactory: func(string, string) (NetworkBackend, error) {
			return nil, errors.New("non-direct synthetic backend is forbidden")
		},
	})
	freshOwner.core, f.cores[i] = reopened, reopened // Cleanup always owns the newest Core, including an unexpected error result.
	if err != nil || reopened == nil {
		f.t.Fatal("normal same-directory Core reopen failed")
	}
	if reopened.lanStartNonce == nonce || f.descriptor(i).ResourceID != resourceID || !resourceInspectionNativeSameProfile(profile, reopened.profileCopy()) || !resourceInspectionNativeSameAuthority(state, reopened.directLAN.copy()) {
		f.t.Fatal("reopen changed retained identity/permissions/endpoints or reused the old process nonce")
	}
	reopened.op.Lock()
	backend, ok := reopened.nodeCopy().(*directLANBackend)
	valid := ok && backend != nil && backend != oldBackend && backend.Node != nil && backend.Node != oldBackend.Node &&
		backend.currentCompletion() != nil && backend.currentCompletion() != oldCompletion && backend.currentCompletion().activationCurrent() && backend.Node.Endpoint() == f.endpoints[i]
	reopened.op.Unlock()
	if !valid {
		f.t.Fatal("reopen did not acquire a fresh ordinary managed owner at the original fixed endpoint")
	}
}

func TestResourceInspectionNativeFirstRemoteInspect(t *testing.T) {
	f := newResourceInspectionNativePair(t)
	record := f.confirm(1, 30*time.Second)
	f.awaitListening(1, record)
	f.inspectOnce(0, 1, record)
	// The two selectors remain narrowing preconditions, not reusable authority.
	wrong := record
	wrong.Revision++
	f.denyOnce(0, wrong)
	f.assertSaved(1, record)
}

func resourceInspectionNativeRestart(t *testing.T, target int) {
	t.Helper()
	requester := 1 - target
	f := newResourceInspectionNativePair(t)
	record := f.confirm(target, 30*time.Second)
	f.awaitListening(target, record)
	f.inspectOnce(requester, target, record)
	f.reopen(target)
	f.awaitListening(target, record)
	f.assertSaved(target, record)
	f.inspectOnce(requester, target, record) // One actual post-restart call; no handshake retry.
	// Exercise real elapsed time. Never edit a clock, grant, fence or expiry.
	timer := time.NewTimer(time.Until(time.Unix(record.ExpiresAt, 0)) + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-f.ctx.Done():
		t.Fatal("fixture deadline reached before the original grant expiry")
	case <-timer.C:
	}
	view := f.view(target)
	if view.ListenerReady || view.Activation != "expired" || len(view.Records) != 1 {
		t.Fatal("original expiry did not close listener admission")
	}
	expired := view.Records[0]
	want := record
	want.State, want.Revision = resourcegrant.Expired, record.Revision+1
	if !reflect.DeepEqual(expired, want) {
		t.Fatal("expiry changed scope, identity or the original deadline")
	}
	f.assertSaved(target, expired)
	f.denyOnce(requester, record)
	f.reopen(target)
	view = f.view(target)
	if view.ListenerReady || view.Activation != "expired" || len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], expired) {
		t.Fatal("restart resurrected the durably expired original grant")
	}
	f.denyOnce(requester, record)
}

func TestResourceInspectionNativeRestartPreservesOriginalExpiry(t *testing.T) {
	resourceInspectionNativeRestart(t, 1)
}
func TestResourceInspectionNativeReverseRestartPreservesOriginalExpiry(t *testing.T) {
	resourceInspectionNativeRestart(t, 0)
}

func TestResourceInspectionNativeRevokeDeniesNewInspection(t *testing.T) {
	f := newResourceInspectionNativePair(t)
	record := f.confirm(1, 45*time.Second)
	f.awaitListening(1, record)
	f.inspectOnce(0, 1, record)
	value := f.mustLocal(1, "resource.grant.revoke", resourcegrant.GrantRevoke{Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision, Confirm: true})
	view, ok := value.(resourcegrant.LocalGrantView)
	want := record
	want.State, want.Revision = resourcegrant.Revoked, record.Revision+1
	if !ok || view.ListenerReady || len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], want) {
		t.Fatal("revoke did not retain an exact terminal tombstone")
	}
	f.assertSaved(1, want)
	f.denyOnce(0, record)
	f.reopen(1)
	view = f.view(1)
	if view.ListenerReady || len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], want) {
		t.Fatal("restart resurrected the revoked grant")
	}
	f.denyOnce(0, record)
}

// One ordinary userspace exchange, used only for collision preservation. This
// intentionally warms that case's managed session; the first-call acceptance
// and restart cases above never use it. There is no OS service-dial fallback.
func (f *resourceInspectionNativePair) echo(listener net.Listener) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	marker := []byte("synthetic-existing-port-owner")
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done) // Runs after the accepted connection's close below.
		conn, err := listener.Accept()
		if err != nil {
			serveErr = err
			return
		}
		defer conn.Close()
		deadline, _ := ctx.Deadline()
		if serveErr = conn.SetDeadline(deadline); serveErr != nil {
			return
		}
		got := make([]byte, len(marker))
		if _, serveErr = io.ReadFull(conn, got); serveErr == nil && !bytes.Equal(got, marker) {
			serveErr = errors.New("unexpected synthetic ordinary-service bytes")
		}
		if serveErr == nil {
			var n int
			n, serveErr = conn.Write(marker)
			if serveErr == nil && n != len(marker) {
				serveErr = io.ErrShortWrite
			}
		}
	}()
	// Cleanup is registered before dialing, so all failure paths close the
	// exact listener and join this one accept worker before owned directory cleanup.
	f.t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			f.cleanupFailed.Store(true)
			f.t.Error("ordinary collision listener cleanup failed")
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			f.cleanupFailed.Store(true)
			f.t.Error("ordinary collision worker did not join")
		}
	})
	backend := f.cores[0].nodeCopy().(*directLANBackend)
	peer := f.cores[1].directLAN.copy().Identity.PublicKey()
	conn, err := backend.Node.DialPeer(ctx, peer, "tcp", directlan.ResourceInspectPort)
	if err != nil {
		f.t.Fatal("ordinary synthetic port owner could not be reached")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		f.t.Fatal("ordinary collision exchange deadline failed")
	}
	if n, err := conn.Write(marker); err != nil || n != len(marker) {
		f.t.Fatal("ordinary synthetic service write failed")
	}
	got := make([]byte, len(marker))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, marker) {
		f.t.Fatal("ordinary synthetic service behavior changed")
	}
	select {
	case <-done:
		if serveErr != nil {
			f.t.Fatal("ordinary synthetic service exchange failed")
		}
	case <-ctx.Done():
		f.cleanupFailed.Store(true)
		f.t.Fatal("ordinary synthetic service exchange did not join")
	}
}

func TestResourceInspectionNativePortCollisionPreservesOwner(t *testing.T) {
	f := newResourceInspectionNativePair(t)
	backend := f.cores[1].nodeCopy().(*directLANBackend)
	listener, err := backend.Node.ListenPeer(f.ctx, "tcp", directlan.ResourceInspectPort)
	if err != nil {
		t.Fatal("ordinary synthetic inspection-port owner could not start")
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			f.cleanupFailed.Store(true)
			t.Error("ordinary collision listener cleanup failed")
		}
	})
	f.echo(listener)
	record := f.confirm(1, 30*time.Second)
	view := f.view(1)
	if view.ListenerReady || view.Activation != "port_conflict" || len(view.Records) != 1 || !reflect.DeepEqual(view.Records[0], record) {
		t.Fatal("inspection displaced the existing owner or misreported its collision")
	}
	f.echo(listener) // The same original owner must still carry its own bytes.
	f.assertSaved(1, record)
	if err := listener.Close(); err != nil {
		f.cleanupFailed.Store(true)
		t.Fatal("ordinary collision owner release failed")
	}
	// Only the same reviewed grant may activate at the same fixed virtual port.
	f.awaitListening(1, record)
	f.inspectOnce(0, 1, record)
	f.assertSaved(1, record)
}
