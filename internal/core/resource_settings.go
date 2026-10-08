package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func resourceUnavailable() error {
	return &localCommandError{"resource_unavailable", "local resource identity is unavailable; check private state and restart its owning agent; do not delete state to restore an identity"}
}
func resourcePayloadError() error {
	return &localCommandError{"resource_invalid", "invalid resource target, schema or settings; inspect the local resource and provide both transfer choices"}
}
func resourceDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func resourceSettings(p capacity.Policy) resource.Settings {
	files, ok := p.Resources["transferConcurrentFiles"]
	if !ok {
		files = capacity.Default()
	}
	peer, ok := p.Resources["transferConcurrentPerPeer"]
	if !ok {
		peer = capacity.Default()
	}
	return resource.Settings{TransferConcurrentFiles: files, TransferConcurrentPerPeer: peer}
}
func resourceEffective(p capacity.Policy) resource.Effective {
	return resource.Effective{TransferConcurrentFiles: p.Number("resources", "transferConcurrentFiles"), TransferConcurrentPerPeer: p.Number("resources", "transferConcurrentPerPeer")}
}
func resourceProjection(current capacity.Policy, settings resource.Settings) capacity.Policy {
	proposed := current.Clone()
	proposed.Resources["transferConcurrentFiles"] = settings.TransferConcurrentFiles
	proposed.Resources["transferConcurrentPerPeer"] = settings.TransferConcurrentPerPeer
	return proposed
}
func (c *Core) resourceRevision(current capacity.Policy, profile Profile) string {
	// Authority write attempts (including failed ones) conservatively invalidate
	// reviews. The existing counter catches A→B→A writes without changing legacy
	// formats. The boot nonce also expires reviews after older binaries run.
	return resourceDigest(struct {
		ID, Nonce, Capacity string
		Generation          uint64
	}{c.resourceIdentity, c.resourceNonce, capacityRevision(current, profile), c.lanStartWriteRevision.Load()})
}
func (c *Core) resourceDescriptor(current capacity.Policy, profile Profile) resource.Descriptor {
	return resource.Descriptor{Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: c.resourceIdentity}, Type: resource.Type, Authority: "local", Provider: "local", Operations: []string{"list", "inspect", "preview", "apply", "operation.status"}, Revision: c.resourceRevision(current, profile), Requested: resourceSettings(current), Effective: resourceEffective(current)}
}

// Called only under c.op through existing authenticated local control (IPC or
// the existing local Web command dispatcher). No peer management handler,
// discovery capability or management grant is added.
func (c *Core) resourceCommand(name string, raw json.RawMessage) (any, error) {
	return c.resourceCommandContext(context.Background(), name, raw)
}
func (c *Core) resourceCommandContext(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if c.resourceIdentity == "" {
		return nil, resourceUnavailable()
	}
	var result any
	var commandErr error
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		binding, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer binding.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, binding.journalInfo) {
			return errResourceBinding
		}
		if name == "resource.apply" {
			result, commandErr = c.resourceApply(ctx, raw, binding)
		} else if name == "resource.operation.status" {
			result, commandErr = c.resourceStatus(raw)
		} else {
			result, commandErr = c.resourceCommandOwned(name, raw)
		}
		if name != "resource.apply" {
			return binding.check()
		}
		return nil
	})
	if err != nil {
		return nil, resourceUnavailable()
	}
	return result, commandErr
}
func (c *Core) resourceCommandOwned(name string, raw json.RawMessage) (any, error) {
	current, profile := c.capacityPolicy(), c.profileCopy()
	descriptor := c.resourceDescriptor(current, profile)
	if name == "resource.list" {
		if err := resource.Decode(raw, 4096, &struct{}{}); err != nil {
			return nil, resourcePayloadError()
		}
		return resource.Catalog{SchemaVersion: resource.SchemaVersion, Resources: []resource.Descriptor{descriptor}}, nil
	}
	var target resource.Target
	var settings resource.Settings
	if name == "resource.inspect" {
		if err := resource.Decode(raw, 4096, &target); err != nil {
			return nil, resourcePayloadError()
		}
	} else {
		var in resource.PreviewRequest
		if err := resource.Decode(raw, 4096, &in); err != nil {
			return nil, resourcePayloadError()
		}
		if err := in.Settings.Validate(); err != nil {
			return nil, resourcePayloadError()
		}
		target, settings = in.Target, in.Settings
	}
	if target.Validate() != nil {
		return nil, resourcePayloadError()
	}
	if target.ResourceID != c.resourceIdentity {
		return nil, &localCommandError{"resource_not_found", "local resource was not found; list the current resources"}
	}
	if name == "resource.inspect" {
		return descriptor, nil
	}
	proposed := resourceProjection(current, settings)
	// Share the canonical policy preview's supported choices, backend, private
	// capacity and transfer admission validation. Its full private view stays
	// internal: only the allowlisted two-field projection is returned.
	policyRequest, _ := json.Marshal(struct {
		Policy capacity.Policy `json:"policy"`
	}{proposed})
	if _, err := c.capacityCommand("policy.preview", policyRequest); err != nil {
		return nil, &localCommandError{"resource_invalid", "transfer settings cannot be previewed against the current capacity policy"}
	}
	if *c.resourceState.HighWater == math.MaxUint64 {
		return nil, resourceSequenceExhausted()
	}
	operationID := resource.OperationID(c.resourceIdentity, c.resourceNonce, *c.resourceState.HighWater+1)
	revision := resourceReviewRevision(operationID, descriptor.Revision, capacityRevision(proposed, profile), settings)
	return resource.Preview{Target: target, OperationID: operationID, BaseRevision: descriptor.Revision, Revision: revision, Requested: settings, Effective: resourceEffective(proposed), Destructive: false}, nil
}
func resourceReviewRevision(operationID, base, proposed string, settings resource.Settings) string {
	return resourceDigest(struct {
		Schema                            int
		ID, Actor, Action, Base, Proposed string
		Settings                          resource.Settings
	}{resource.SchemaVersion, operationID, resource.LocalActor, "apply", base, proposed, settings})
}
