package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
)

func terminalDirectLANPeers(state directLANState) []string {
	var ids []string
	if state.Metadata != nil {
		for _, record := range state.Metadata.Peers {
			if record.PairRevocation != nil {
				ids = append(ids, record.Peer.Key)
			}
		}
	}
	return ids
}

// Capture before retiring bindings. An implicit mixed identity exists even if
// the peer was never included in an explicit multi-transport binding.
func (c *Core) terminalDirectLANDeniedIDs(raw []string) ([]string, error) {
	denied := make(map[string]bool, 2*len(raw))
	for _, id := range raw {
		if !config.ValidPeerID(id) {
			return nil, errors.New("terminal direct LAN identity is invalid")
		}
		denied[id], denied[mixedID("direct-lan", id)] = true, true
	}
	if len(raw) == 0 {
		return nil, nil
	}
	state, err := c.readMixedState()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, binding := range state.Bindings {
		for _, claim := range binding.Identities {
			if claim.Backend == "direct-lan" && denied[claim.ID] {
				denied[binding.PeerID] = true
			}
		}
	}
	ids := make([]string, 0, len(denied))
	for id := range denied {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (c *Core) installTerminalDirectLANDenials(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.managedDenied == nil {
		c.managedDenied = map[string]bool{}
	}
	for _, id := range ids {
		c.managedDenied[id] = true
	}
	for _, entry := range c.startup.Entries {
		if !c.startupApprovalValidLocked(entry) {
			delete(c.startupPending, entry.Name)
		}
	}
	for _, entry := range c.savedProxies.Entries {
		if !c.savedProxyApprovalValidLocked(entry) {
			delete(c.savedProxyPending, entry.Scope.Name)
			delete(c.savedProxyRuns, entry.Scope.Name)
		}
	}
}

// Call without Core.mu/store.mu. The marker is an independent negative check,
// including when a failed ancillary repair left old approval bytes in place.
func (c *Core) managedPeersDenied(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	c.mu.RLock()
	denied := deniedPeerIDs(ids, c.managedDenied)
	store := c.directLAN
	c.mu.RUnlock()
	if denied || store == nil {
		return denied
	}
	raw := terminalDirectLANPeers(store.copy())
	if len(raw) == 0 {
		return false
	}
	all, err := c.terminalDirectLANDeniedIDs(raw)
	if err != nil {
		return true // unknown retained routing cannot restore saved authority
	}
	for _, id := range ids {
		if containsPeerID(all, id) {
			return true
		}
	}
	return false
}

func (c *Core) managedPeerDenied(id string) bool {
	return c.managedPeersDenied([]string{id})
}

// Open calls this after evidence and inert settings are loaded, before any
// transfer manager, saved-work scheduling, or network owner is constructed.
// It never clears the direct-LAN store's independent recovery latch.
func (c *Core) reconcileTerminalDirectLAN() error {
	store := c.directLANStoreCopy()
	if store == nil {
		return nil
	}
	raw := terminalDirectLANPeers(store.copy())
	if len(raw) == 0 {
		return nil
	}
	// Even a malformed mixed file cannot leave raw or implicit authority live.
	var immediate []string
	for _, id := range raw {
		immediate = append(immediate, id, mixedID("direct-lan", id))
	}
	c.installTerminalDirectLANDenials(immediate)
	ids, err := c.terminalDirectLANDeniedIDs(raw)
	if err != nil {
		return c.terminalDirectLANCleanupFailed(err)
	}
	return c.cleanupTerminalDirectLAN(ids)
}

// Core.op and exclusive profile/process ownership are required. Only durable
// settings are changed here; live work is canceled and joined by the removal
// coordinator before entry. The same function is safe before construction on
// reopen. Marker evidence and old startup/proxy approval epochs are retained.
func (c *Core) cleanupTerminalDirectLAN(ids []string) error {
	for _, id := range ids {
		if !config.ValidPeerID(id) {
			return c.terminalDirectLANCleanupFailed(errors.New("terminal cleanup identity is invalid"))
		}
	}
	c.installTerminalDirectLANDenials(ids)
	c.mu.Lock()
	retry := c.managedCleanupPending
	c.managedCleanupPending = true
	c.mu.Unlock()

	denied := map[string]bool{}
	epochErrors := map[string]error{}
	var result error
	for _, id := range ids {
		denied[id] = true
		err := c.revokeTerminalStartupPeer(id, retry)
		epochErrors[id] = err
		result = errors.Join(result, err)
	}

	profile := c.profileCopy()
	keptPeers := make([]Trust, 0, len(profile.Peers))
	for _, peer := range profile.Peers {
		if !denied[peer.ID] {
			keptPeers = append(keptPeers, peer)
		}
	}
	profileChanged := len(keptPeers) != len(profile.Peers)
	profile.Peers = keptPeers
	// Retain in-memory reduction even if its durable publication fails.
	c.mu.Lock()
	c.profile = profile
	c.mu.Unlock()
	if profileChanged || retry {
		result = errors.Join(result, c.saveProfile(profile))
	}

	state, err := c.readMixedState()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	} else if err == nil {
		kept := make([]connectionroute.Binding, 0, len(state.Bindings))
		for _, binding := range state.Bindings {
			affected := false
			for _, claim := range binding.Identities {
				if claim.Backend == "direct-lan" && denied[claim.ID] {
					affected = true
				}
			}
			// Never erase the only saved link to an affected synthetic grant
			// until its own durable invalidation has succeeded.
			if affected && (!denied[binding.PeerID] || epochErrors[binding.PeerID] != nil) {
				result = errors.Join(result, errors.New("mixed binding cleanup awaits durable synthetic approval revocation"))
				kept = append(kept, binding)
			} else if !affected {
				kept = append(kept, binding)
			}
		}
		if len(kept) != len(state.Bindings) || retry {
			state.Bindings = kept
			raw, encodeErr := json.Marshal(state)
			if encodeErr != nil {
				result = errors.Join(result, encodeErr)
			} else {
				result = errors.Join(result, c.writeAtomic(filepath.Join(c.dir, "mixed.json"), raw))
			}
		}
	}
	if result != nil {
		return c.terminalDirectLANCleanupFailed(result)
	}
	c.mu.Lock()
	c.managedCleanupPending = false
	c.managedCleanupError = nil
	c.mu.Unlock()
	return nil
}

func (c *Core) terminalDirectLANCleanupFailed(err error) error {
	c.networkReady.Store(false)
	c.mu.Lock()
	c.managedCleanupPending = true
	c.managedCleanupError = err
	c.startupSuppressed = true
	c.startupPending = map[string]string{}
	c.savedProxyPending = map[string]string{}
	c.networkState = "error"
	c.networkErrorCode = "direct_lan_cleanup_required"
	c.networkError = "terminal direct LAN pairs remain denied; retry saved-approval cleanup before reconnecting"
	c.mu.Unlock()
	return errors.Join(&localCommandError{"direct_lan_cleanup_required", "terminal direct LAN pairs remain denied; retry saved-approval cleanup before reconnecting"}, err)
}

// This arms only settings that survived durable reconciliation. Loading settings
// before this point is deliberately inert, including on an online launch.
func (c *Core) armReconciledStartup(suppressed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startupSuppressed = suppressed || c.managedCleanupPending
	if c.startupSuppressed {
		return
	}
	for _, entry := range c.startup.Entries {
		if entry.Enabled && c.startupApprovalValidLocked(entry) {
			c.startupPending[entry.Name] = entry.Revision
		}
	}
	for _, entry := range c.savedProxies.Entries {
		if entry.StartOnLaunch && c.savedProxyApprovalValidLocked(entry) {
			c.savedProxyPending[entry.Scope.Name] = entry.Revision
		}
	}
}
