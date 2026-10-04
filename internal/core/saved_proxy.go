package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

type savedProxy struct {
	Scope         ProxyScope        `json:"scope"`
	Revision      string            `json:"revision"`
	Hostname      string            `json:"hostname"`
	Username      string            `json:"username"`
	Password      string            `json:"password"`
	StartOnLaunch bool              `json:"startOnLaunch"`
	PeerEpochs    map[string]string `json:"peerEpochs,omitempty"`
}
type savedProxyStore struct {
	Version int          `json:"version"`
	Entries []savedProxy `json:"entries"`
}
type savedProxyRun struct {
	ID, Revision string
	Expires      time.Time
	Suspended    bool
	Scope        ProxyScope
}
type SavedProxyView struct {
	Name             string     `json:"name"`
	Scope            ProxyScope `json:"scope"`
	Revision         string     `json:"revision"`
	StartOnLaunch    bool       `json:"startOnLaunch"`
	CredentialsSaved bool       `json:"credentialsSaved"`
	Valid            bool       `json:"valid"`
	State            string     `json:"state"`
}
type SavedProxyCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func savedProxyPublic(entry savedProxy) SavedProxyView {
	scope := entry.Scope
	scope.Targets = append([]ProxyTarget(nil), scope.Targets...)
	return SavedProxyView{Name: scope.Name, Scope: scope, Revision: entry.Revision, StartOnLaunch: entry.StartOnLaunch, CredentialsSaved: true, Valid: true, State: "saved"}
}
func (c *Core) loadSavedProxies(suppressed bool) error {
	return c.loadSavedProxySettings(suppressed, true)
}

