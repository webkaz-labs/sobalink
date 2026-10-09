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
	done, err := c.beginWork()
	if err != nil {
		return nil, resourceRemoteUnavailable()
	}
	defer done()
	c.op.Lock()
	if c.resourceGrants == nil || c.ctx.Err() != nil || ctx.Err() != nil {
		c.op.Unlock()
		return nil, resourceRemoteUnavailable()
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
		return nil, resourceRemoteUnavailable()
	}
	// The remote target's grant identifies this process as its permitted peer.
	expected := resourcegrant.Relationship{Backend: relationship.Backend, TargetKey: relationship.PeerKey, PeerKey: relationship.TargetKey, PairBinding: relationship.PairBinding}
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	response, err := backend.Node.InspectRemote(run, expected, input.Request)
	if err != nil {
		if errors.Is(err, resourcegrant.ErrUnsupported) {
			return nil, &localCommandError{"resource_remote_unsupported", "the authenticated peer reported an unsupported inspection protocol version"}
		}
		return nil, resourceRemoteUnavailable()
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
		return nil, resourceRemoteUnavailable()
	}
	return response, nil
}
