package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/deadline"
)

type serviceSelection struct {
	IDs              []string `json:"ids"`
	Group            string   `json:"group"`
	ExpectedRevision string   `json:"expectedRevision"`
	Owner            string   `json:"owner"`
	LeaseSeconds     int      `json:"leaseSeconds"`
	Lifetime         string   `json:"lifetime,omitempty"`
	TTLSeconds       *int     `json:"ttlSeconds,omitempty"`
}

func validServiceOwner(owner string, lease int) error {
	if owner == "" && lease == 0 {
		return nil
	}
	if !config.ValidName(owner) || lease < 0 || lease > 300 {
		return errors.New("ownership requires a valid owner; an optional renewable lease must be 1..300 seconds")
	}
	return nil
}

func (a *activeService) leaseActiveAt(now time.Time) bool {
	if a.leaseSeconds == 0 {
		return a.leaseExpires.Load() == nil
	}
	expires := a.leaseExpires.Load()
	return expires != nil && deadline.Active(now, *expires)
}

func (a *activeService) definitionRevision() string {
	if a.savedRevision != "" {
		return a.savedRevision
	}
	return serviceRevision(a.spec)
}

func (c *Core) selectServices(in serviceSelection) ([]ServiceSpec, string, error) {
	return selectServices(c.profileCopy(), in)
}

func selectServices(p Profile, in serviceSelection) ([]ServiceSpec, string, error) {
	ids := append([]string(nil), in.IDs...)
	if in.Group != "" {
		if len(ids) != 0 {
			return nil, "", errors.New("select service IDs or one group")
		}
		for _, g := range p.Groups {
			if g.Name == in.Group {
				ids = append(ids, g.ServiceIDs...)
			}
		}
		if len(ids) == 0 {
			return nil, "", &localCommandError{"group_not_found", "saved group no longer exists"}
		}
	}
	if len(ids) == 0 {
		return nil, "", errors.New("select at least one saved service")
	}
	seen := map[string]bool{}
	selected := make([]ServiceSpec, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, "", errors.New("duplicate service selection")
		}
		seen[id] = true
		index := slices.IndexFunc(p.Services, func(s ServiceSpec) bool { return s.ID == id })
		if index < 0 {
			return nil, "", &localCommandError{"service_not_found", "selected saved service no longer exists"}
		}
		selected = append(selected, p.Services[index])
	}
	b, _ := json.Marshal(struct {
		Group    string
		Services []ServiceSpec
	}{in.Group, selected})
	h := sha256.Sum256(b)
	return selected, hex.EncodeToString(h[:]), nil
}

func (c *Core) selectionView(selected []ServiceSpec, revision, group, owner string) map[string]any {
	states := make([]map[string]any, 0, len(selected))
	ready := true
	for _, s := range selected {
		c.mu.RLock()
		if a := c.active[s.ID]; a != nil {
			view := c.serviceView(a)
			c.mu.RUnlock()
			if a.guard() != nil || !c.serviceTransportReady(a) || owner != "" && a.owner != owner {
				ready = false
			}
			states = append(states, view)
		} else {
			state := c.serviceStates[s.ID]
			c.mu.RUnlock()
			if state == "" {
				state = "saved"
			}
			states = append(states, map[string]any{"id": s.ID, "status": state, "owner": ""})
			ready = false
		}
	}
	return map[string]any{"services": selected, "revision": revision, "group": group, "ready": ready, "states": states, "application": "unverified"}
}

func savedStartPayload(spec ServiceSpec, owner string, lease int) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"name": spec.Name, "backend": spec.Backend, "network": spec.Network, "ports": spec.Ports,
		"excludePorts": spec.ExcludePorts, "localPort": spec.LocalPort, "loopbackHost": spec.LoopbackHost,
		"lifetime": spec.Lifetime, "ttlSeconds": spec.TTLSeconds, "peerId": spec.PeerID, "peerIds": spec.PeerIDs,
		"purpose": spec.Purpose, "discoverable": spec.Discoverable, "serviceId": spec.ServiceID, "serviceRevision": spec.ServiceRevision,
		"replaceId": spec.ID, "expectedRevision": serviceRevision(spec), "owner": owner, "leaseSeconds": lease,
	})
	return data
}

