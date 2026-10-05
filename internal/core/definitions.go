package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

// ServiceGroup contains saved references only. Saving or importing it never
// creates a permission, listener, task owner, or lease.
type ServiceGroup struct {
	Name       string            `json:"name"`
	ServiceIDs []string          `json:"serviceIds"`
	RustDesk   *RustDeskMetadata `json:"rustdesk,omitempty"`
}

// DefinitionBundle deliberately excludes node identity, trust, destinations,
// pairing secrets, login state, messages, and task runtime state.
type DefinitionBundle struct {
	Version  int            `json:"version"`
	Services []ServiceSpec  `json:"services"`
	Groups   []ServiceGroup `json:"groups"`
}

func init() {
	supportedCapacityLogical["groups"] = true
	supportedCapacityLogical["groupMembers"] = true
}

func ReadDefinitionBundle(path, stateDir string) (DefinitionBundle, error) {
	var bundle DefinitionBundle
	policy, err := readCapacityPolicy(stateDir)
	if err != nil {
		return bundle, err
	}
	err = readBoundedPrivateJSON(filepath.Clean(path), policy.Number("resources", "profileBytes"), &bundle)
	return bundle, err
}

func cloneGroups(groups []ServiceGroup) []ServiceGroup {
	groups = append([]ServiceGroup(nil), groups...)
	for i := range groups {
		groups[i].ServiceIDs = append([]string(nil), groups[i].ServiceIDs...)
		if groups[i].RustDesk != nil {
			metadata := *groups[i].RustDesk
			groups[i].RustDesk = &metadata
		}
	}
	return groups
}

func definitionsRevision(p Profile) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validateGroups(p Profile) error {
	ids := map[string]bool{}
	for _, s := range p.Services {
		ids[s.ID] = true
	}
	names := map[string]bool{}
	for _, g := range p.Groups {
		if !config.ValidName(g.Name) || names[g.Name] || len(g.ServiceIDs) == 0 {
			return errors.New("groups need unique valid names and at least one saved service")
		}
		if err := validateRustDeskGroup(p, g); err != nil {
			return err
		}
		names[g.Name] = true
		seen := map[string]bool{}
		for _, id := range g.ServiceIDs {
			if !ids[id] || seen[id] {
				return errors.New("group members must reference distinct saved services")
			}
			seen[id] = true
		}
	}
	return nil
}

func (c *Core) validateGroupCapacity(next, old Profile) error {
	if len(next.Groups) > len(old.Groups) && int64(len(next.Groups)) > c.limit("logical", "groups") {
		return &localCommandError{"group_capacity", "saved group limit reached; raise groups in capacity settings"}
	}
	for _, group := range next.Groups {
		previous := 0
		for _, saved := range old.Groups {
			if saved.Name == group.Name {
				previous = len(saved.ServiceIDs)
			}
		}
		if len(group.ServiceIDs) > previous && int64(len(group.ServiceIDs)) > c.limit("logical", "groupMembers") {
			return &localCommandError{"group_member_capacity", "group member limit reached; raise groupMembers in capacity settings"}
		}
	}
	return nil
}

