package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// Startup approvals are private, separate from portable definitions. A saved
// selection is immutable: changing a definition or group requires fresh review.
type StartupEntry struct {
	Name              string            `json:"name"`
	IDs               []string          `json:"ids,omitempty"`
	Group             string            `json:"group,omitempty"`
	Services          []ServiceSpec     `json:"services"`
	SelectionRevision string            `json:"selectionRevision"`
	Network           string            `json:"network"`
	Hostname          string            `json:"hostname"`
	Enabled           bool              `json:"enabled"`
	Revision          string            `json:"revision"`
	PeerEpochs        map[string]string `json:"peerEpochs,omitempty"`
}
type startupStore struct {
	Version     int               `json:"version"`
	Entries     []StartupEntry    `json:"entries"`
	Revocations map[string]string `json:"revocations,omitempty"`
}
type startupSelection struct {
	Name                  string   `json:"name"`
	IDs                   []string `json:"ids,omitempty"`
	Group                 string   `json:"group,omitempty"`
	ExpectedRevision      string   `json:"expectedRevision,omitempty"`
	ExpectedStoreRevision string   `json:"expectedStoreRevision,omitempty"`
}
type StartupReview struct {
	StartupEntry
	StoreRevision string `json:"storeRevision"`
}

func privateRevision(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (c *Core) writePrivateSettings(name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return &localCommandError{"private_settings_unavailable", "private settings could not be encoded"}
	}
	if int64(len(b))+1 > c.limit("resources", "profileBytes") {
		return &localCommandError{"profile_capacity", "private settings exceed the configured profileBytes budget"}
	}
	if err := c.writeAtomic(filepath.Join(c.dir, name), append(b, '\n')); err != nil {
		message := "private settings could not be saved; check the private state directory"
		if errors.Is(err, config.ErrAtomicCommitted) {
			message = "private settings were replaced, but durability could not be confirmed; inspect private state before retrying"
		}
		return privateAtomicError(&localCommandError{"private_settings_unavailable", message}, err)
	}
	return nil
}
func (c *Core) loadStartup(suppressed bool) error {
	return c.loadStartupSettings(suppressed, true)
}

