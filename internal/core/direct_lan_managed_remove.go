package core

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Removal is a reduction-only lifecycle admission, never an activation receipt.
// Core.op and exclusive profile ownership cover its whole lifetime.
type managedRemovalOwner struct {
	core                                         *Core
	store                                        *directLANStore
	process, attemptedNetwork, attemptedHostname string
	node                                         NetworkBackend
	control                                      *contextControlOwner
	joined                                       bool
}

type managedRemovalResult struct {
	Migration          pairRecordSaveResult `json:"-"`
	Terminal           pairRecordSaveResult `json:"-"`
	MigrationChanged   bool                 `json:"migrationChanged"`
	MigrationPublished bool                 `json:"migrationPublished"`
	MigrationDurable   bool                 `json:"migrationDurable"`
	TerminalChanged    bool                 `json:"terminalChanged"`
	TerminalPublished  bool                 `json:"terminalPublished"`
	TerminalDurable    bool                 `json:"terminalDurable"`
	AncillaryComplete  bool                 `json:"ancillaryComplete"`
	OwnersJoined       bool                 `json:"ownersJoined"`
}

type pairRemovalRetry struct {
	state, file string
	revision    uint64
}

func (s *directLANStore) removalRetryCurrentLocked() bool {
	r := s.removalRetry
	return r != nil && r.state == privateRevision(s.state) && r.file == s.fileDigest && r.revision == s.reviewRevision
}

