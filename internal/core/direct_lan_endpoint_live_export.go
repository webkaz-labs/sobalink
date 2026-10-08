package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Live export changes only signed issuance history. It uses the existing export
// reviewer/reducer, sole whole-file writer and fresh owner epoch, without
// rebinding a transport or changing incoming endpoint/application authority.
func (c *Core) liveEndpointExportCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	in, err := decodeDirectLANEndpointExportInput(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	preview := name == "direct-lan.endpoint.export.preview" || name == "direct-lan.endpoint.reexport.preview"
	reexport := name == "direct-lan.endpoint.reexport" || name == "direct-lan.endpoint.reexport.preview"
	if reexport && (in.Operation != "" || in.Lifetime != "" || in.Expires != "") {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	c.op.Lock()
	defer c.op.Unlock()
	b := c.endpointBackendLocked()
	if b == nil || b.Node == nil || b.currentCompletion() == nil || !b.currentCompletion().activationCurrent() {
		return nil, directlan.ErrUnavailable
	}
	o := b.currentCompletion()
	o.mu.Lock()
	defer o.mu.Unlock()
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	live := &contextSaveLiveness{ctx: ctx, core: c.ctx, ordinary: o}
	wrote := false
	result, err := s.endpointExportLocked(ctx, o.process, in, preview, reexport, time.Now(), func(next endpointmeta.Snapshot) (endpointmeta.SaveResolution, error) {
		before := *cloneDirectLANMetadata(s.state.Metadata)
		state, e := s.stateWithEndpointMetadataLocked(next)
		// Before any write, the full prospective current projection must preserve
		// every currently admitted endpoint and immutable pair binding.
		if e == nil {
			var projected directlan.Config
			projected, e = projectManagedCurrentEndpoint(state, time.Now())
			if e == nil && (projected.Listen != b.Node.Endpoint()) {
				e = endpointmeta.ErrReview
			}
		}
		if e != nil {
			return endpointmeta.SaveResolution{}, e
		}
		wrote = true
		e = s.writeContextPublicationLocked(o.process, state, live)
		return endpointmeta.ResolveSave(before, next, atomicPublished(e), e), e
	})
	if wrote {
		if err == nil {
			_, err = s.managedCurrentEndpointProjectionLocked(time.Now())
		}
		if err == nil {
			err = live.err()
		}
		if err == nil {
			o.receipt, o.revision = s.contextPublication, s.reviewRevision
			o.configuration = contextConfigurationDigest(s.state)
			o.currentEndpoints = true
			o.epoch = directlan.NewContextEpoch()
			s.contextEpoch = o.epoch
			o.authority.Store(o.epoch)
			err = live.err()
		}
		if err != nil {
			o.invalidate()
			return nil, directLANEndpointError(err)
		}
	}
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	switch value := result.(type) {
	case directLANEndpointExportReview:
		value.EndpointUpdatesEnabled = true
		return value, nil
	case directLANEndpointExportResult:
		value.EndpointUpdatesEnabled = true
		return value, nil
	}
	return result, nil
}