func (c *Core) normalizeDefinition(s ServiceSpec) (ServiceSpec, error) {
	if s.Purpose == "" {
		s.Purpose = "generic"
	}
	if err := validateSavedDiscoveryReview(s); err != nil {
		return s, err
	}
	if !config.ValidName(s.Name) || !config.ValidPeerID(s.ID) {
		return s, errors.New("saved service needs a valid name and ID")
	}
	if s.Backend != "tailnet" && s.Backend != "lan" && s.Backend != "direct-lan" && s.Backend != "mixed" {
		return s, &localCommandError{"service_backend_required", "select an explicit connection backend for the saved service"}
	}
	if s.Direction != "share" && s.Direction != "forward" || s.Network != "tcp" && s.Network != "udp" {
		return s, errors.New("choose share or forward and TCP or UDP")
	}
	var err error
	s.Lifetime, err = serviceLifetime(s.Lifetime, s.TTLSeconds, s.Direction)
	if err != nil {
		return s, err
	}
	s.LoopbackHost, err = serviceLoopback(s.LoopbackHost)
	if err != nil {
		return s, err
	}
	ports, err := ranges.ParseWithLimit(s.Ports, c.limit("logical", "portIntervals"))
	if err != nil {
		return s, err
	}
	var excluded ranges.Set
	if s.ExcludePorts != "" {
		excluded, err = ranges.ParseWithLimit(s.ExcludePorts, c.limit("logical", "portIntervals"))
		if err != nil {
			return s, err
		}
	}
	effective, err := ports.Excluding(excluded)
	if err != nil || effective.Empty() {
		return s, errors.New("no ports remain after exclusions")
	}
	s.Ports, s.ExcludePorts = ports.String(), excluded.String()
	if s.ServiceID != "" {
		review, err := decodeDiscoveryReview(s.ServiceRevision)
		if err != nil || !review.matchesRequest(s, effective.String()) {
			return s, discoveryReviewError()
		}
	}
	if s.LocalPort < 0 || s.LocalPort > 65535 {
		return s, errors.New("local port must be 1..65535 or omitted")
	}
	if s.Direction == "share" {
		if s.PeerID != "" || s.ServiceID != "" {
			return s, errors.New("shares use allowed peer IDs and cannot follow a remote service")
		}
		if len(s.PeerIDs) < 1 || int64(len(s.PeerIDs)) > c.limit("logical", "sharePeers") {
			return s, &localCommandError{"share_peer_capacity", "select allowed peers within the sharePeers limit"}
		}
		seen := map[string]bool{}
		for _, id := range s.PeerIDs {
			if !config.ValidPeerID(id) || seen[id] {
				return s, errors.New("allowed peers must have distinct valid IDs")
			}
			seen[id] = true
		}
		reserved, _ := ranges.Parse("54543-54545")
		effective, err = effective.Excluding(reserved)
		if err != nil || effective.Empty() || s.LocalPort != 0 && (effective.Count() != 1 || reserved.Contains(uint16(s.LocalPort))) {
			return s, errors.New("share ports must exclude control ports; a mapped share needs one exposed port")
		}
	} else {
		if !config.ValidPeerID(s.PeerID) || len(s.PeerIDs) != 0 || s.Discoverable || s.ServiceID != "" && !config.ValidPeerID(s.ServiceID) {
			return s, errors.New("outbound connections need one valid peer and cannot advertise metadata")
		}
		if s.LocalPort == 0 {
			for _, interval := range effective.Intervals() {
				if interval.First < 1024 {
					return s, errors.New("local listeners need ports 1024..65535")
				}
			}
		} else if s.LocalPort < 1024 || int64(s.LocalPort)+int64(effective.Count())-1 > 65535 {
			return s, errors.New("mapped local listeners must fit ports 1024..65535")
		}
	}
	return s, nil
}

