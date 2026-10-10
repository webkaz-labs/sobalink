package core

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func (c *Core) resourceCatalogLocalSettings(ctx context.Context, s resourcecatalog.Selection, l resourcecatalog.Limits, budget *resourceCatalogBudget) resourceCatalogCapture {
	if !budget.reserve(s.Target.ResourceID) {
		return resourceCatalogFailure(s, "limited")
	}
	raw, err := json.Marshal(s.Target)
	if err != nil || ctx.Err() != nil {
		return resourceCatalogFailure(s, "unavailable")
	}
	c.op.Lock()
	value, err := c.resourceCommandContext(ctx, "resource.inspect", raw)
	c.op.Unlock()
	checked := time.Now().UnixMilli()
	if err != nil {
		return resourceCatalogFailure(s, "unavailable")
	}
	descriptor, ok := value.(resource.Descriptor)
	if !ok {
		return resourceCatalogFailure(s, "invalid")
	}
	row, err := resourcecatalog.ProjectLocalSettings(s, descriptor, l)
	if err != nil {
		return resourceCatalogFailure(s, "invalid")
	}
	return resourceCatalogComplete(s, checked, []resourcecatalog.Row{row})
}

func catalogServiceLifetime(direction, lifetime string, ttl int) bool {
	switch lifetime {
	case "until-stopped":
		return direction == "forward" && ttl == 0
	case "until-revoked":
		return direction == "share" && ttl == 0
	case "", "finite":
		return ttl > 0
	}
	return false
}

func (c *Core) resourceCatalogSavedServices(ctx context.Context, s resourcecatalog.Selection, l resourcecatalog.Limits, budget *resourceCatalogBudget) resourceCatalogCapture {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closing || ctx.Err() != nil {
		return resourceCatalogFailure(s, "unavailable")
	}
	if len(c.profile.Services) > budget.rowCapacity() {
		return resourceCatalogFailure(s, "limited")
	}
	rows := make([]resourcecatalog.Row, 0, len(c.profile.Services))
	for index := range c.profile.Services {
		saved := &c.profile.Services[index]
		if ctx.Err() != nil {
			return resourceCatalogFailure(s, "unavailable")
		}
		// Copy only the scalar allowlist. Never copy a ServiceSpec or call the
		// raw serviceViews()/serviceView() helpers containing private endpoints.
		id, name, direction, network, ports, lifetime, ttl := saved.ID, saved.Name, saved.Direction, saved.Network, saved.Ports, saved.Lifetime, saved.TTLSeconds
		state := c.serviceStates[id]
		if state == "" {
			state = "saved"
		}
		extra := 0
		active := c.active[id]
		if active != nil {
			name, direction, network, lifetime, ttl = active.spec.Name, active.spec.Direction, active.spec.Network, active.spec.Lifetime, active.spec.TTLSeconds
			ports = ""
			// A normalized port interval uses at most 11 characters plus one
			// comma. Check before String allocates its temporary parts slice.
			if active.effective.IntervalCount() > budget.bytes/72 {
				return resourceCatalogFailure(s, "limited")
			}
			extra = active.effective.IntervalCount() * 12
			state = "active"
			if active.activation != nil && !active.activation.Load() {
				state = "starting"
			}
			if !c.networkReady.Load() || !active.ready.Load() {
				state = "reconnecting"
			}
			if active.error != "" {
				state = "failed"
			}
		}
		if !catalogServiceLifetime(direction, lifetime, ttl) {
			return resourceCatalogFailure(s, "invalid")
		}
		if !budget.reserveWithExtra(extra, id, name, direction, network, ports, lifetime, state) {
			return resourceCatalogFailure(s, "limited")
		}
		if active != nil {
			ports = active.effective.String()
		}
		row, err := resourcecatalog.ProjectSavedService(s, id, resourcecatalog.SavedService{Name: name, Direction: direction, Network: network, Ports: ports, Lifetime: lifetime, State: state, Application: "unverified"}, l)
		if err != nil {
			return resourceCatalogFailure(s, "invalid")
		}
		rows = append(rows, row)
	}
	return resourceCatalogComplete(s, time.Now().UnixMilli(), rows)
}

func resourceCatalogIncomingState(state transfer.BatchState) (string, bool) {
	switch state {
	case transfer.Pending:
		return "awaiting-acceptance", true
	case transfer.Accepted:
		return "queued", true
	case transfer.Receiving:
		return "transferring", true
	case transfer.Partial:
		return "failed", true
	case transfer.Completed:
		return "completed", true
	case transfer.Cancelled:
		return "cancelled", true
	case transfer.Rejected:
		return "declined", true
	}
	return "", false
}