func (c *Core) pairRemovalOwnerLocked(ctx context.Context, s *directLANStore, process string, o *managedRemovalOwner) error {
	if o == nil {
		return c.pairRecordOwnerLocked(ctx, s, process)
	}
	if ctx == nil || ctx.Err() != nil {
		return endpointmeta.ErrReview
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.managedRemoval != o || o.core != c || o.store != s || o.process != process || !o.joined || c.ctx == nil || c.ctx.Err() != nil || c.closing || c.directLAN != s || c.lanStartNonce != process || c.node != nil || c.contextControl != nil || c.attemptedNetwork != o.attemptedNetwork || c.attemptedHostname != o.attemptedHostname {
		return endpointmeta.ErrReview
	}
	return nil
}

// Public removal/retry reaches this coordinator only under Core.op. A failed
// join keeps the actual owner installed. No Node.Revoke or DTO deletion occurs.
func (c *Core) removeManagedDirectLANLocked(ctx context.Context, id string) (managedRemovalResult, error) {
	return c.removeManagedDirectLANAtLocked(ctx, id, time.Now)
}

// The clock seam permits deterministic source-authored synthetic publisher tests.
func (c *Core) removeManagedDirectLANAtLocked(ctx context.Context, id string, clock func() time.Time) (result managedRemovalResult, resultErr error) {
	s := c.directLANStoreCopy()
	if s == nil {
		return result, endpointmeta.ErrReview
	}
	s.mu.Lock()
	now := clock()
	m, err := s.pairRecordModelForRemovalLocked(now, true)
	if err != nil {
		s.mu.Unlock()
		return result, err
	}
	var target *endpointmeta.PeerRecord
	for i := range m.Peers {
		if m.Peers[i].Peer.Key == id {
			target = &m.Peers[i]
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return result, endpointmeta.ErrIdentity
	}
	if _, err := pairRecordDenialBinding(*target); err != nil {
		s.mu.Unlock()
		return result, err
	}
	frozen := pairRemovalRetry{privateRevision(s.state), s.fileDigest, s.reviewRevision}
	path, capacity := s.path, *s.currentCapacity()
	s.mu.Unlock()
	ids, err := c.terminalDirectLANDeniedIDs([]string{id})
	if err != nil {
		return result, err
	}
	mixed, mixedErr := c.readMixedState()
	if mixedErr != nil && !errors.Is(mixedErr, os.ErrNotExist) {
		return result, mixedErr
	}
	mixedRevision := privateRevision(mixed)
	c.mu.Lock()
	owner := &managedRemovalOwner{core: c, store: s, process: c.lanStartNonce, attemptedNetwork: c.attemptedNetwork, attemptedHostname: c.attemptedHostname, node: c.node, control: c.contextControl}
	if owner.control != nil && (owner.control.store != s || owner.control.process != owner.process) {
		c.mu.Unlock()
		return result, endpointmeta.ErrReview
	}
	if owner.attemptedNetwork != "" && owner.attemptedNetwork != "direct-lan" && owner.attemptedNetwork != "mixed" {
		c.mu.Unlock()
		return result, endpointmeta.ErrReview
	}
	switch n := owner.node.(type) {
	case nil:
	case *directLANBackend:
		if n.store != s {
			c.mu.Unlock()
			return result, endpointmeta.ErrReview
		}
	case *mixedBackend:
		child, ok := n.nodes["direct-lan"].(*directLANBackend)
		if !ok || child.store != s {
			c.mu.Unlock()
			return result, endpointmeta.ErrReview
		}
	default:
		c.mu.Unlock()
		return result, endpointmeta.ErrReview
	}
	c.managedRemoval = owner
	if c.managedDenied == nil {
		c.managedDenied = map[string]bool{}
	}
	for _, denied := range ids {
		c.managedDenied[denied] = true
	}
	c.startupSuppressed = true
	c.startupPending, c.savedProxyPending = map[string]string{}, map[string]string{}
	c.networkState, c.networkErrorCode = "stopped", "direct_lan_removal_pending"
	c.mu.Unlock()
	c.networkReady.Store(false)
	defer func() {
		if resultErr != nil {
			c.mu.Lock()
			c.networkState = "error"
			if c.networkErrorCode != "direct_lan_cleanup_required" {
				c.networkErrorCode = "direct_lan_removal_unconfirmed"
				c.networkError = "direct LAN remains stopped; inspect the removal outcome and retry before reconnecting"
			}
			c.mu.Unlock()
		}
	}()
	// Stop/cancel app admissions before joining the containing transport owner.
	var appErr error
	for _, denied := range ids {
		c.cancelSavedPeerProxies(denied)
		if c.transfers != nil {
			appErr = errors.Join(appErr, c.reducePeerWorkWithFailure(denied, nil, true))
		}
	}
	closeErr := c.stopContextControlLocked() // never while store.mu is held
	if owner.node != nil {
		closeErr = errors.Join(closeErr, owner.node.Close())
		if closeErr == nil {
			c.mu.Lock()
			if c.node == owner.node {
				c.node = nil
			}
			c.mu.Unlock()
		}
	}
	if closeErr != nil {
		return result, errors.Join(appErr, closeErr)
	}
	owner.joined, result.OwnersJoined = true, true
	afterMixed, afterMixedErr := c.readMixedState()
	if (mixedErr == nil) != (afterMixedErr == nil) || afterMixedErr != nil && !errors.Is(afterMixedErr, os.ErrNotExist) || privateRevision(afterMixed) != mixedRevision {
		return result, errors.Join(appErr, endpointmeta.ErrReview, afterMixedErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != path || *s.currentCapacity() != capacity || privateRevision(s.state) != frozen.state || s.fileDigest != frozen.file || s.reviewRevision != frozen.revision {
		return result, errors.Join(appErr, endpointmeta.ErrReview)
	}
	save := func(in pairRecordInputs) (pairRecordSaveResult, error) {
		at := clock()
		a, e := c.capturePairRecordRemovalAdmissionLocked(ctx, s, owner.process, in, at, owner)
		if e != nil {
			return pairRecordSaveResult{}, e
		}
		return c.savePairRecordLocked(ctx, s, owner.process, a, at)
	}
	if s.state.Version == directLANMetadataStateVersion {
		result.Migration, err = save(pairRecordInputs{operation: pairRecordMigrate})
		result.MigrationChanged = result.Migration.changed
		result.MigrationPublished, result.MigrationDurable = result.Migration.published, result.Migration.durable
		if err != nil || !result.Migration.durable {
			return result, errors.Join(appErr, err)
		}
	}
	result.Terminal, err = save(pairRecordInputs{operation: pairRecordRevoke, peer: id, republish: true})
	result.TerminalChanged = result.Terminal.changed
	result.TerminalPublished, result.TerminalDurable = result.Terminal.published, result.Terminal.durable
	if !result.Terminal.durable {
		return result, errors.Join(appErr, err)
	}
	// The terminal marker is durable before any ancillary durable reduction.
	// Cleanup uses other existing stores and must run outside the evidence lock.
	s.mu.Unlock()
	for _, denied := range ids {
		if c.transfers != nil {
			appErr = errors.Join(appErr, c.revokePeerWithFailure(denied, nil))
		}
	}
	cleanupErr := c.cleanupTerminalDirectLAN(ids)
	s.mu.Lock()
	result.AncillaryComplete = cleanupErr == nil && appErr == nil
	return result, errors.Join(appErr, err, cleanupErr)
}