func (c *Core) saveDefinition(raw json.RawMessage) (any, error) {
	var in struct {
		Configuration    ServiceSpec `json:"configuration"`
		ExpectedRevision string      `json:"expectedRevision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	p := c.profileCopy()
	index := -1
	if in.Configuration.ID == "" {
		if in.ExpectedRevision != "" {
			return nil, errors.New("new definitions do not take a saved revision")
		}
		in.Configuration.ID = randomID()
	} else {
		for i, s := range p.Services {
			if s.ID == in.Configuration.ID {
				index = i
			}
		}
		if index < 0 {
			return nil, &localCommandError{"service_not_found", "saved service no longer exists"}
		}
		if in.ExpectedRevision == "" || in.ExpectedRevision != serviceRevision(p.Services[index]) {
			return nil, &localCommandError{"service_revision_conflict", "saved service changed; review its configuration before replacing"}
		}
		c.mu.RLock()
		active := c.active[in.Configuration.ID] != nil
		c.mu.RUnlock()
		if active {
			return nil, &localCommandError{"service_active", "stop the service before replacing its saved definition"}
		}
		if p.Services[index].Backend != "" && p.Services[index].Backend != in.Configuration.Backend {
			return nil, &localCommandError{"service_backend_mismatch", "create a new definition to use another backend"}
		}
	}
	if in.Configuration.Backend == "" {
		in.Configuration.Backend = p.Settings.Network
	}
	spec, err := c.normalizeDefinition(in.Configuration)
	if err != nil {
		return nil, err
	}
	for _, s := range p.Services {
		if s.Name == spec.Name && s.ID != spec.ID {
			return nil, &localCommandError{"service_name_conflict", "a saved service already uses this name"}
		}
	}
	if index < 0 {
		p.Services = append(p.Services, spec)
	} else {
		p.Services[index] = spec
	}
	saveErr := c.saveProfile(p)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.mu.Lock()
	c.profile = p
	delete(c.serviceStates, spec.ID)
	c.mu.Unlock()
	return SavedServiceConfiguration{Configuration: spec, Revision: serviceRevision(spec), Active: false}, saveErr
}

func (c *Core) deleteDefinition(raw json.RawMessage) (any, error) {
	var in struct {
		ID                      string `json:"id"`
		ExpectedRevision        string `json:"expectedRevision"`
		ExpectedProfileRevision string `json:"expectedProfileRevision"`
		StopActive              bool   `json:"stopActive"`
		RemoveFromGroups        bool   `json:"removeFromGroups"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	p := c.profileCopy()
	if in.ExpectedProfileRevision != "" && in.ExpectedProfileRevision != definitionsRevision(p) || in.RemoveFromGroups && in.ExpectedProfileRevision == "" {
		return nil, &localCommandError{"profile_revision_conflict", "profile changed; review deletion and group effects again"}
	}
	index := slices.IndexFunc(p.Services, func(s ServiceSpec) bool { return s.ID == in.ID })
	if index < 0 {
		return nil, &localCommandError{"service_not_found", "saved service no longer exists"}
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != serviceRevision(p.Services[index]) {
		return nil, &localCommandError{"service_revision_conflict", "review the current saved definition before deleting"}
	}
	c.mu.RLock()
	active := c.active[in.ID] != nil
	c.mu.RUnlock()
	if active && !in.StopActive {
		return nil, &localCommandError{"service_active", "deleting an active service requires explicit stopActive"}
	}
	groups := []ServiceGroup{}
	for _, group := range p.Groups {
		if slices.Contains(group.ServiceIDs, in.ID) {
			if !in.RemoveFromGroups {
				return nil, &localCommandError{"service_in_group", "review group removal and explicitly select removeFromGroups"}
			}
			group.ServiceIDs = slices.DeleteFunc(group.ServiceIDs, func(id string) bool { return id == in.ID })
		}
		if len(group.ServiceIDs) != 0 {
			groups = append(groups, group)
		}
	}
	p.Services = slices.Delete(p.Services, index, index+1)
	p.Groups = groups
	// Persist first: an uncommitted failure leaves the live permission untouched.
	saveErr := c.saveProfile(p)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.stopServiceIDs([]string{in.ID})
	c.mu.Lock()
	c.profile = p
	delete(c.serviceStates, in.ID)
	delete(c.serviceFailures, in.ID)
	delete(c.serviceDiagnostics, in.ID)
	c.mu.Unlock()
	return map[string]any{"id": in.ID, "deleted": true, "stopped": active}, saveErr
}

func (c *Core) profileDefinitionsCommand(name string, raw json.RawMessage) (any, error) {
	p := c.profileCopy()
	if name == "profile.export" {
		var in struct{}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return map[string]any{"profile": DefinitionBundle{Version: 1, Services: p.Services, Groups: p.Groups}, "revision": definitionsRevision(p), "disabled": true}, nil
	}
	var in struct {
		Profile          DefinitionBundle `json:"profile"`
		ExpectedRevision string           `json:"expectedRevision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if in.Profile.Version != 1 {
		return nil, errors.New("unsupported definition bundle version")
	}
	next := cloneProfile(p)
	next.Services = append([]ServiceSpec(nil), in.Profile.Services...)
	next.Groups = cloneGroups(in.Profile.Groups)
	ids, names := map[string]bool{}, map[string]bool{}
	for i, s := range next.Services {
		if ids[s.ID] || names[s.Name] {
			return nil, errors.New("imported services need unique IDs and names")
		}
		ids[s.ID], names[s.Name] = true, true
		var err error
		next.Services[i], err = c.normalizeDefinition(s)
		if err != nil {
			return nil, fmt.Errorf("invalid imported service: %w", err)
		}
	}
	if err := validateGroups(next); err != nil {
		return nil, err
	}
	if err := c.validateGroupCapacity(next, p); err != nil {
		return nil, err
	}
	if int64(len(next.Services)) > c.limit("logical", "savedServices") && len(next.Services) > len(p.Services) {
		return nil, &localCommandError{"service_capacity", "import exceeds savedServices capacity"}
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return nil, err
	}
	if int64(len(encoded))+1 > c.limit("resources", "profileBytes") {
		return nil, &localCommandError{"profile_capacity", "import exceeds the configured profile storage budget"}
	}
	reviewData, _ := json.Marshal(struct {
		Current  Profile
		Incoming DefinitionBundle
	}{p, DefinitionBundle{Version: 1, Services: next.Services, Groups: next.Groups}})
	reviewHash := sha256.Sum256(reviewData)
	revision := hex.EncodeToString(reviewHash[:])
	view := map[string]any{"profile": DefinitionBundle{Version: 1, Services: next.Services, Groups: next.Groups}, "revision": revision, "disabled": true, "replacesServices": len(p.Services), "preservesIdentity": true}
	removesRustDeskMetadata := []string{}
	for _, group := range p.Groups {
		if group.RustDesk == nil {
			continue
		}
		kept := slices.ContainsFunc(next.Groups, func(incoming ServiceGroup) bool {
			return incoming.Name == group.Name && incoming.RustDesk != nil && *incoming.RustDesk == *group.RustDesk
		})
		if !kept {
			removesRustDeskMetadata = append(removesRustDeskMetadata, group.Name)
		}
	}
	view["removesRustDeskMetadata"] = removesRustDeskMetadata
	if name == "profile.import.preview" {
		return view, nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != revision {
		return nil, &localCommandError{"profile_revision_conflict", "profile changed; preview the import again"}
	}
	c.mu.RLock()
	active := len(c.active) != 0
	c.mu.RUnlock()
	if active {
		return nil, &localCommandError{"service_active", "stop active services before replacing saved definitions"}
	}
	saveErr := c.saveProfile(next)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.mu.Lock()
	c.profile = next
	c.serviceStates = map[string]string{}
	c.serviceFailures = map[string]serviceFailure{}
	c.serviceDiagnostics = map[string]ServiceDiagnostic{}
	c.mu.Unlock()
	view["applied"] = true
	return view, saveErr
}

func (c *Core) groupCommand(name string, raw json.RawMessage) (any, error) {
	p := c.profileCopy()
	if name == "group.list" {
		var in struct{}
		if err := decodePayload(raw, &in); err != nil {
			return nil, err
		}
		return map[string]any{"groups": p.Groups, "revision": definitionsRevision(p)}, nil
	}
	var in struct {
		Group            ServiceGroup `json:"group"`
		ExpectedRevision string       `json:"expectedRevision"`
		RemoveRustDesk   bool         `json:"removeRustDesk"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	index := slices.IndexFunc(p.Groups, func(g ServiceGroup) bool { return g.Name == in.Group.Name })
	if index >= 0 && (in.ExpectedRevision == "" || in.ExpectedRevision != definitionsRevision(p)) {
		return nil, &localCommandError{"group_revision_conflict", "review current groups before replacing a group"}
	}
	if index < 0 && in.ExpectedRevision != "" && in.ExpectedRevision != definitionsRevision(p) {
		return nil, &localCommandError{"group_revision_conflict", "profile changed; review current groups"}
	}
	if index >= 0 && p.Groups[index].RustDesk != nil && in.Group.RustDesk == nil && !in.RemoveRustDesk {
		return nil, &localCommandError{"rustdesk_metadata_removal_required", "explicitly select removeRustDesk to detach public-key and role metadata"}
	}
	if in.RemoveRustDesk && in.Group.RustDesk != nil {
		return nil, errors.New("removeRustDesk requires omitted RustDesk metadata")
	}
	if index < 0 {
		p.Groups = append(p.Groups, in.Group)
	} else {
		p.Groups[index] = in.Group
	}
	saveErr := c.saveProfile(p)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	return map[string]any{"group": in.Group, "revision": definitionsRevision(p), "active": false}, saveErr
}