func (c *Core) selectionCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if err := validateLifetimeInput(raw); err != nil {
		return nil, err
	}
	var in serviceSelection
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if in.Owner != "" && !config.ValidName(in.Owner) {
		return nil, errors.New("invalid task owner")
	}
	if name != "services.start" && (in.Lifetime != "" || in.TTLSeconds != nil) {
		return nil, errors.New("temporary lifetime is only valid when starting saved services")
	}
	if name == "services.renew" {
		return c.renewServices(ctx, in)
	}
	// Owned cleanup remains useful after an explicitly deleted definition.
	if name == "services.stop" && in.Owner != "" && in.Group == "" && len(in.IDs) > 0 {
		saved := c.profileCopy()
		in.IDs = slices.DeleteFunc(append([]string(nil), in.IDs...), func(id string) bool {
			return !slices.ContainsFunc(saved.Services, func(s ServiceSpec) bool { return s.ID == id })
		})
		if len(in.IDs) == 0 {
			return map[string]any{"ready": false, "states": []any{}, "services": []ServiceSpec{}}, nil
		}
	}
	selected, revision, err := c.selectServices(in)
	if err != nil {
		return nil, err
	}
	if name == "service.selection" || name == "services.ready" {
		return c.selectionView(selected, revision, in.Group, in.Owner), nil
	}
	ids := make([]string, len(selected))
	for i, s := range selected {
		ids[i] = s.ID
	}
	if name == "services.start" {
		if in.ExpectedRevision == "" || in.ExpectedRevision != revision {
			return nil, &localCommandError{"service_revision_conflict", "selected definitions changed; review their complete scope again"}
		}
		if err := validServiceOwner(in.Owner, in.LeaseSeconds); err != nil {
			return nil, err
		}
		ttl := 0
		if in.TTLSeconds != nil {
			ttl = *in.TTLSeconds
		}
		override := in.Lifetime != "" || in.TTLSeconds != nil
		if override {
			for _, s := range selected {
				if _, err := serviceLifetime(in.Lifetime, ttl, s.Direction); err != nil {
					return nil, err
				}
			}
		}
		c.expireServices()
		// Check every existing owner before starting anything; retries never renew
		// a live permission or take over another task's work.
		c.mu.RLock()
		for _, s := range selected {
			if a := c.active[s.ID]; a != nil && (a.owner != in.Owner || a.leaseSeconds != in.LeaseSeconds || a.definitionRevision() != serviceRevision(s)) {
				c.mu.RUnlock()
				return nil, &localCommandError{"service_owner_conflict", "a selected service is active with another owner or scope"}
			}
			if a := c.active[s.ID]; a != nil {
				mode, seconds := s.Lifetime, s.TTLSeconds
				if override {
					mode, _ = serviceLifetime(in.Lifetime, ttl, s.Direction)
					seconds = ttl
				}
				if mode == "" && seconds > 0 {
					mode = "finite"
				}
				if a.spec.Lifetime != mode || a.spec.TTLSeconds != seconds {
					c.mu.RUnlock()
					return nil, &localCommandError{"service_lifetime_conflict", "an active service has another lifetime; stop it explicitly before choosing a new lifetime"}
				}
			}
		}
		c.mu.RUnlock()
		before := c.profileCopy()
		started := []string{}
		activation := &atomic.Bool{}
		rollback := func(cause error) (any, error) {
			activation.Store(false)
			c.stopServiceIDs(started)
			// An uncertain commit is not an unwritten save. Do not issue an
			// automatic compensating write against its newly published file.
			if errors.Is(cause, config.ErrAtomicCommitted) {
				return nil, fmt.Errorf("multi-service start stopped; saved definitions were replaced with uncertain durability: %w", cause)
			}
			restore := c.saveProfile(before)
			if atomicPublished(restore) {
				c.mu.Lock()
				c.profile = before
				c.mu.Unlock()
			}
			if restore != nil {
				cause = errors.Join(cause, fmt.Errorf("could not restore saved definitions: %w", restore))
			}
			return nil, fmt.Errorf("multi-service start failed; newly started services were stopped: %w", cause)
		}
		for _, s := range selected {
			c.mu.RLock()
			exists := c.active[s.ID] != nil
			c.mu.RUnlock()
			if exists {
				continue
			}
			command := "service.connect"
			if s.Direction == "share" {
				command = "service.share"
			}
			payload := savedStartPayload(s, in.Owner, in.LeaseSeconds)
			if override {
				var fields map[string]any
				_ = json.Unmarshal(payload, &fields)
				fields["runtimeLifetime"] = in.Lifetime
				fields["runtimeTTLSeconds"] = ttl
				payload, _ = json.Marshal(fields)
			}
			if _, err := c.startServiceCommand(ctx, command, payload, activation); err != nil {
				return rollback(err)
			}
			started = append(started, s.ID)
			c.mu.RLock()
			a := c.active[s.ID]
			c.mu.RUnlock()
			ready := a != nil && a.permissionActiveAt(time.Now()) && c.serviceTransportReady(a)
			if !ready {
				return rollback(errors.New("listener readiness was not established"))
			}
		}
		// Starting a legacy saved definition may normalize explicit defaults.
		selected, revision, err = c.selectServices(in)
		if err != nil {
			return rollback(err)
		}
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		// An earlier member can expire or lose its listener while later members
		// are preparing. Recheck the whole selection while the new group remains
		// closed; opening first would expose a partially failed group briefly.
		if !c.selectionPrepared(selected) {
			return rollback(errors.New("a selected permission or listener stopped before activation"))
		}
		activation.Store(true)
		view := c.selectionView(selected, revision, in.Group, in.Owner)
		if view["ready"] != true {
			return rollback(errors.New("not every selected listener is ready"))
		}
		return view, nil
	}
	if name == "services.stop" && in.Owner == "" && (in.ExpectedRevision == "" || in.ExpectedRevision != revision) {
		return nil, &localCommandError{"service_revision_conflict", "selected definitions changed; review their scope before stopping"}
	}
	if name != "services.stop" {
		return nil, errors.New("unknown service selection command")
	}
	c.mu.RLock()
	for _, s := range selected {
		if a := c.active[s.ID]; a != nil && a.owner != in.Owner {
			c.mu.RUnlock()
			return nil, &localCommandError{"service_owner_conflict", "another task owns a selected service; it was left unchanged"}
		}
	}
	c.mu.RUnlock()
	c.stopServiceIDs(ids)
	return c.selectionView(selected, revision, in.Group, in.Owner), nil
}

