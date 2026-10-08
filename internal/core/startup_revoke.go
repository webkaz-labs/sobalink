package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Epochs are private revocation tokens, not portable definitions or credentials.
// One atomic write invalidates both kinds of saved outbound approval. Re-review
// can intentionally approve the same exact identity under its new epoch.
func samePeerEpochs(reviewed, current map[string]string) bool {
	for id, epoch := range reviewed {
		if current[id] != epoch {
			return false
		}
	}
	return true
}
func (c *Core) reviewPeerEpochs(ids []string) map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	epochs := map[string]string{}
	for _, id := range ids {
		if id != "" {
			epochs[id] = c.startup.Revocations[id]
		}
	}
	return epochs
}
func (c *Core) peerEpochsValid(epochs map[string]string) bool {
	ids := make([]string, 0, len(epochs))
	for id := range epochs {
		ids = append(ids, id)
	}
	if c.managedPeersDenied(ids) {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return samePeerEpochs(epochs, c.startup.Revocations) && !deniedPeerEpochs(epochs, c.managedDenied)
}
func (c *Core) revokeStartupPeer(id string) error {
	return c.revokeStartupPeerWithFailure(id, nil)
}

func (c *Core) revokeStartupPeerWithFailure(id string, failure *peerScopeFailure) error {
	return c.revokeStartupPeerSettings(id, failure, false, false)
}

// Reopen and terminal cleanup run before transfer/service owners exist. They
// invalidate only still-usable approvals and never invoke live cleanup. A failed
// prior write republishes the same denial epoch rather than rotating it again.
func (c *Core) revokeTerminalStartupPeer(id string, retry bool) error {
	return c.revokeStartupPeerSettings(id, nil, true, retry)
}

func (c *Core) revokeStartupPeerSettings(id string, failure *peerScopeFailure, terminal, retry bool) error {
	if !config.ValidPeerID(id) {
		return errors.New("select an exact peer identity to revoke")
	}
	c.mu.RLock()
	rotate := !terminal || c.usableSavedPeerApprovalLocked(id)
	if !rotate && !retry {
		c.mu.RUnlock()
		return nil
	}
	next := c.startup
	next.Revocations = map[string]string{}
	for peer, epoch := range c.startup.Revocations {
		next.Revocations[peer] = epoch
	}
	c.mu.RUnlock()
	if rotate {
		counter := uint64(0)
		if previous := next.Revocations[id]; previous != "" {
			counter, _ = strconv.ParseUint(strings.SplitN(previous, ":", 2)[0], 16, 64)
		}
		if counter == ^uint64(0) {
			return &localCommandError{"startup_revocation_unconfirmed", "private revocation generation is exhausted; disable saved startup settings before continuing"}
		}
		next.Revocations[id] = fmt.Sprintf("%016x:%s", counter+1, randomID())
	}
	// The journal is authoritative on reopen and is written before app trust.
	persistErr := c.writePrivateSettings("startup-revocations.json", next.Revocations)
	if persistErr != nil && !errors.Is(persistErr, config.ErrAtomicCommitted) {
		// A separate atomic store is a best-effort durable fallback. Neither
		// failure rolls back the in-memory revocation or keeps live work running.
		// Preserve fallback uncertainty without erasing the journal failure.
		persistErr = errors.Join(persistErr, c.writePrivateSettings("startup.json", next))
	}
	c.mu.Lock()
	c.startup = next
	for _, entry := range next.Entries {
		if !samePeerEpochs(entry.PeerEpochs, next.Revocations) {
			delete(c.startupPending, entry.Name)
		}
	}
	c.mu.Unlock()
	c.cancelSavedPeerProxies(id)
	if persistErr != nil {
		var scopeErr error
		if !terminal {
			scopeErr = c.stopPeerServicesWithFailure(id, failure)
			c.stopPeerProxies(id)
		}
		c.mu.Lock()
		c.startupSuppressed = true
		c.startupPending = map[string]string{}
		c.savedProxyPending = map[string]string{}
		c.networkError = "Durable startup revocation could not be confirmed; repair private state before restarting"
		c.networkErrorCode = "startup_revocation_unconfirmed"
		c.mu.Unlock()
		return errors.Join(&localCommandError{"startup_revocation_unconfirmed", "outbound work stopped; durable startup revocation could not be confirmed. Repair private state before restarting"}, persistErr, scopeErr)
	}
	return nil
}

// c.mu must be held. Disabled startup entries cannot execute; saved proxies can
// still be explicitly started even when their launch flag is off. Compare the
// complete approval, including target IDs omitted from old epoch maps.
func (c *Core) usableSavedPeerApprovalLocked(id string) bool {
	for _, entry := range c.startup.Entries {
		ids := startupEntryPeerIDs(entry)
		if entry.Enabled && containsPeerID(ids, id) && peerApprovalEpochsValid(ids, entry.PeerEpochs, c.startup.Revocations) {
			return true
		}
	}
	for _, entry := range c.savedProxies.Entries {
		ids := savedProxyPeerIDs(entry)
		if containsPeerID(ids, id) && peerApprovalEpochsValid(ids, entry.PeerEpochs, c.startup.Revocations) {
			return true
		}
	}
	for _, peer := range c.profile.Peers {
		if peer.ID == id && peer.RevocationEpoch == c.startup.Revocations[id] {
			return true
		}
	}
	return false
}

func containsPeerID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func startupEntryPeerIDs(entry StartupEntry) []string {
	var ids []string
	for _, service := range entry.Services {
		if service.PeerID != "" {
			ids = append(ids, service.PeerID)
		}
		ids = append(ids, service.PeerIDs...)
	}
	return ids
}

func savedProxyPeerIDs(entry savedProxy) []string {
	ids := make([]string, 0, len(entry.Scope.Targets))
	for _, target := range entry.Scope.Targets {
		ids = append(ids, target.PeerID)
	}
	return ids
}

func peerApprovalEpochsValid(ids []string, reviewed, current map[string]string) bool {
	if !samePeerEpochs(reviewed, current) {
		return false
	}
	for _, id := range ids {
		if reviewed[id] != current[id] {
			return false
		}
	}
	return true
}

func deniedPeerEpochs(epochs map[string]string, denied map[string]bool) bool {
	for id := range epochs {
		if denied[id] {
			return true
		}
	}
	return false
}

func deniedPeerIDs(ids []string, denied map[string]bool) bool {
	for _, id := range ids {
		if denied[id] {
			return true
		}
	}
	return false
}

// c.mu must be held. Negative authority is independent of saved epoch bytes.
func (c *Core) startupApprovalValidLocked(entry StartupEntry) bool {
	ids := startupEntryPeerIDs(entry)
	return peerApprovalEpochsValid(ids, entry.PeerEpochs, c.startup.Revocations) && !deniedPeerIDs(ids, c.managedDenied) && !deniedPeerEpochs(entry.PeerEpochs, c.managedDenied)
}

func (c *Core) savedProxyApprovalValidLocked(entry savedProxy) bool {
	ids := savedProxyPeerIDs(entry)
	return peerApprovalEpochsValid(ids, entry.PeerEpochs, c.startup.Revocations) && !deniedPeerIDs(ids, c.managedDenied) && !deniedPeerEpochs(entry.PeerEpochs, c.managedDenied)
}

func (c *Core) startupApprovalValid(entry StartupEntry) bool {
	if c.managedPeersDenied(startupEntryPeerIDs(entry)) {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.startupApprovalValidLocked(entry)
}

func (c *Core) savedProxyApprovalValid(entry savedProxy) bool {
	if c.managedPeersDenied(savedProxyPeerIDs(entry)) {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.savedProxyApprovalValidLocked(entry)
}
