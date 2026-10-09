package core

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// Saved state becomes eligible only after owned durable startup recertification.
// These fields are Core.op-owned. The default-false option causes no grant I/O.
type resourceGrantCoordinator struct {
	state                                 resourcegrant.Envelope
	directory, file                       os.FileInfo
	firstUse, frozen, timeUncertain       bool
	observedAt                            time.Time
	fence                                 *resourcegrant.DisclosureFence
	managementFence                       *resourcegrant.ManagementFence
	bootDeadline                          time.Time
	bootGrantID                           string
	bootRevision                          uint64
	relationshipDenied                    bool
	activation                            string
	runtime, retiring                     *resourceInspectionRuntime
	managementRuntime, managementRetiring *resourceManagementRuntime
}

func resourceGrantStatePath(dir string) string {
	return filepath.Join(dir, "resource-grants", "state.json")
}
func (g *resourceGrantCoordinator) closeFences() {
	g.fence.Close()
	g.managementFence.Close()
}
func (g *resourceGrantCoordinator) freeze() { g.frozen = true; g.closeFences(); g.retireRuntime() }
func (g *resourceGrantCoordinator) observe(now time.Time) error {
	if g.frozen || g.timeUncertain || g.state.Clock != nil && (g.state.Clock.Uncertain || now.Before(time.Unix(g.state.Clock.Seconds, int64(g.state.Clock.Nanoseconds)))) || !g.observedAt.IsZero() && now.Round(0).Before(g.observedAt.Round(0)) {
		g.timeUncertain = true
		g.closeFences()
		g.retireRuntime()
		return resourcegrant.ErrInvalid
	}
	g.observedAt = now
	record, _ := g.state.ActiveRecord()
	if record.ID != "" {
		if now.Unix() >= record.ExpiresAt || !g.bootDeadline.IsZero() && !now.Before(g.bootDeadline) {
			g.closeFences()
			g.retireRuntime()
		}
	}
	return nil
}

func (b *resourcePathBinding) openGrant(expected os.FileInfo) error {
	// A request recertifies the same held grant directory repeatedly. Keep its
	// owned handle rather than leaking a replacement on each recertification.
	if b.grant != nil {
		if expected != nil && (b.grantInfo == nil || !os.SameFile(expected, b.grantInfo)) {
			return errResourceBinding
		}
		return b.check()
	}
	path := filepath.Dir(resourceGrantStatePath(b.dir))
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || expected != nil && !os.SameFile(expected, before) {
		return errResourceBinding
	}
	b.grant, err = config.OpenPrivateChildDirectoryBound(b.dir, "resource-grants", b.profileInfo)
	if err != nil {
		return err
	}
	b.grantInfo, err = b.grant.Stat()
	if err != nil || !os.SameFile(before, b.grantInfo) {
		return errResourceBinding
	}
	return b.check()
}

func readResourceGrantEnvelope(b *resourcePathBinding) (resourcegrant.Envelope, os.FileInfo, error) {
	var empty resourcegrant.Envelope
	path := resourceGrantStatePath(b.dir)
	if b.grant == nil || b.check() != nil {
		return empty, nil, errResourceBinding
	}
	before, err := os.Lstat(path)
	if err != nil {
		return empty, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > resourcegrant.MaxBytes {
		return empty, nil, resourcegrant.ErrInvalid
	}
	f, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.grantInfo)
	if err != nil {
		return empty, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return empty, nil, resourcegrant.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, resourcegrant.MaxBytes+1))
	if err != nil {
		return empty, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return empty, nil, resourcegrant.ErrInvalid
	}
	// Revalidate file privacy and the exact entry after reading as well as before.
	certified, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.grantInfo)
	if err != nil {
		return empty, nil, err
	}
	certifiedInfo, statErr := certified.Stat()
	closeErr := certified.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, certifiedInfo) {
		return empty, nil, resourcegrant.ErrInvalid
	}
	state, err := resourcegrant.Decode(data)
	if err != nil {
		return empty, nil, err
	}
	if err := b.check(); err != nil {
		return empty, nil, err
	}
	return state, after, nil
}

