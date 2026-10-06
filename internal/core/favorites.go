package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
)

// FavoriteReference is an inert pointer to one current saved definition or
// exact group name. It contains neither a copy of that scope nor permission.
// A reused name/ID still requires normal current service-selection review.
type FavoriteReference struct {
	Kind      string `json:"kind"`
	ServiceID string `json:"serviceId,omitempty"`
	GroupName string `json:"groupName,omitempty"`
}

type FavoriteEntry struct {
	FavoriteReference
	Available bool `json:"available"`
}

// FavoritesView reports reference existence only, never readiness or authority.
// Use a fresh command request ID to refresh this view after any mutation.
type FavoritesView struct {
	Version             int             `json:"version"`
	Revision            string          `json:"revision"`
	Entries             []FavoriteEntry `json:"entries"`
	DurabilityUncertain bool            `json:"durabilityUncertain"`
}

type FavoriteChangeRequest struct {
	Reference        FavoriteReference `json:"reference"`
	ExpectedRevision string            `json:"expectedRevision"`
}

// Kept separate from strict version-1 Profile for old-binary compatibility.
// Revision changes on every actual edit, including an add/remove/add cycle.
type favoritesStore struct {
	Version  int                 `json:"version"`
	Revision string              `json:"revision"`
	Entries  []FavoriteReference `json:"entries"`
}

func (r FavoriteReference) valid() bool {
	return r.Kind == "service" && config.ValidPeerID(r.ServiceID) && r.GroupName == "" ||
		r.Kind == "group" && config.ValidName(r.GroupName) && r.ServiceID == ""
}

func emptyFavorites() favoritesStore {
	return favoritesStore{Version: 1, Revision: privateRevision("favorites-v1-empty"), Entries: []FavoriteReference{}}
}

// Called only by explicit favorites commands, under c.op. Corrupt or future
// preferences cannot interfere with startup, trust, services or other settings.
func (c *Core) readFavorites() (favoritesStore, error) {
	store := favoritesStore{}
	path := filepath.Join(c.dir, "favorites.json")
	if err := readBoundedPrivateJSON(path, c.limit("resources", "profileBytes"), &store); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyFavorites(), nil
		}
		return store, &localCommandError{"favorites_unavailable", "favorites could not be read within the profileBytes budget; inspect the private favorites file and its permissions"}
	}
	revision, err := hex.DecodeString(store.Revision)
	if err != nil || len(revision) != 32 || hex.EncodeToString(revision) != store.Revision || store.Version != 1 || store.Entries == nil {
		return store, &localCommandError{"favorites_unavailable", "favorites have an unsupported version or invalid metadata; inspect the private favorites file"}
	}
	seen := make(map[FavoriteReference]bool, len(store.Entries))
	for _, ref := range store.Entries {
		if !ref.valid() || seen[ref] {
			return store, &localCommandError{"favorites_unavailable", "favorites contain invalid or duplicate references; inspect the private favorites file"}
		}
		seen[ref] = true
	}
	if err := config.Protect(path, false); err != nil {
		return store, &localCommandError{"favorites_unavailable", "favorites could not be protected; inspect the private favorites file permissions"}
	}
	return store, nil
}

func (c *Core) favoritesView(store favoritesStore) FavoritesView {
	p := c.profileCopy()
	available := make(map[FavoriteReference]bool, len(p.Services)+len(p.Groups))
	for _, service := range p.Services {
		available[FavoriteReference{Kind: "service", ServiceID: service.ID}] = true
	}
	for _, group := range p.Groups {
		available[FavoriteReference{Kind: "group", GroupName: group.Name}] = true
	}
	view := FavoritesView{Version: 1, Revision: store.Revision, Entries: make([]FavoriteEntry, 0, len(store.Entries)), DurabilityUncertain: c.favoritesUncertain}
	for _, ref := range store.Entries {
		view.Entries = append(view.Entries, FavoriteEntry{FavoriteReference: ref, Available: available[ref]})
	}
	return view
}

func (c *Core) favoritesCommand(name string, raw json.RawMessage) (any, error) {
	if int64(len(raw)) > c.limit("resources", "profileBytes") || !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, &localCommandError{"favorites_invalid_request", "favorites input is invalid or exceeds the profileBytes budget"}
	}
	var in FavoriteChangeRequest
	if name == "favorites.list" {
		if err := decodePayload(raw, &struct{}{}); err != nil {
			return nil, &localCommandError{"favorites_invalid_request", "favorites list accepts an empty object only"}
		}
	} else if err := decodePayload(raw, &in); err != nil || !in.Reference.valid() {
		return nil, &localCommandError{"favorites_invalid_request", "choose exactly one valid service ID or exact group name for the favorite"}
	}
	store, err := c.readFavorites()
	if err != nil {
		return nil, err
	}
	if name == "favorites.list" {
		return c.favoritesView(store), nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != store.Revision {
		return nil, &localCommandError{"favorites_revision_conflict", "favorites changed; list favorites again and use the current revision"}
	}
	index := slices.Index(store.Entries, in.Reference)
	if name == "favorites.add" {
		// Checking the current profile is enough for an inert preference. Actual
		// activation always uses existing full-scope selection review instead.
		candidate := c.favoritesView(favoritesStore{Entries: []FavoriteReference{in.Reference}})
		if !candidate.Entries[0].Available {
			return nil, &localCommandError{"favorites_target_missing", "the saved service or group no longer exists; review current saved definitions"}
		}
		if index < 0 {
			store.Entries = append(store.Entries, in.Reference)
		}
	} else if index >= 0 {
		store.Entries = slices.Delete(store.Entries, index, index+1)
	}
	changed := name == "favorites.add" && index < 0 || name == "favorites.remove" && index >= 0
	if !changed && !c.favoritesUncertain {
		return c.favoritesView(store), nil
	}
	store.Revision = privateRevision(randomID())
	data, err := json.Marshal(store)
	if err != nil {
		return nil, &localCommandError{"favorites_unavailable", "favorites could not be encoded"}
	}
	if int64(len(data))+1 > c.limit("resources", "profileBytes") {
		return nil, &localCommandError{"favorites_capacity", "favorites exceed the configured profileBytes budget; remove favorites or review the storage budget"}
	}
	err = c.writeAtomic(filepath.Join(c.dir, "favorites.json"), append(data, '\n'))
	if !atomicPublished(err) {
		return nil, privateAtomicError(&localCommandError{"favorites_unavailable", "favorites could not be saved; check free storage and private state permissions"}, err)
	}
	c.favoritesUncertain = err != nil
	if err == nil {
		return c.favoritesView(store), nil
	}
	// Never roll back a published outcome. Read the actual file instead of
	// assuming the requested data won, and retain uncertainty until a confirmed
	// whole-store write. Even a later failed reconciliation cannot erase it.
	published, readErr := c.readFavorites()
	outcome := privateAtomicError(&localCommandError{"favorites_persistence_uncertain", "favorites were replaced, but durability is uncertain; list favorites and inspect private storage before retrying"}, err)
	if readErr != nil {
		return nil, errors.Join(outcome, readErr)
	}
	return c.favoritesView(published), outcome
}