func (c *Core) loadStartupSettings(suppressed, protect bool) error {
	store := startupStore{Version: 1, Entries: []StartupEntry{}}
	err := readBoundedPrivateJSON(filepath.Join(c.dir, "startup.json"), c.limit("resources", "profileBytes"), &store)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &localCommandError{"private_settings_unavailable", "private startup settings could not be read; inspect the private state directory"}
	}
	if store.Version != 1 {
		return errors.New("unsupported private startup settings version")
	}
	seen := map[string]bool{}
	for _, entry := range store.Entries {
		if !config.ValidName(entry.Name) || seen[entry.Name] || entry.Revision == "" || len(entry.Services) == 0 {
			return errors.New("invalid private startup settings")
		}
		seen[entry.Name] = true
		for _, s := range entry.Services {
			if s.Direction != "forward" {
				return errors.New("startup settings must contain outbound services only")
			}
		}
	}
	revocations := map[string]string{}
	err = readBoundedPrivateJSON(filepath.Join(c.dir, "startup-revocations.json"), c.limit("resources", "profileBytes"), &revocations)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &localCommandError{"private_settings_unavailable", "durable startup revocation could not be read; repair private state before restarting"}
	}
	if store.Revocations == nil {
		store.Revocations = map[string]string{}
	}
	for id, epoch := range revocations {
		if !config.ValidPeerID(id) || epoch == "" {
			return errors.New("invalid private startup revocations")
		}
		if epoch > store.Revocations[id] {
			store.Revocations[id] = epoch
		}
	}
	c.mu.Lock()
	c.startup = store
	// The journal is authoritative even if a later profile write never ran.
	// Only an explicit approval under the current epoch can restore authority.
	peers := c.profile.Peers[:0]
	for _, peer := range c.profile.Peers {
		if peer.RevocationEpoch == store.Revocations[peer.ID] {
			peers = append(peers, peer)
		}
	}
	c.profile.Peers = peers
	c.startupSuppressed = suppressed
	c.startupPending = map[string]string{}
	c.startupStates = map[string]string{}
	if !suppressed {
		for _, entry := range store.Entries {
			if entry.Enabled {
				c.startupPending[entry.Name] = entry.Revision
			}
		}
	}
	c.mu.Unlock()
	return c.loadSavedProxySettings(suppressed, protect)
}
func startupEntryValid(p Profile, entry StartupEntry) bool {
	if p.Settings.Network != entry.Network || p.Settings.Hostname != entry.Hostname {
		return false
	}
	selected, revision, err := selectServices(p, serviceSelection{IDs: entry.IDs, Group: entry.Group})
	return err == nil && revision == entry.SelectionRevision && privateRevision(selected) == privateRevision(entry.Services)
}
func (c *Core) startupView() map[string]any {
	p := c.profileCopy()
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]map[string]any, 0, len(c.startup.Entries))
	for _, entry := range c.startup.Entries {
		state := c.startupStates[entry.Name]
		if state == "" {
			state = "saved"
		}
		entries = append(entries, map[string]any{"name": entry.Name, "ids": entry.IDs, "group": entry.Group, "services": entry.Services, "enabled": entry.Enabled, "valid": startupEntryValid(p, entry) && samePeerEpochs(entry.PeerEpochs, c.startup.Revocations), "revision": entry.Revision, "state": state})
	}
	return map[string]any{"entries": entries, "revision": privateRevision(c.startup), "suppressed": c.startupSuppressed}
}
func (c *Core) reviewStartup(in startupSelection) (StartupReview, error) {
	if !config.ValidName(in.Name) {
		return StartupReview{}, &localCommandError{"startup_name_required", "choose a startup selection name"}
	}
	p := c.profileCopy()
	selected, revision, err := selectServices(p, serviceSelection{IDs: in.IDs, Group: in.Group})
	if err != nil {
		return StartupReview{}, err
	}
	for _, s := range selected {
		if s.Direction != "forward" {
			return StartupReview{}, &localCommandError{"startup_outbound_only", "startup selections may contain outbound connections only; remove inbound shares"}
		}
	}
	entry := StartupEntry{Name: in.Name, IDs: append([]string(nil), in.IDs...), Group: in.Group, Services: selected, SelectionRevision: revision, Network: p.Settings.Network, Hostname: p.Settings.Hostname}
	ids := []string{}
	for _, s := range selected {
		ids = append(ids, s.PeerID)
	}
	entry.PeerEpochs = c.reviewPeerEpochs(ids)
	entry.Revision = privateRevision(entry)
	c.mu.RLock()
	rev := privateRevision(c.startup)
	c.mu.RUnlock()
	return StartupReview{StartupEntry: entry, StoreRevision: rev}, nil
}
func (c *Core) startupCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name == "startup.list" {
		var in struct{}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return c.startupView(), nil
	}
	if name == "startup.disable" {
		var in struct {
			Name                  string `json:"name"`
			ExpectedStoreRevision string `json:"expectedStoreRevision"`
		}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		c.mu.RLock()
		next := c.startup
		next.Entries = append([]StartupEntry(nil), next.Entries...)
		c.mu.RUnlock()
		if in.ExpectedStoreRevision == "" || in.ExpectedStoreRevision != privateRevision(next) {
			return nil, &localCommandError{"startup_revision_conflict", "startup settings changed; review them again"}
		}
		index := slices.IndexFunc(next.Entries, func(e StartupEntry) bool { return e.Name == in.Name })
		if index < 0 {
			return nil, &localCommandError{"startup_not_found", "saved startup selection no longer exists"}
		}
		next.Entries[index].Enabled = false
		saveErr := c.writePrivateSettings("startup.json", next)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.startup = next
		delete(c.startupPending, in.Name)
		c.startupStates[in.Name] = "disabled"
		c.mu.Unlock()
		return c.startupView(), saveErr
	}
	var in startupSelection
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	review, err := c.reviewStartup(in)
	if err != nil {
		return nil, err
	}
	if name == "startup.preview" {
		return review, nil
	}
	if name != "startup.save" {
		return nil, errors.New("unknown startup command")
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != review.Revision || in.ExpectedStoreRevision != review.StoreRevision {
		return nil, &localCommandError{"startup_revision_conflict", "startup selection or settings changed; review the complete outbound scope again"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	next := c.startup
	next.Entries = append([]StartupEntry(nil), next.Entries...)
	c.mu.RUnlock()
	entry := review.StartupEntry
	entry.Enabled = true
	index := slices.IndexFunc(next.Entries, func(e StartupEntry) bool { return e.Name == entry.Name })
	if index < 0 {
		next.Entries = append(next.Entries, entry)
	} else {
		next.Entries[index] = entry
	}
	saveErr := c.writePrivateSettings("startup.json", next)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.mu.Lock()
	c.startup = next
	delete(c.startupPending, entry.Name)
	c.startupStates[entry.Name] = "saved"
	c.mu.Unlock()
	return c.startupView(), saveErr
}

// runStartup is called under c.op only after network readiness. Each approval is
// consumed before any listener starts; explicit stops cannot trigger retries.
func (c *Core) runStartup(ctx context.Context) {
	c.runStartupSelections(ctx, c.selectionCommand)
	c.runSavedProxyStartup(ctx)
	c.resumeSavedProxies(ctx)
}
func (c *Core) runStartupSelections(ctx context.Context, start func(context.Context, string, json.RawMessage) (any, error)) {
	c.mu.Lock()
	pending := c.startupPending
	c.startupPending = map[string]string{}
	entries := append([]StartupEntry(nil), c.startup.Entries...)
	c.mu.Unlock()
	for _, entry := range entries {
		if pending[entry.Name] != entry.Revision || !entry.Enabled {
			continue
		}
		state := "stale"
		if startupEntryValid(c.profileCopy(), entry) && c.peerEpochsValid(entry.PeerEpochs) {
			raw, _ := json.Marshal(serviceSelection{IDs: entry.IDs, Group: entry.Group, ExpectedRevision: entry.SelectionRevision})
			_, err := start(ctx, "services.start", raw)
			state = "started"
			if err != nil {
				state = "failed"
			}
		}
		c.mu.Lock()
		c.startupStates[entry.Name] = state
		c.mu.Unlock()
	}
}

// Any stop/revoke invalidates pending launch work involving these services.
func (c *Core) cancelStartupServices(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.startup.Entries {
		for _, s := range entry.Services {
			if slices.Contains(ids, s.ID) {
				delete(c.startupPending, entry.Name)
			}
		}
	}
}