func (c *Core) initializeResourceGrants() {
	if c.resourceGrants != nil {
		c.resourceGrants.freeze()
	}
	g := &resourceGrantCoordinator{}
	c.resourceGrants = g
	if c.resourceIdentity == "" || c.resourceLock == nil {
		g.freeze()
		return
	}
	zero := uint64(0)
	clock, clockErr := resourcegrant.Checkpoint(time.Now())
	if clockErr != nil {
		g.freeze()
		return
	}
	g.state = resourcegrant.Envelope{Version: resourcegrant.Version, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: c.resourceIdentity}, HighWater: &zero, Records: []resourcegrant.Record{}, Clock: &clock}
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			return errResourceBinding
		}
		info, err := os.Lstat(filepath.Dir(resourceGrantStatePath(c.dir)))
		if errors.Is(err, os.ErrNotExist) {
			g.firstUse = true
			return b.check()
		}
		if err != nil {
			return err
		}
		if err := b.openGrant(info); err != nil {
			return err
		}
		state, _, err := readResourceGrantEnvelope(b)
		if err != nil || state.Target != g.state.Target {
			return resourcegrant.ErrInvalid
		}
		// Observe the original absolute lifetime before durable recertification.
		// Runtime activation is a separate step after relationship validation.
		observed := time.Now()
		next, err := resourcegrant.ObserveEnvelope(state, observed)
		if err != nil {
			return err
		}
		g.timeUncertain = next.Clock.Uncertain
		if err := c.writeResourceGrantsBound(next, b); err != nil {
			return err
		}
		g.anchorBootDeadline(observed)
		return c.observeResourceGrantsBound(b, time.Now())
	})
	if err != nil {
		g.freeze()
	}
}

// Re-read exact state under retained profile/lock/directory ownership. After a
// directory has been observed in this process, absence never becomes first use.
func (c *Core) currentResourceGrantsBound(b *resourcePathBinding) error {
	g := c.resourceGrants
	if g == nil || g.frozen || g.state.Target.ResourceID != c.resourceIdentity {
		return resourcegrant.ErrInvalid
	}
	if err := b.check(); err != nil {
		g.freeze()
		return err
	}
	if g.firstUse {
		_, err := os.Lstat(filepath.Dir(resourceGrantStatePath(c.dir)))
		if !errors.Is(err, os.ErrNotExist) {
			g.freeze()
			return resourcegrant.ErrInvalid
		}
		return b.check()
	}
	if err := b.openGrant(g.directory); err != nil {
		g.freeze()
		return err
	}
	state, file, err := readResourceGrantEnvelope(b)
	if err != nil || g.file == nil || !os.SameFile(g.file, file) || resourceDigest(state) != resourceDigest(g.state) {
		g.freeze()
		return resourcegrant.ErrInvalid
	}
	if err := b.check(); err != nil {
		g.freeze()
		return err
	}
	return nil
}

func (c *Core) writeResourceGrantsBound(state resourcegrant.Envelope, b *resourcePathBinding) error {
	g := c.resourceGrants
	if g == nil || g.frozen || state.Validate() != nil || state.Target != g.state.Target || state.Version < g.state.Version || g.state.HighWater == nil || *state.HighWater < *g.state.HighWater || b.grant == nil {
		return resourcegrant.ErrInvalid
	}
	// Every authority publication checkpoints the greatest observed wall time.
	// A plausible later clock never clears persisted uncertainty.
	now := time.Now()
	checkpoint, err := state.Clock.Observe(now)
	if err != nil {
		g.freeze()
		return err
	}
	if g.observedAt.Round(0).After(now.Round(0)) {
		checkpoint.Uncertain = true
		checkpoint, err = checkpoint.Observe(g.observedAt)
		if err != nil {
			g.freeze()
			return err
		}
	}
	checkpoint.Uncertain = checkpoint.Uncertain || g.timeUncertain
	g.timeUncertain = checkpoint.Uncertain
	if g.timeUncertain {
		g.closeFences()
	}
	state.Clock = &checkpoint
	data, err := json.Marshal(state)
	if err != nil || len(data) > resourcegrant.MaxBytes {
		return resourcegrant.ErrInvalid
	}
	if err := b.write(resourceGrantStatePath(c.dir), data, c.atomicWrite); err != nil {
		g.freeze()
		return err
	}
	current, file, err := readResourceGrantEnvelope(b)
	if err != nil || resourceDigest(current) != resourceDigest(state) {
		g.freeze()
		return resourcegrant.ErrInvalid
	}
	if err := b.check(); err != nil {
		g.freeze()
		return err
	}
	g.state, g.directory, g.file, g.firstUse = current, b.grantInfo, file, false
	return nil
}

