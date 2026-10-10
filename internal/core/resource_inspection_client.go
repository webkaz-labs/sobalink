package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func resourceRemoteUnavailable() error {
	return &localCommandError{"resource_remote_unavailable", "remote inspection could not be completed; verify the peer, exact saved grant and listener readiness"}
}

// Authenticated LOCAL control only. This is one typed remote inspection, not a
// bridge for local commands. Core.op protects selection but never spans the
// bounded network exchange. No response is retained in request history.
func (c *Core) resourceRemoteInspectCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var input resourcegrant.RemoteInspectInput
	if resource.Decode(raw, 4096, &input) != nil || input.Validate() != nil {
		return nil, resourceGrantInvalid()
	}
	reply, err := c.resourceRemoteInspectionExchange(ctx, input, nil)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (c *Core) resourceRemoteInspectionExchange(ctx context.Context, input resourcegrant.RemoteInspectInput, expectedOrigin *resourceManagementOrigin) (resourcegrant.Inspection, error) {
	var empty resourcegrant.Inspection
	if ctx == nil || input.Validate() != nil {
		return empty, resourceGrantInvalid()
	}
	if expectedOrigin != nil {
		original := *expectedOrigin
		expectedOrigin = &original
	}
	done, err := c.beginWork()
	if err != nil {
		return empty, resourceRemoteUnavailable()
	}
	defer done()
	c.op.Lock()
	if c.resourceGrants == nil || c.ctx.Err() != nil || ctx.Err() != nil {
		c.op.Unlock()
		return empty, resourceRemoteUnavailable()
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	var relationship resourcegrant.Relationship
	if expectedOrigin != nil {
		err = c.resourceManagementOriginCurrentLocked(*expectedOrigin)
		if input.PeerKey != expectedOrigin.relationship.PeerKey {
			err = resourceRemoteUnavailable()
		}
		backend, relationship = expectedOrigin.backend, expectedOrigin.relationship
	} else {
		err = c.withResourceInspectionState(func(*resourcePathBinding) error {
			var projectionErr error
			relationship, projectionErr = c.resourceGrantRelationship(input.PeerKey, time.Now())
			return projectionErr
		})
	}
	c.op.Unlock()
	if !ok || backend == nil || backend.Node == nil || err != nil {
		return empty, resourceRemoteUnavailable()
	}
	// The remote target's grant identifies this process as its permitted peer.
	expected := resourcegrant.Relationship{Backend: relationship.Backend, TargetKey: relationship.PeerKey, PeerKey: relationship.TargetKey, PairBinding: relationship.PairBinding}
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	var response resourcegrant.Inspection
	if expectedOrigin != nil {
		response, err = backend.Node.InspectRemoteCaptured(run, expectedOrigin.peer, expected, input.Request)
	} else {
		response, err = backend.Node.InspectRemote(run, expected, input.Request)
	}
	if err != nil {
		if errors.Is(err, resourcegrant.ErrUnsupported) {
			return empty, &localCommandError{"resource_remote_unsupported", "the authenticated peer reported an unsupported inspection protocol version"}
		}
		return empty, resourceRemoteUnavailable()
	}
	c.op.Lock()
	defer c.op.Unlock()
	if expectedOrigin != nil {
		if run.Err() != nil || c.resourceManagementOriginCurrentLocked(*expectedOrigin) != nil {
			return empty, resourceRemoteUnavailable()
		}
		return response, nil
	}
	var current resourcegrant.Relationship
	err = c.withResourceInspectionState(func(*resourcePathBinding) error {
		var projectionErr error
		current, projectionErr = c.resourceGrantRelationship(input.PeerKey, time.Now())
		return projectionErr
	})
	if err != nil || current != relationship || c.nodeCopy() != backend || run.Err() != nil || c.ctx.Err() != nil {
		return empty, resourceRemoteUnavailable()
	}
	return response, nil
}
