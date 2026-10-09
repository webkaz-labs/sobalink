package core

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// A single serial handler bounds handshake work. It is deliberately outside
// Core.wg: it can wait for Core.op and must be joined only after op is released.
type resourceInspectionRuntime struct {
	node         *directlan.Node
	listener     *directlan.InspectionListener
	relationship resourcegrant.Relationship
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
	mu           sync.Mutex
	active       *directlan.ManagedInspection
}

func (r *resourceInspectionRuntime) stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.cancel()
		_ = r.listener.Close()
		r.mu.Lock()
		active := r.active
		r.mu.Unlock()
		if active != nil {
			_ = active.Close()
		}
	})
}
func (g *resourceGrantCoordinator) retireRuntime() {
	g.retireInspectionRuntime()
	g.retireManagementRuntime()
}
func (g *resourceGrantCoordinator) retireInspectionRuntime() {
	if g == nil || g.runtime == nil {
		return
	}
	g.runtime.stop()
	// A replacement is not started until the previous retiring owner completes.
	g.retiring, g.runtime = g.runtime, nil
}
func inspectionRuntimeDone(r *resourceInspectionRuntime) bool {
	if r == nil {
		return true
	}
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}
func (c *Core) withResourceInspectionState(fn func(*resourcePathBinding) error) error {
	if c.resourceLock == nil || c.resourceGrants == nil {
		return resourcegrant.ErrInvalid
	}
	var callbackErr error
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			return errResourceBinding
		}
		if err := c.currentResourceGrantsBound(b); err != nil {
			return err
		}
		if err := c.observeResourceGrantsBound(b, time.Now()); err != nil {
			return err
		}
		callbackErr = fn(b)
		return b.check()
	})
	if err != nil {
		c.resourceGrants.freeze()
		return err
	}
	return callbackErr
}
func activeResourceGrant(g *resourceGrantCoordinator) (resourcegrant.Record, bool) {
	for _, record := range g.state.Records {
		if record.State == resourcegrant.Active {
			return record, true
		}
	}
	return resourcegrant.Record{}, false
}

