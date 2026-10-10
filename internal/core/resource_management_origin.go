package core

import (
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// This private current-process value never appears in a DTO or evidence file.
// The detachable transport capture narrows admission to the original full
// managed tuple; neither its correlation epoch nor a review digest is authority.
type resourceManagementOrigin struct {
	controllerID string
	bootNonce    string
	owner        *config.Lock
	backend      *directLANBackend
	relationship resourcegrant.Relationship
	peer         *directlan.ResourcePeerCapture
	completion   *managedCompletionOwner
	epoch        *directlan.ContextEpoch
}

// Signal-only identity predicate; the caller also performs the actual owned
// activation/storage checks. A valid replacement epoch is never substituted.
func (original resourceManagementOrigin) epochCurrent(c *Core) bool {
	return original.backend != nil && original.completion != nil && original.epoch != nil && original.epoch.Valid() && original.backend.currentCompletion() == original.completion && original.completion.core == c && original.completion.backend == original.backend && original.completion.node == original.backend.Node && original.completion.authority.Load() == original.epoch && original.completion.authorityCurrent()
}

func remoteResourceRelationship(local resourcegrant.Relationship) resourcegrant.Relationship {
	return resourcegrant.Relationship{Backend: local.Backend, TargetKey: local.PeerKey, PeerKey: local.TargetKey, PairBinding: local.PairBinding}
}

// Caller holds Core.op, outside any lifecycle ownership callback.
func (c *Core) captureResourceManagementOriginLocked(peerKey string) (resourceManagementOrigin, error) {
	var empty resourceManagementOrigin
	if c.ctx == nil || c.ctx.Err() != nil || !resource.ValidID(c.resourceIdentity) || !resource.ValidID(c.resourceNonce) || c.resourceLock == nil {
		return empty, resourceManagementUnavailable()
	}
	backend, ok := c.nodeCopy().(*directLANBackend)
	if !ok || backend == nil || backend.Node == nil {
		return empty, resourceManagementUnavailable()
	}
	var relationship resourcegrant.Relationship
	err := c.withResourceInspectionState(func(*resourcePathBinding) error {
		var err error
		relationship, err = c.resourceGrantRelationship(peerKey)
		return err
	})
	if err != nil {
		return empty, resourceManagementUnavailable()
	}
	completion := backend.currentCompletion()
	if completion == nil || !completion.activationCurrent() || !completion.coreCurrent(peerKey) {
		return empty, resourceManagementUnavailable()
	}
	original := resourceManagementOrigin{controllerID: c.resourceIdentity, bootNonce: c.resourceNonce, owner: c.resourceLock, backend: backend, relationship: relationship, completion: completion, epoch: completion.authority.Load()}
	if !original.epochCurrent(c) {
		return empty, resourceManagementUnavailable()
	}
	peer, err := backend.Node.CaptureResourcePeerWithEpoch(remoteResourceRelationship(relationship), original.epoch)
	if err != nil {
		return empty, resourceManagementUnavailable()
	}
	original.peer = peer
	if c.resourceManagementOriginCurrentLocked(original) != nil {
		return empty, resourceManagementUnavailable()
	}
	return original, nil
}

// The original capture is checked at every outer admission/disclosure gate. A
// fresh lookup may verify equality but can never replace this captured origin.
func (c *Core) resourceManagementOriginCurrentLocked(original resourceManagementOrigin) error {
	if original.peer == nil || original.backend == nil || original.backend.Node == nil || original.owner == nil || original.owner != c.resourceLock || original.controllerID != c.resourceIdentity || original.bootNonce != c.resourceNonce || c.ctx == nil || c.ctx.Err() != nil || c.nodeCopy() != original.backend || !original.epochCurrent(c) {
		return resourceManagementUnavailable()
	}
	var current resourcegrant.Relationship
	err := c.withResourceInspectionState(func(*resourcePathBinding) error {
		var err error
		current, err = c.resourceGrantRelationship(original.relationship.PeerKey)
		return err
	})
	if err != nil || current != original.relationship || !original.completion.activationCurrent() || !original.completion.coreCurrent(original.relationship.PeerKey) || !original.epochCurrent(c) || !original.backend.Node.ResourcePeerCurrent(original.peer, remoteResourceRelationship(original.relationship)) {
		return resourceManagementUnavailable()
	}
	return nil
}
