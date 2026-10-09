package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func resourceManagementUnavailable() error {
	return &localCommandError{"resource_management_remote_unavailable", "remote management could not be completed; verify the exact peer, grant and listener; after an uncertain apply, query its operation status before deciding what to do next"}
}
func managementCommandAction(name string) string {
	switch name {
	case "resource.remote.management.inspect":
		return resourcegrant.Inspect
	case "resource.remote.management.preview":
		return resourcegrant.PreviewAction
	case "resource.remote.management.apply":
		return resourcegrant.ApplyAction
	case "resource.remote.management.operation.status":
		return resourcegrant.StatusAction
	default:
		return ""
	}
}

// Authenticated LOCAL typed entry only. The four explicit names never come
// from a peer and cannot forward arbitrary local command names or payloads.
func (c *Core) resourceRemoteManagementCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var input resourcegrant.RemoteManagementInput
	if resource.Decode(raw, 4096, &input) != nil || input.Validate() != nil || managementCommandAction(name) == "" || input.Request.Action != managementCommandAction(name) {
		return nil, resourceGrantInvalid()
	}
	done, err := c.beginWork()
	if err != nil {
		return nil, resourceManagementUnavailable()
	}
	defer done()
	c.op.Lock()
	if c.resourceGrants == nil || c.ctx.Err() != nil || ctx.Err() != nil {
		c.op.Unlock()
		return nil, resourceManagementUnavailable()
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	var relationship resourcegrant.Relationship
	err = c.withResourceInspectionState(func(*resourcePathBinding) error {
		var projectionErr error
		relationship, projectionErr = c.resourceGrantRelationship(input.PeerKey, time.Now())
		return projectionErr
	})
	c.op.Unlock()
	if !ok || backend == nil || backend.Node == nil || err != nil {
		return nil, resourceManagementUnavailable()
	}
	expected := resourcegrant.Relationship{Backend: relationship.Backend, TargetKey: relationship.PeerKey, PeerKey: relationship.TargetKey, PairBinding: relationship.PairBinding}
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	// Exactly one bounded request. Transport recovery precedes the application
	// request only. In particular, an uncertain apply is never retransmitted.
	reply, err := backend.Node.ManageRemote(run, expected, input.Request)
	if err != nil {
		if errors.Is(err, resourcegrant.ErrUnsupported) {
			return nil, &localCommandError{"resource_management_remote_unsupported", "the authenticated peer does not support the management protocol; use a management-capable peer and an explicitly reviewed management grant"}
		}
		return nil, resourceManagementUnavailable()
	}
	c.op.Lock()
	defer c.op.Unlock()
	var current resourcegrant.Relationship
	err = c.withResourceInspectionState(func(*resourcePathBinding) error {
		var projectionErr error
		current, projectionErr = c.resourceGrantRelationship(input.PeerKey, time.Now())
		return projectionErr
	})
	if err != nil || current != relationship || c.nodeCopy() != backend || run.Err() != nil || c.ctx.Err() != nil {
		return nil, resourceManagementUnavailable()
	}
	return reply, nil
}
