package core

import (
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/config"
	"strconv"
	"strings"
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
	c.mu.RLock()
	defer c.mu.RUnlock()
	return samePeerEpochs(epochs, c.startup.Revocations)
}
func (c *Core) revokeStartupPeer(id string) error {
	if !config.ValidPeerID(id) {
		return errors.New("select an exact peer identity to revoke")
	}
	c.mu.RLock()
	next := c.startup
	next.Revocations = map[string]string{}
	for peer, epoch := range c.startup.Revocations {
		next.Revocations[peer] = epoch
	}
	c.mu.RUnlock()
	counter := uint64(0)
	if previous := next.Revocations[id]; previous != "" {
		counter, _ = strconv.ParseUint(strings.SplitN(previous, ":", 2)[0], 16, 64)
	}
	if counter == ^uint64(0) {
		return &localCommandError{"startup_revocation_unconfirmed", "private revocation generation is exhausted; disable saved startup settings before continuing"}
	}
	next.Revocations[id] = fmt.Sprintf("%016x:%s", counter+1, randomID())
	// The journal is authoritative on reopen and is written before app trust.
	persistErr := c.writePrivateSettings("startup-revocations.json", next.Revocations)
	if persistErr != nil {
		// A separate atomic store is a best-effort durable fallback. Neither
		// failure rolls back the in-memory revocation or keeps live work running.
		_ = c.writePrivateSettings("startup.json", next)
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
		c.stopPeerServices(id)
		c.stopPeerProxies(id)
		c.mu.Lock()
		c.startupSuppressed = true
		c.startupPending = map[string]string{}
		c.savedProxyPending = map[string]string{}
		c.networkError = "Durable startup revocation could not be confirmed; repair private state before restarting"
		c.networkErrorCode = "startup_revocation_unconfirmed"
		c.mu.Unlock()
		return &localCommandError{"startup_revocation_unconfirmed", "outbound work stopped; durable startup revocation could not be confirmed. Repair private state before restarting"}
	}
	return nil
}