func (c *Core) loadSavedProxySettings(suppressed, protect bool) error {
	store := savedProxyStore{Version: 1, Entries: []savedProxy{}}
	path := filepath.Join(c.dir, "saved-proxies.json")
	err := readBoundedPrivateJSON(path, c.limit("resources", "profileBytes"), &store)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &localCommandError{"private_settings_unavailable", "private proxy settings could not be read; inspect the private state directory"}
	}
	if err == nil && protect {
		if err = config.Protect(path, false); err != nil {
			return &localCommandError{"private_settings_unavailable", "private proxy settings could not be protected"}
		}
	}
	if store.Version != 1 {
		return errors.New("unsupported private proxy settings version")
	}
	seen := map[string]bool{}
	for _, entry := range store.Entries {
		if !config.ValidName(entry.Scope.Name) || seen[entry.Scope.Name] || entry.Revision == "" || !validProxyCredentials(entry.Username, entry.Password) {
			return errors.New("invalid private proxy settings")
		}
		seen[entry.Scope.Name] = true
	}
	c.savedProxies = store
	c.savedProxyPending = map[string]string{}
	c.savedProxyRuns = map[string]savedProxyRun{}
	if !suppressed {
		for _, entry := range store.Entries {
			if entry.StartOnLaunch {
				c.savedProxyPending[entry.Scope.Name] = entry.Revision
			}
		}
	}
	return nil
}
func validProxyCredentials(username, password string) bool {
	return len(username) > 0 && len(username) <= 255 && len(password) > 0 && len(password) <= 255
}
func (c *Core) savedProxyView() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]SavedProxyView, 0, len(c.savedProxies.Entries))
	for _, entry := range c.savedProxies.Entries {
		view := savedProxyPublic(entry)
		view.Valid = entry.Hostname == c.profile.Settings.Hostname && entry.Scope.Backend == c.profile.Settings.Network && samePeerEpochs(entry.PeerEpochs, c.startup.Revocations)
		if state := c.startupStates["proxy:"+entry.Scope.Name]; state != "" {
			view.State = state
		}
		if !view.Valid {
			view.State = "stale"
		}
		if run, ok := c.savedProxyRuns[entry.Scope.Name]; ok {
			if run.Suspended {
				view.State = "reconnecting"
			} else if active := c.proxies[run.ID]; active != nil {
				view.State = proxyView(active)["status"].(string)
			}
		}
		entries = append(entries, view)
	}
	// The store revision intentionally excludes credentials, including their hash.
	revisions := make([][2]string, 0, len(c.savedProxies.Entries))
	for _, entry := range c.savedProxies.Entries {
		revisions = append(revisions, [2]string{entry.Scope.Name, entry.Revision})
	}
	return map[string]any{"entries": entries, "revision": privateRevision(struct {
		Entries           [][2]string
		Hostname, Network string
		Revocations       map[string]string
	}{revisions, c.profile.Settings.Hostname, c.profile.Settings.Network, c.startup.Revocations}), "suppressed": c.startupSuppressed}
}
func (c *Core) findSavedProxy(name string) (savedProxy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.savedProxies.Entries {
		if e.Scope.Name == name {
			return e, true
		}
	}
	return savedProxy{}, false
}
func (c *Core) savedProxyCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name == "proxy.saved.list" {
		var in struct{}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return c.savedProxyView(), nil
	}
	if name == "proxy.save" || name == "proxy.generate" {
		var in struct {
			Scope                 ProxyScope `json:"scope"`
			ExpectedRevision      string     `json:"expectedRevision"`
			ExpectedStoreRevision string     `json:"expectedStoreRevision"`
			Username              string     `json:"username,omitempty"`
			Password              string     `json:"password,omitempty"`
			StartOnLaunch         *bool      `json:"startOnLaunch"`
		}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		if in.StartOnLaunch == nil {
			return nil, &localCommandError{"proxy_startup_choice_required", "explicitly choose whether the saved proxy starts on future launches"}
		}
		review, err := c.reviewProxy(ctx, in.Scope)
		if err != nil {
			return nil, err
		}
		if in.ExpectedRevision == "" || in.ExpectedRevision != review.Revision || in.ExpectedStoreRevision != c.savedProxyView()["revision"] {
			return nil, &localCommandError{"proxy_saved_revision_conflict", "proxy scope or private settings changed; review them again"}
		}
		c.mu.RLock()
		conflict := false
		for _, active := range c.proxies {
			if active.scope.Name == review.Scope.Name {
				conflict = true
			}
		}
		c.mu.RUnlock()
		if conflict {
			return nil, &localCommandError{"proxy_name_conflict", "an active proxy already uses this name; stop it before reviewing a replacement"}
		}
		if name == "proxy.generate" {
			if in.Username != "" || in.Password != "" {
				return nil, &localCommandError{"proxy_credentials_required", "generation does not accept supplied credentials"}
			}
			user := make([]byte, 18)
			password := make([]byte, 32)
			if _, err = rand.Read(user); err != nil {
				return nil, errors.New("private credential generation failed")
			}
			if _, err = rand.Read(password); err != nil {
				return nil, errors.New("private credential generation failed")
			}
			in.Username = base64.RawURLEncoding.EncodeToString(user)
			in.Password = base64.RawURLEncoding.EncodeToString(password)
		}
		if !validProxyCredentials(in.Username, in.Password) {
			return nil, &localCommandError{"proxy_credentials_required", "supply username and password of 1..255 bytes through private runtime input"}
		}
		entry := savedProxy{Scope: review.Scope, Revision: randomID(), Hostname: c.profileCopy().Settings.Hostname, Username: in.Username, Password: in.Password, StartOnLaunch: *in.StartOnLaunch}
		ids := make([]string, 0, len(entry.Scope.Targets))
		for _, target := range entry.Scope.Targets {
			ids = append(ids, target.PeerID)
		}
		entry.PeerEpochs = c.reviewPeerEpochs(ids)
		c.mu.RLock()
		next := c.savedProxies
		next.Entries = append([]savedProxy(nil), next.Entries...)
		c.mu.RUnlock()
		index := slices.IndexFunc(next.Entries, func(e savedProxy) bool { return e.Scope.Name == entry.Scope.Name })
		if index < 0 {
			next.Entries = append(next.Entries, entry)
		} else {
			next.Entries[index] = entry
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		saveErr := c.writePrivateSettings("saved-proxies.json", next)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.savedProxies = next
		delete(c.savedProxyPending, entry.Scope.Name)
		delete(c.savedProxyRuns, entry.Scope.Name)
		c.mu.Unlock()
		return savedProxyPublic(entry), saveErr
	}
	var in struct {
		Name             string `json:"name"`
		ExpectedRevision string `json:"expectedRevision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	entry, found := c.findSavedProxy(in.Name)
	if !found {
		return nil, &localCommandError{"proxy_saved_not_found", "saved private proxy no longer exists"}
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != entry.Revision {
		return nil, &localCommandError{"proxy_saved_revision_conflict", "private proxy settings changed; review them again"}
	}
	switch name {
	case "proxy.reveal":
		return SavedProxyCredentials{Username: entry.Username, Password: entry.Password}, nil
	case "proxy.saved.start":
		return c.startSavedProxy(ctx, entry, nil)
	case "proxy.saved.delete", "proxy.saved.disable":
		c.mu.RLock()
		next := c.savedProxies
		next.Entries = append([]savedProxy(nil), next.Entries...)
		run := c.savedProxyRuns[in.Name]
		c.mu.RUnlock()
		index := slices.IndexFunc(next.Entries, func(e savedProxy) bool { return e.Scope.Name == in.Name })
		if name == "proxy.saved.delete" {
			next.Entries = slices.Delete(next.Entries, index, index+1)
		} else {
			next.Entries[index].StartOnLaunch = false
			next.Entries[index].Revision = randomID()
		}
		saveErr := c.writePrivateSettings("saved-proxies.json", next)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.savedProxies = next
		delete(c.savedProxyPending, in.Name)
		if name == "proxy.saved.delete" || run.Suspended {
			delete(c.savedProxyRuns, in.Name)
		}
		c.mu.Unlock()
		// Deleting saved credentials also closes any session using that record.
		if name == "proxy.saved.delete" && run.ID != "" {
			c.stopProxyIDs([]string{run.ID})
		}
		return c.savedProxyView(), saveErr
	}
	return nil, errors.New("unknown saved proxy command")
}
func (c *Core) startSavedProxy(ctx context.Context, entry savedProxy, recovery *savedProxyRun) (any, error) {
	p := c.profileCopy()
	if entry.Hostname != p.Settings.Hostname || entry.Scope.Backend != p.Settings.Network || !c.peerEpochsValid(entry.PeerEpochs) {
		return nil, &localCommandError{"proxy_saved_revision_conflict", "saved proxy belongs to another node, network or approval; review it again"}
	}
	c.mu.RLock()
	retained, hadRun := c.savedProxyRuns[entry.Scope.Name]
	c.mu.RUnlock()
	if recovery == nil && hadRun && retained.Suspended {
		return nil, &localCommandError{"proxy_name_conflict", "this proxy retains a suspended permission; stop it before starting a new lifetime"}
	}
	var expires time.Time
	recoveryID := ""
	if recovery != nil {
		if !hadRun || !retained.Suspended || retained.ID != recovery.ID || retained.Revision != entry.Revision || privateRevision(retained.Scope) != privateRevision(entry.Scope) || !retained.Expires.Equal(recovery.Expires) {
			return nil, &localCommandError{"proxy_saved_revision_conflict", "suspended proxy approval changed; review before starting again"}
		}
		expires = retained.Expires
		recoveryID = retained.ID
		if !expires.IsZero() && !time.Now().Before(expires) {
			return nil, &localCommandError{"proxy_start_cancelled", "saved proxy permission expired; start explicitly"}
		}
	}
	review, err := c.reviewProxyScope(ctx, entry.Scope, recovery != nil)
	if err != nil {
		return nil, err
	}
	result, err := c.startProxyWithReservation(ctx, review, entry.Username, entry.Password, expires, recoveryID)
	if err != nil {
		return nil, err
	}
	id := result["id"].(string)
	c.mu.Lock()
	active := c.proxies[id]
	c.savedProxyRuns[entry.Scope.Name] = savedProxyRun{ID: id, Revision: entry.Revision, Expires: active.expires, Scope: active.scope}
	c.mu.Unlock()
	return result, nil
}
func (c *Core) runSavedProxyStartup(ctx context.Context) {
	c.mu.Lock()
	pending := c.savedProxyPending
	c.savedProxyPending = map[string]string{}
	entries := append([]savedProxy(nil), c.savedProxies.Entries...)
	c.mu.Unlock()
	for _, entry := range entries {
		if entry.StartOnLaunch && pending[entry.Scope.Name] == entry.Revision {
			_, err := c.startSavedProxy(ctx, entry, nil)
			state := "started"
			if err != nil {
				state = "failed"
			}
			c.mu.Lock()
			c.startupStates["proxy:"+entry.Scope.Name] = state
			c.mu.Unlock()
		}
	}
}
func (c *Core) cancelSavedProxyRun(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, run := range c.savedProxyRuns {
		if run.ID == id {
			delete(c.savedProxyRuns, name)
		}
	}
}

// Suspend only an existing, live saved permission. Its absolute expiry survives
// transport recreation; ordinary ephemeral proxies retain stop-on-loss behavior.
func (c *Core) suspendSavedProxies() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, run := range c.savedProxyRuns {
		if active := c.proxies[run.ID]; active != nil && active.ctx.Err() == nil {
			run.Suspended = true
			c.savedProxyRuns[name] = run
		}
	}
}
func (c *Core) resumeSavedProxies(ctx context.Context) {
	c.expireSavedProxyRuns()
	c.mu.RLock()
	runs := map[string]savedProxyRun{}
	for name, run := range c.savedProxyRuns {
		if run.Suspended {
			runs[name] = run
		}
	}
	c.mu.RUnlock()
	for name, run := range runs {
		entry, ok := c.findSavedProxy(name)
		if !ok || entry.Revision != run.Revision {
			c.cancelSavedProxyRun(run.ID)
			continue
		}
		_, err := c.startSavedProxy(ctx, entry, &run)
		if err == nil {
			continue
		}
		switch networkErrorCode(err) {
		case "proxy_network_unavailable", "proxy_listener_unavailable", "proxy_start_cancelled":
			// Keep the original bounded reservation for the next maintenance pass.
			// Its scope, ID and absolute expiry are unchanged by failed transport work.
			if ctx.Err() == nil {
				continue
			}
		}
		c.cancelSavedProxyRun(run.ID)
	}
}
func (c *Core) expireSavedProxyRuns() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, run := range c.savedProxyRuns {
		if run.Suspended && !run.Expires.IsZero() && !time.Now().Before(run.Expires) {
			delete(c.savedProxyRuns, name)
		}
	}
}

// c.mu must be held. Suspended runs retain their admitted listener reservation.
func (c *Core) suspendedProxyReservations() int {
	count := 0
	for _, run := range c.savedProxyRuns {
		if run.Suspended && c.proxies[run.ID] == nil {
			count++
		}
	}
	return count
}
func (c *Core) cancelSavedPeerProxies(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.savedProxies.Entries {
		for _, target := range entry.Scope.Targets {
			if target.PeerID == peerID {
				delete(c.savedProxyPending, entry.Scope.Name)
				delete(c.savedProxyRuns, entry.Scope.Name)
			}
		}
	}
}
