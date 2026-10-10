package core

import (
	"context"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func resourceCatalogRemoteOutcome(s resourcecatalog.Selection, err error) resourceCatalogCapture {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && (coded.ErrorCode() == "resource_remote_unsupported" || coded.ErrorCode() == "resource_management_remote_unsupported") {
		return resourceCatalogFailure(s, "unsupported")
	}
	return resourceCatalogFailure(s, "unavailable")
}

// Both exchanges are existing authenticated protocol bodies refactored by the
// shared-client owner to accept this original captured origin. Neither can
// recapture a replacement, retry, fall back, or run another management action.
func (c *Core) resourceCatalogRemoteSettings(ctx context.Context, s resourcecatalog.Selection, l resourcecatalog.Limits, budget *resourceCatalogBudget) (resourceCatalogCapture, *resourceManagementOrigin) {
	if !budget.reserveWithExtra(resourcegrant.MaxManagementResponseBytes, s.Target.ResourceID, s.PeerKey, s.GrantID) {
		return resourceCatalogFailure(s, "limited"), nil
	}
	c.op.Lock()
	origin, err := c.captureResourceManagementOriginLocked(s.PeerKey)
	c.op.Unlock()
	if err != nil || ctx.Err() != nil {
		return resourceCatalogFailure(s, "unavailable"), nil
	}
	var row resourcecatalog.Row
	if s.Kind == resourcecatalog.RemoteSettingsV1 {
		input := resourcegrant.RemoteInspectInput{PeerKey: s.PeerKey, Request: resourcegrant.InspectRequest{ProtocolVersion: 1, Target: s.Target, GrantID: s.GrantID, GrantRevision: s.GrantRevision}}
		reply, exchangeErr := c.resourceRemoteInspectionExchange(ctx, input, &origin)
		if exchangeErr != nil {
			return resourceCatalogRemoteOutcome(s, exchangeErr), &origin
		}
		if reply.Validate() != nil || reply.Target != s.Target {
			return resourceCatalogFailure(s, "invalid"), &origin
		}
		row, err = resourcecatalog.ProjectInspectionV1(s, resourcecatalog.InspectionV1{ProtocolVersion: reply.ProtocolVersion, Target: reply.Target, Requested: reply.Requested, Effective: reply.Effective}, l)
	} else if s.Kind == resourcecatalog.RemoteSettingsV2 {
		input := resourcegrant.RemoteManagementInput{PeerKey: s.PeerKey, Request: resourcegrant.ManagementRequest{ManagementSelector: resourcegrant.ManagementSelector{ProtocolVersion: 2, Target: s.Target, GrantID: s.GrantID, GrantRevision: s.GrantRevision}, Action: resourcegrant.Inspect}, Confirm: false}
		reply, exchangeErr := c.resourceRemoteManagementExchange(ctx, input, &origin)
		if exchangeErr != nil {
			return resourceCatalogRemoteOutcome(s, exchangeErr), &origin
		}
		if reply.Validate() != nil || reply.ManagementSelector != input.Request.ManagementSelector || reply.Action != resourcegrant.Inspect || reply.Inspection == nil {
			return resourceCatalogFailure(s, "invalid"), &origin
		}
		row, err = resourcecatalog.ProjectInspectionV2(s, resourcecatalog.InspectionV2{ProtocolVersion: reply.ProtocolVersion, Target: reply.Target, GrantID: reply.GrantID, GrantRevision: reply.GrantRevision, Action: reply.Action, Inspection: resourcecatalog.SettingsValues{Requested: reply.Inspection.Requested, Effective: reply.Inspection.Effective}}, l)
	} else {
		return resourceCatalogFailure(s, "invalid"), &origin
	}
	if err != nil {
		return resourceCatalogFailure(s, "invalid"), &origin
	}
	return resourceCatalogComplete(s, time.Now().UnixMilli(), []resourcecatalog.Row{row}), &origin
}