func (c *Core) selectionPrepared(selected []ServiceSpec) bool {
	for _, spec := range selected {
		c.mu.RLock()
		active := c.active[spec.ID]
		c.mu.RUnlock()
		if active == nil || !active.permissionActiveAt(time.Now()) || !c.serviceTransportReady(active) {
			return false
		}
	}
	return true
}

// A heartbeat only extends an existing, still-live lease. Selection, ownership,
// expiry validation and all updates share c.mu with stop/revoke admission; slow
// network maintenance must not make a healthy task lose its lease.
func (c *Core) renewServices(ctx context.Context, in serviceSelection) (any, error) {
	if in.Owner == "" {
		return nil, errors.New("lease renewal requires a task owner")
	}
	if in.LeaseSeconds < 1 {
		return nil, errors.New("renewal requires an existing positive renewable lease")
	}
	if err := validServiceOwner(in.Owner, in.LeaseSeconds); err != nil {
		return nil, err
	}
	c.mu.Lock()
	selected, revision, err := func() ([]ServiceSpec, string, error) {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if c.closing || c.ctx.Err() != nil {
			return nil, "", errors.New("application is stopping")
		}
		selected, revision, err := selectServices(c.profile, in)
		if err != nil {
			return nil, "", err
		}
		now := time.Now()
		for _, s := range selected {
			a := c.active[s.ID]
			if a != nil && a.owner != in.Owner {
				return nil, "", &localCommandError{"service_owner_conflict", "another task owns a selected service; it was left unchanged"}
			}
			if a == nil || !a.permissionActiveAt(now) {
				return nil, "", &localCommandError{"service_lease_expired", "a selected lease expired or stopped; start explicitly"}
			}
			if a.leaseSeconds != in.LeaseSeconds {
				return nil, "", errors.New("renewal cannot change the approved lease duration")
			}
		}
		expires := now.Add(time.Duration(in.LeaseSeconds) * time.Second)
		for _, s := range selected {
			c.active[s.ID].leaseExpires.Store(&expires)
		}
		return selected, revision, nil
	}()
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.selectionView(selected, revision, in.Group, in.Owner), nil
}

func (c *Core) stopSharesCommand(raw json.RawMessage) (any, error) {
	var in struct{}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	c.mu.RLock()
	ids := []string{}
	for id, active := range c.active {
		if active.spec.Direction == "share" {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	slices.Sort(ids)
	c.stopServiceIDs(ids)
	return map[string]any{"stopped": ids, "nodeRunning": c.nodeCopy() != nil}, nil
}