func resourceCatalogOutgoingState(state string) bool {
	switch state {
	case "awaiting-acceptance", "queued", "transferring", "saving", "failed", "completed", "cancelled", "declined":
		return true
	}
	return false
}

func (c *Core) resourceCatalogTransfers(ctx context.Context, s resourcecatalog.Selection, l resourcecatalog.Limits, budget *resourceCatalogBudget) resourceCatalogCapture {
	maxRows := budget.rowCapacity()
	if maxRows <= 0 {
		return resourceCatalogFailure(s, "limited")
	}
	c.mu.RLock()
	manager := c.transfers
	c.mu.RUnlock()
	incoming, err := manager.ActivitySnapshot(ctx, maxRows)
	if err != nil {
		if errors.Is(err, transfer.ErrLimit) {
			return resourceCatalogFailure(s, "limited")
		}
		return resourceCatalogFailure(s, "unavailable")
	}
	type pointer struct {
		key   string
		batch *outgoingBatch
	}
	c.mu.RLock()
	if c.closing || c.transfers != manager || len(c.outgoing) > maxRows-len(incoming) {
		c.mu.RUnlock()
		return resourceCatalogFailure(s, "limited")
	}
	pointers := make([]pointer, 0, len(c.outgoing))
	for key, batch := range c.outgoing {
		pointers = append(pointers, pointer{key, batch})
	}
	c.mu.RUnlock()
	sort.Slice(pointers, func(i, j int) bool { return pointers[i].key < pointers[j].key })
	rows := make([]resourcecatalog.Row, 0, len(incoming)+len(pointers))
	for _, activity := range incoming {
		if ctx.Err() != nil {
			return resourceCatalogFailure(s, "unavailable")
		}
		state, ok := resourceCatalogIncomingState(activity.State)
		if !ok {
			return resourceCatalogFailure(s, "invalid")
		}
		if !budget.reserve(activity.ID, activity.PeerID, state) {
			return resourceCatalogFailure(s, "limited")
		}
		row, err := resourcecatalog.ProjectTransfer(s, activity.ID, "incoming", resourcecatalog.Transfer{PeerID: activity.PeerID, State: state, TotalBytes: activity.TotalBytes, CompletedBytes: activity.CompletedBytes}, l)
		if err != nil {
			return resourceCatalogFailure(s, "invalid")
		}
		rows = append(rows, row)
	}
	for _, pointer := range pointers {
		if pointer.batch == nil || ctx.Err() != nil {
			return resourceCatalogFailure(s, "unavailable")
		}
		b := pointer.batch
		b.mu.Lock()
		id, peer, state, completed := b.ID, b.PeerID, b.State, b.Completed
		if id != pointer.key || !resourceCatalogOutgoingState(state) {
			b.mu.Unlock()
			return resourceCatalogFailure(s, "invalid")
		}
		if !budget.reserve(id, peer, state) {
			b.mu.Unlock()
			return resourceCatalogFailure(s, "limited")
		}
		var total int64
		valid := true
		// Manifest sizes are immutable admitted scalars. Paths, hashes, files,
		// spool state and errors are neither copied nor serialized.
		for index := range b.Manifest.Entries {
			size := b.Manifest.Entries[index].Size
			if ctx.Err() != nil || size < 0 || size > capacity.MaxJSONInteger-total {
				valid = false
				break
			}
			total += size
		}
		b.mu.Unlock()
		if !valid {
			return resourceCatalogFailure(s, "invalid")
		}
		row, err := resourcecatalog.ProjectTransfer(s, id, "outgoing", resourcecatalog.Transfer{PeerID: peer, State: state, TotalBytes: total, CompletedBytes: completed}, l)
		if err != nil {
			return resourceCatalogFailure(s, "invalid")
		}
		rows = append(rows, row)
	}
	// A different outgoing set cannot be advertised as a complete capture.
	// There is no retry and no manager/batch lock is held while taking Core.mu.
	c.mu.RLock()
	current := !c.closing && c.transfers == manager && len(c.outgoing) == len(pointers)
	for _, pointer := range pointers {
		current = current && c.outgoing[pointer.key] == pointer.batch
	}
	c.mu.RUnlock()
	if !current || ctx.Err() != nil {
		return resourceCatalogFailure(s, "unavailable")
	}
	return resourceCatalogComplete(s, time.Now().UnixMilli(), rows)
}