// The caller must have just rechecked reviewed absence under the same owned
// binding. Directory creation alone never enables a grant or listener.
func (c *Core) initializeResourceGrantDirectoryBound(b *resourcePathBinding) error {
	g := c.resourceGrants
	if g == nil || !g.firstUse || g.frozen {
		return resourcegrant.ErrInvalid
	}
	// Mark observed before any attempt: even ambiguous mkdir/sync failure cannot
	// be retried as pristine first use by this process.
	g.firstUse = false
	child, err := config.CreatePrivateChildDirectoryBound(c.dir, "resource-grants", b.profileInfo)
	if err != nil {
		g.freeze()
		return err
	}
	b.grant = child
	b.grantInfo, err = child.Stat()
	if err != nil || b.check() != nil {
		g.freeze()
		return errResourceBinding
	}
	g.directory = b.grantInfo
	return nil
}

// Terminal observations are persisted as denial before any later activation.
// Ordinary reads advance only memory; there is no per-request durable write.
func (c *Core) observeResourceGrantsBound(b *resourcePathBinding, now time.Time) error {
	g := c.resourceGrants
	_ = g.observe(now)
	if g.firstUse {
		return nil
	}
	timingExpired := false
	for _, record := range g.state.Records {
		if record.State != resourcegrant.Active {
			continue
		}
		expired, uncertain := g.fence.TimingObservation(resourcegrant.InspectRequest{ProtocolVersion: resourcegrant.ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision})
		timingExpired = expired
		g.timeUncertain = g.timeUncertain || uncertain
	}
	for _, managed := range g.state.ManagementRecords {
		record := managed.Record
		if record.State != resourcegrant.Active {
			continue
		}
		expired, uncertain := g.managementFence.TimingObservation(managementSelector(record))
		timingExpired = timingExpired || expired
		g.timeUncertain = g.timeUncertain || uncertain
	}
	next, err := resourcegrant.ObserveEnvelope(g.state, now)
	if err != nil {
		g.freeze()
		return err
	}
	if timingExpired || !g.bootDeadline.IsZero() && !now.Before(g.bootDeadline) {
		next, err = resourcegrant.ExpireEnvelope(next)
		if err != nil {
			g.freeze()
			return err
		}
	}
	next.Clock.Uncertain = next.Clock.Uncertain || g.timeUncertain
	if next.Clock.Uncertain {
		g.timeUncertain = true
		g.closeFences()
		g.retireRuntime()
	}
	reduced := *next.HighWater != *g.state.HighWater
	if reduced {
		g.closeFences()
		g.retireRuntime()
	}
	if reduced || next.Clock.Uncertain && !g.state.Clock.Uncertain {
		return c.writeResourceGrantsBound(next, b)
	}
	return nil
}

// Called once after owned publication using its pre-publication observation.
// The cutoff has no authority itself and is never recalculated on reconnect.
func (g *resourceGrantCoordinator) anchorBootDeadline(observed time.Time) {
	record, _ := g.state.ActiveRecord()
	if record.ID == "" {
		return
	}
	if g.bootGrantID == record.ID && g.bootRevision == record.Revision {
		return
	}
	g.closeFences()
	g.fence = nil
	g.managementFence = nil
	g.bootGrantID, g.bootRevision = record.ID, record.Revision
	g.bootDeadline = observed.Add(time.Unix(record.ExpiresAt, 0).Sub(observed))
	g.relationshipDenied = false
}
