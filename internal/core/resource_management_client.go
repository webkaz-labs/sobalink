package core

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
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
	reply, err := c.resourceRemoteManagementExchange(ctx, input, nil)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// expected is an internal original capture, never decoded caller authority.
func (c *Core) resourceRemoteManagementExchange(ctx context.Context, input resourcegrant.RemoteManagementInput, expectedOrigin *resourceManagementOrigin) (resourcegrant.ManagementReply, error) {
	var empty resourcegrant.ManagementReply
	encoded, encodeErr := json.Marshal(input)
	if ctx == nil || encodeErr != nil || resource.Decode(encoded, 4096, &input) != nil || input.Validate() != nil {
		return empty, resourceGrantInvalid()
	}
	if expectedOrigin != nil {
		original := *expectedOrigin
		expectedOrigin = &original
	}
	done, err := c.beginWork()
	if err != nil {
		return empty, resourceManagementUnavailable()
	}
	defer done()
	c.op.Lock()
	if c.resourceGrants == nil || c.ctx.Err() != nil || ctx.Err() != nil {
		c.op.Unlock()
		return empty, resourceManagementUnavailable()
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	var relationship resourcegrant.Relationship
	if expectedOrigin != nil {
		err = c.resourceManagementOriginCurrentLocked(*expectedOrigin)
		if input.PeerKey != expectedOrigin.relationship.PeerKey {
			err = resourceManagementUnavailable()
		}
		backend, relationship = expectedOrigin.backend, expectedOrigin.relationship
	} else {
		err = c.withResourceInspectionState(func(*resourcePathBinding) error {
			var projectionErr error
			relationship, projectionErr = c.resourceGrantRelationship(input.PeerKey)
			return projectionErr
		})
	}
	c.op.Unlock()
	if !ok || backend == nil || backend.Node == nil || err != nil {
		return empty, resourceManagementUnavailable()
	}
	expected := resourcegrant.Relationship{Backend: relationship.Backend, TargetKey: relationship.PeerKey, PeerKey: relationship.TargetKey, PairBinding: relationship.PairBinding}
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	// Exactly one bounded request. Transport recovery precedes the application
	// request only. In particular, an uncertain apply is never retransmitted.
	var reply resourcegrant.ManagementReply
	operationID := ""
	if input.Request.Apply != nil {
		operationID = input.Request.Apply.OperationID
	} else if input.Request.Status != nil {
		operationID = input.Request.Status.OperationID
	}
	resourceacceptance.Record(c, resourceacceptance.ManagementClientInvoked, input.Request.Action, "", operationID)
	if expectedOrigin != nil {
		reply, err = backend.Node.ManageRemoteCaptured(run, expectedOrigin.peer, expected, input.Request)
	} else {
		reply, err = backend.Node.ManageRemote(run, expected, input.Request)
	}
	if err != nil {
		if errors.Is(err, resourcegrant.ErrUnsupported) {
			return empty, &localCommandError{"resource_management_remote_unsupported", "the authenticated peer does not support the management protocol; use a management-capable peer and an explicitly reviewed management grant"}
		}
		return empty, resourceManagementUnavailable()
	}
	c.op.Lock()
	defer c.op.Unlock()
	if expectedOrigin != nil {
		if run.Err() != nil || c.resourceManagementOriginCurrentLocked(*expectedOrigin) != nil || !resourceManagementCapturedReplyMatches(input.Request, reply) {
			return empty, resourceManagementUnavailable()
		}
		return reply, nil
	}
	var current resourcegrant.Relationship
	err = c.withResourceInspectionState(func(*resourcePathBinding) error {
		var projectionErr error
		current, projectionErr = c.resourceGrantRelationship(input.PeerKey)
		return projectionErr
	})
	if err != nil || current != relationship || c.nodeCopy() != backend || run.Err() != nil || c.ctx.Err() != nil {
		return empty, resourceManagementUnavailable()
	}
	return reply, nil
}

// Extra correspondence for original-capture consumers, including requested
// settings (effective values are a separate target projection).
func resourceManagementCapturedReplyMatches(request resourcegrant.ManagementRequest, reply resourcegrant.ManagementReply) bool {
	if request.Validate() != nil || reply.Validate() != nil || request.ManagementSelector != reply.ManagementSelector || request.Action != reply.Action {
		return false
	}
	switch request.Action {
	case resourcegrant.PreviewAction:
		return reply.Preview != nil && resourceDigest(reply.Preview.Requested) == resourceDigest(request.Preview.Settings)
	case resourcegrant.ApplyAction:
		return reply.Operation != nil && reply.Operation.OperationID == request.Apply.OperationID
	case resourcegrant.StatusAction:
		return reply.Operation != nil && reply.Operation.OperationID == request.Status.OperationID || reply.Unavailable != nil && reply.Unavailable.OperationID == request.Status.OperationID
	default:
		return true
	}
}