// reconcileResourceInspection runs under Core.op, immediately after explicit
// confirm and in the existing maintenance pass. The tick provides cleanup and
// retry, never an expiry grace period or a new per-boot grant lifetime.
func (c *Core) reconcileResourceInspection() {
	g := c.resourceGrants
	if g == nil {
		return
	}
	if managementRuntimeDone(g.managementRetiring) {
		g.managementRetiring = nil
	}
	if _, management := g.state.ActiveRecord(); management {
		g.fence.Close()
		g.retireInspectionRuntime()
		c.reconcileResourceManagement()
		return
	}
	g.managementFence.Close()
	g.retireManagementRuntime()
	inactive := func(reason string) { g.activation = reason; g.retireRuntime() }
	if g.frozen {
		inactive("storage_uncertain")
		return
	}
	if c.ctx.Err() != nil {
		g.fence.Close()
		inactive("stopping")
		return
	}
	if g.firstUse {
		inactive("no_grant")
		return
	}
	if inspectionRuntimeDone(g.retiring) {
		g.retiring = nil
	}
	if g.runtime != nil && inspectionRuntimeDone(g.runtime) {
		g.retireRuntime()
	}
	var record resourcegrant.Record
	var relationship resourcegrant.Relationship
	var relationshipErr error
	err := c.withResourceInspectionState(func(*resourcePathBinding) error {
		record, _ = activeResourceGrant(g)
		if record.ID != "" {
			relationship, relationshipErr = c.resourceGrantRelationship(record.Relationship.PeerKey, time.Now())
		}
		return nil
	})
	if err != nil {
		g.freeze()
		inactive("storage_uncertain")
		return
	}
	if g.timeUncertain {
		inactive("clock_uncertain")
		return
	}
	if record.ID == "" {
		latestState := ""
		var latestRevision uint64
		for _, retained := range grantRecordData(g.state) {
			if retained.Revision > latestRevision {
				latestRevision, latestState = retained.Revision, retained.State
			}
		}
		if latestState == resourcegrant.Expired {
			inactive("expired")
			return
		}
		inactive("no_active_grant")
		return
	}
	if g.bootGrantID != record.ID || g.bootRevision != record.Revision || g.bootDeadline.IsZero() {
		g.freeze()
		inactive("storage_uncertain")
		return
	}
	if g.relationshipDenied {
		inactive("relationship_changed")
		return
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	if !ok || backend == nil || backend.Node == nil || c.profileCopy().Settings.Network != "direct-lan" {
		inactive("backend_unavailable")
		return
	}
	if relationshipErr != nil {
		inactive("relationship_unavailable")
		return
	}
	if relationship != record.Relationship {
		g.relationshipDenied = true
		g.fence.Close()
		inactive("relationship_changed")
		return
	}
	// Relationship projection may do owned store work. Observe again before
	// minting; detected anomalies are persisted through the owned state path.
	fresh := time.Now()
	observationErr := g.observe(fresh)
	timingExpired, timingUncertain := g.fence.TimingObservation(resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision})
	if observationErr != nil || fresh.Unix() >= record.ExpiresAt || !fresh.Before(g.bootDeadline) || timingExpired || timingUncertain {
		if err := c.withResourceInspectionState(func(*resourcePathBinding) error { return nil }); err != nil {
			g.freeze()
			inactive("storage_uncertain")
		} else if g.timeUncertain {
			inactive("clock_uncertain")
		} else {
			inactive("expired")
		}
		return
	}
	if g.runtime != nil {
		if g.runtime.node != backend.Node {
			inactive("reconnecting")
		} else {
			g.activation = "listening"
		}
		return
	}
	if g.retiring != nil || g.managementRetiring != nil {
		g.activation = "stopping_previous_listener"
		return
	}
	if g.fence == nil {
		g.fence, err = resourcegrant.NewDisclosureFenceBefore(record, g.bootDeadline)
		if err != nil {
			inactive("activation_unavailable")
			return
		}
	}
	ctx, cancel := context.WithDeadline(c.ctx, g.bootDeadline)
	listener, err := backend.Node.ListenInspection(ctx)
	if err != nil {
		cancel()
		if errors.Is(err, directlan.ErrInspectionConflict) {
			inactive("port_conflict")
			return
		}
		inactive("listener_unavailable")
		return
	}
	runtime := &resourceInspectionRuntime{node: backend.Node, listener: listener, relationship: relationship, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	g.runtime, g.activation = runtime, "listening"
	go c.runResourceInspection(runtime)
}
func (c *Core) runResourceInspection(runtime *resourceInspectionRuntime) {
	defer close(runtime.done)
	defer runtime.stop()
	for {
		capability, err := runtime.listener.Accept()
		if err != nil {
			if errors.Is(err, directlan.ErrUntrusted) && inspectionRejectBackoff(runtime.ctx) {
				continue
			}
			return
		}
		// Reject a different authenticated peer before it occupies the handshake
		// deadline. Keep the listener owner and bound rejection processing.
		relationship, ok := capability.Relationship()
		if !ok || relationship != runtime.relationship || runtime.ctx.Err() != nil {
			_ = capability.Close()
			if inspectionRejectBackoff(runtime.ctx) {
				continue
			}
			return
		}
		runtime.mu.Lock()
		if runtime.ctx.Err() != nil {
			runtime.mu.Unlock()
			_ = capability.Close()
			return
		}
		runtime.active = capability
		runtime.mu.Unlock()
		succeeded := c.serveResourceInspection(runtime, capability)
		_ = capability.Close()
		runtime.mu.Lock()
		runtime.active = nil
		runtime.mu.Unlock()
		if runtime.ctx.Err() != nil || !succeeded && !inspectionRejectBackoff(runtime.ctx) {
			return
		}
	}
}

// Rejections cannot spin an unbounded hot accept loop or release the owned port.
func inspectionRejectBackoff(ctx context.Context) bool {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Core) serveResourceInspection(runtime *resourceInspectionRuntime, capability *directlan.ManagedInspection) bool {
	if capability.Negotiate() != nil {
		return false
	}
	request, err := capability.ReadRequest()
	if err != nil {
		return false
	}
	var inspection resourcegrant.Inspection
	var fence *resourcegrant.DisclosureFence
	c.op.Lock()
	g := c.resourceGrants
	if g == nil || g.runtime != runtime || runtime.ctx.Err() != nil || g.frozen || g.timeUncertain {
		c.op.Unlock()
		return false
	}
	err = c.withResourceInspectionState(func(*resourcePathBinding) error {
		record, ok := activeResourceGrant(g)
		if !ok || g.timeUncertain || g.relationshipDenied || request.Target != record.Target || request.GrantID != record.ID || request.GrantRevision != record.Revision || runtime.relationship != record.Relationship {
			return resourcegrant.ErrInvalid
		}
		relationship, err := c.resourceGrantRelationship(record.Relationship.PeerKey, time.Now())
		if err != nil || relationship != record.Relationship {
			return resourcegrant.ErrInvalid
		}
		current := c.capacityPolicy()
		inspection = resourcegrant.Inspection{ProtocolVersion: resourcegrant.ProtocolVersion, Target: record.Target, Requested: resourceSettings(current), Effective: resourceEffective(current)}
		fence = g.fence
		return inspection.Validate()
	})
	c.op.Unlock()
	if err != nil || runtime.ctx.Err() != nil {
		return false
	}
	// Generic close for all denials and transport failures. No remote error text,
	// grant catalog, private authority, journal or operation identifiers escape.
	writeErr := capability.WriteInspection(fence, request, inspection)
	// WriteInspection has returned after releasing its borrow and closing the
	// capability. Preserve terminal timing evidence for this exact old fence;
	// an intervening replacement grant must never inherit its expiry.
	expired, uncertain := fence.TimingObservation(request)
	if expired || uncertain {
		c.op.Lock()
		if c.resourceGrants == g && g.fence == fence && !g.frozen {
			_ = c.withResourceInspectionState(func(*resourcePathBinding) error { return nil })
		}
		c.op.Unlock()
	}
	return writeErr == nil
}
