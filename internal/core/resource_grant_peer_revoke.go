package core

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

type resourceGrantRevokeUncertain struct{ cause error }

func (*resourceGrantRevokeUncertain) Error() string {
	return "resource access is denied locally; saved grant revocation could not be confirmed; check private state before restarting"
}
func (*resourceGrantRevokeUncertain) ErrorCode() string { return "resource_grant_revoke_uncertain" }
func (e *resourceGrantRevokeUncertain) Unwrap() error   { return e.cause }

// Explicit whole-peer permission removal is a reduction, even if ordinary
// message/file Trust is already absent. It never creates first-use grant state.
func (c *Core) revokeInspectionPeerPermission(ctx context.Context, peer string) error {
	g := c.resourceGrants
	if g == nil {
		return nil
	}
	// Close the matching runtime before ownership checks or any fallible I/O.
	for _, record := range grantRecordData(g.state) {
		if record.State == resourcegrant.Active && record.Relationship.PeerKey == peer {
			g.closeFences()
			g.retireRuntime()
		}
	}
	if g.frozen {
		return &resourceGrantRevokeUncertain{resourcegrant.ErrInvalid}
	}
	err := c.withResourceInspectionState(func(b *resourcePathBinding) error {
		record, _ := g.state.ActiveRecord()
		if record.ID == "" || record.Relationship.PeerKey != peer {
			return nil
		}
		raw, err := json.Marshal(resourceGrantRevoke{Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision, Confirm: true})
		if err != nil {
			return err
		}
		_, err = c.revokeResourceGrant(ctx, raw, b)
		return err
	})
	if err != nil {
		return &resourceGrantRevokeUncertain{err}
	}
	return nil
}

// Called under Core.op immediately after decoding peer.trust=false. No current
// peer lookup or broad transfer grant is needed to reduce existing permission.
// Failures never skip ordinary teardown, nor imply durable full revocation.
func (c *Core) revokePeerPermission(ctx context.Context, peer string) error {
	if !config.ValidPeerID(peer) {
		return errors.New("select an exact peer identity to revoke")
	}
	inspectionErr := c.revokeInspectionPeerPermission(ctx, peer)
	startupErr := c.revokeStartupPeer(peer)
	profile := c.profileCopy()
	_, had := c.trust(peer)
	var saveErr error
	if had {
		filtered := profile.Peers[:0]
		for _, trust := range profile.Peers {
			if trust.ID != peer || trust.Network != profile.Settings.Network {
				filtered = append(filtered, trust)
			}
		}
		profile.Peers = filtered
		c.mu.Lock()
		c.profile = profile
		c.mu.Unlock()
	}
	teardownErr := c.revokePeerWithFailure(peer, nil)
	if had {
		saveErr = c.saveProfile(profile)
	}
	other := errors.Join(teardownErr, saveErr)
	if startupErr != nil {
		other = privateAtomicError(startupErr, other)
	}
	if inspectionErr != nil {
		return &resourceGrantRevokeUncertain{errors.Join(inspectionErr, other)}
	}
	return other
}
