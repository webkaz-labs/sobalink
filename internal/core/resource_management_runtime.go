package core

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// Management is a distinct v2-only serial listener. It shares exclusive port
// ownership with inspection, never its fence or dispatch. Join after Core.op.
type resourceManagementRuntime struct {
	node         *directlan.Node
	listener     *directlan.ManagementListener
	relationship resourcegrant.Relationship
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
	mu           sync.Mutex
	active       *directlan.ManagedManagement
}

func (r *resourceManagementRuntime) stop() {
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
func (g *resourceGrantCoordinator) retireManagementRuntime() {
	if g == nil || g.managementRuntime == nil {
		return
	}
	g.managementRuntime.stop()
	g.managementRetiring, g.managementRuntime = g.managementRuntime, nil
}
func managementRuntimeDone(r *resourceManagementRuntime) bool {
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
func activeManagementGrant(g *resourceGrantCoordinator) (resourcegrant.ManagementRecord, bool) {
	if g != nil {
		for _, record := range g.state.ManagementRecords {
			if record.Record.State == resourcegrant.Active {
				return record, true
			}
		}
	}
	return resourcegrant.ManagementRecord{}, false
}
func managementSelector(record resourcegrant.Record) resourcegrant.ManagementSelector {
	return resourcegrant.ManagementSelector{ProtocolVersion: resourcegrant.ManagementProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision}
}

// Called only by the shared coordinator under Core.op after selecting the
// explicit management arm. Reconnect never recalculates the boot cutoff.
func (c *Core) reconcileResourceManagement() {
	g := c.resourceGrants
	inactive := func(reason string) { g.activation = reason; g.retireRuntime() }
	if g.frozen {
		inactive("storage_uncertain")
		return
	}
	if c.ctx.Err() != nil {
		g.closeFences()
		inactive("stopping")
		return
	}
	if inspectionRuntimeDone(g.retiring) {
		g.retiring = nil
	}
	if managementRuntimeDone(g.managementRetiring) {
		g.managementRetiring = nil
	}
	if g.managementRuntime != nil && managementRuntimeDone(g.managementRuntime) {
		g.retireManagementRuntime()
	}
	var managed resourcegrant.ManagementRecord
	var relationship resourcegrant.Relationship
	var relationshipErr error
	err := c.withResourceInspectionState(func(*resourcePathBinding) error {
		managed, _ = activeManagementGrant(g)
		if managed.Record.ID != "" {
			relationship, relationshipErr = c.resourceGrantRelationship(managed.Record.Relationship.PeerKey)
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
	record := managed.Record
	if record.ID == "" {
		inactive("expired")
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
		g.closeFences()
		inactive("relationship_changed")
		return
	}
	fresh := time.Now()
	observationErr := g.observe(fresh)
	expired, uncertain := g.managementFence.TimingObservation(managementSelector(record))
	if observationErr != nil || fresh.Unix() >= record.ExpiresAt || !fresh.Before(g.bootDeadline) || expired || uncertain {
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
	if g.managementRuntime != nil {
		if g.managementRuntime.node != backend.Node {
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
	if g.managementFence == nil {
		g.managementFence, err = resourcegrant.NewManagementFenceBefore(managed, g.bootDeadline)
		if err != nil {
			inactive("activation_unavailable")
			return
		}
	}
	ctx, cancel := context.WithDeadline(c.ctx, g.bootDeadline)
	listener, err := backend.Node.ListenManagement(ctx)
	if err != nil {
		cancel()
		if errors.Is(err, directlan.ErrManagementConflict) {
			inactive("port_conflict")
			return
		}
		inactive("listener_unavailable")
		return
	}
	runtime := &resourceManagementRuntime{node: backend.Node, listener: listener, relationship: relationship, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	g.managementRuntime, g.activation = runtime, "listening"
	go c.runResourceManagement(runtime)
}

func (c *Core) runResourceManagement(runtime *resourceManagementRuntime) {
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
		relationship, _, ok := capability.Snapshot()
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
		succeeded := c.serveResourceManagement(runtime, capability)
		_ = capability.Close()
		runtime.mu.Lock()
		runtime.active = nil
		runtime.mu.Unlock()
		if runtime.ctx.Err() != nil || !succeeded && !inspectionRejectBackoff(runtime.ctx) {
			return
		}
	}
}

func (c *Core) serveResourceManagement(runtime *resourceManagementRuntime, capability *directlan.ManagedManagement) bool {
	if capability.BindContext(runtime.ctx) != nil || capability.Negotiate() != nil {
		return false
	}
	request, err := capability.ReadRequest()
	if err != nil {
		return false
	}
	var response *directlan.ManagementResponse
	var fence *resourcegrant.ManagementFence
	c.op.Lock()
	g := c.resourceGrants
	if g == nil || g.managementRuntime != runtime || runtime.ctx.Err() != nil || g.frozen || g.timeUncertain {
		c.op.Unlock()
		return false
	}
	// This actual lifecycle callback spans authorization, history, migration,
	// durable intent, concrete provider admission, provider and terminal evidence.
	err = c.withResourceInspectionState(func(b *resourcePathBinding) error {
		binding, err := c.authorizeManagementBound(runtime, capability, request, b)
		if err != nil {
			return err
		}
		reply, err := c.managementOperationBound(runtime, capability, request, binding, b)
		if err != nil {
			return err
		}
		// Provider admission is not disclosure permission. Re-read current owned
		// grant storage and re-observe clock after all provider/journal work.
		if _, err = c.authorizeManagementBound(runtime, capability, request, b); err != nil {
			return err
		}
		fence = g.managementFence
		response, err = capability.PrepareReply(fence, reply)
		return err
	})
	c.op.Unlock()
	if response != nil {
		defer response.Close()
	}
	writeErr := err
	if writeErr == nil && response != nil {
		writeErr = response.Write()
	}
	if response != nil {
		// Release even a prepared response whose outer owned postcheck failed,
		// before waiting on Core.op for terminal timing persistence.
		_ = response.Close()
	}
	// Final transport admission can itself observe terminal time. Persist its
	// evidence only for this exact fence; replacement grants inherit nothing.
	expired, uncertain := fence.TimingObservation(request.ManagementSelector)
	if expired || uncertain {
		c.op.Lock()
		if c.resourceGrants == g && g.managementFence == fence && !g.frozen {
			_ = c.withResourceInspectionState(func(*resourcePathBinding) error { return nil })
		}
		c.op.Unlock()
	}
	return response != nil && writeErr == nil
}
