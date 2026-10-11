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
type resourceCatalogViewCoordinator struct {
	state                           resourcegrant.CatalogViewEnvelope
	directory, file                 os.FileInfo
	firstUse, frozen, timeUncertain bool
	observedAt                      time.Time
	fence                           *resourcegrant.CatalogViewFence
	bootDeadline                    time.Time
	bootGrantID                     string
	bootRevision                    uint64
}

func resourceCatalogViewStatePath(dir string) string {
	return filepath.Join(dir, "resource-catalog-views", "state.json")
}
func (g *resourceCatalogViewCoordinator) closeFences() { g.fence.Close() }
func (g *resourceCatalogViewCoordinator) freeze()      { g.frozen = true; g.closeFences() }
func (g *resourceCatalogViewCoordinator) observe(now time.Time) error {
	if g.frozen || g.timeUncertain || g.state.Clock != nil && (g.state.Clock.Uncertain || now.Before(time.Unix(g.state.Clock.Seconds, int64(g.state.Clock.Nanoseconds)))) || !g.observedAt.IsZero() && now.Round(0).Before(g.observedAt.Round(0)) {
		g.timeUncertain = true
		g.closeFences()
		return resourcegrant.ErrInvalid
	}
	g.observedAt = now
	record := g.state.ActiveRecord()
	if record.ID != "" {
		if now.Unix() >= record.ExpiresAt || !g.bootDeadline.IsZero() && !now.Before(g.bootDeadline) {
			g.closeFences()
		}
	}
	return nil
}

func (b *resourcePathBinding) openCatalogView(expected os.FileInfo) error {
	// A request recertifies the same held grant directory repeatedly. Keep its
	// owned handle rather than leaking a replacement on each recertification.
	if b.catalog != nil {
		if expected != nil && (b.catalogInfo == nil || !os.SameFile(expected, b.catalogInfo)) {
			return errResourceBinding
		}
		return b.check()
	}
	path := filepath.Dir(resourceCatalogViewStatePath(b.dir))
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || expected != nil && !os.SameFile(expected, before) {
		return errResourceBinding
	}
	b.catalog, err = config.OpenPrivateChildDirectoryBound(b.dir, "resource-catalog-views", b.profileInfo)
	if err != nil {
		return err
	}
	b.catalogInfo, err = b.catalog.Stat()
	if err != nil || !os.SameFile(before, b.catalogInfo) {
		return errResourceBinding
	}
	return b.check()
}

func readCatalogViewEnvelope(b *resourcePathBinding) (resourcegrant.CatalogViewEnvelope, os.FileInfo, error) {
	var empty resourcegrant.CatalogViewEnvelope
	path := resourceCatalogViewStatePath(b.dir)
	if b.catalog == nil || b.check() != nil {
		return empty, nil, errResourceBinding
	}
	before, err := os.Lstat(path)
	if err != nil {
		return empty, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > resourcegrant.MaxBytes {
		return empty, nil, resourcegrant.ErrInvalid
	}
	f, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.catalogInfo)
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
	certified, err := config.OpenPrivateChildFileBound(filepath.Dir(path), "state.json", b.catalogInfo)
	if err != nil {
		return empty, nil, err
	}
	certifiedInfo, statErr := certified.Stat()
	closeErr := certified.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, certifiedInfo) {
		return empty, nil, resourcegrant.ErrInvalid
	}
	state, err := resourcegrant.DecodeCatalogView(data)
	if err != nil {
		return empty, nil, err
	}
	if err := b.check(); err != nil {
		return empty, nil, err
	}
	return state, after, nil
}

func (c *Core) initializeCatalogViews() {
	if c.catalogViews != nil {
		c.catalogViews.freeze()
	}
	g := &resourceCatalogViewCoordinator{}
	c.catalogViews = g
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
	g.state = resourcegrant.CatalogViewEnvelope{Version: resourcegrant.CatalogViewVersion, Target: resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: c.resourceIdentity}, HighWater: &zero, Records: []resourcegrant.CatalogViewRecord{}, Clock: &clock}
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(directory, lock os.FileInfo) error {
		b, err := openResourcePathBinding(c.dir, directory, lock)
		if err != nil {
			return err
		}
		defer b.close()
		if c.resourceDirectoryIdentity == nil || !os.SameFile(c.resourceDirectoryIdentity, b.journalInfo) {
			return errResourceBinding
		}
		info, err := os.Lstat(filepath.Dir(resourceCatalogViewStatePath(c.dir)))
		if errors.Is(err, os.ErrNotExist) {
			g.firstUse = true
			return b.check()
		}
		if err != nil {
			return err
		}
		if err := b.openCatalogView(info); err != nil {
			return err
		}
		state, _, err := readCatalogViewEnvelope(b)
		if err != nil || state.Target != g.state.Target {
			return resourcegrant.ErrInvalid
		}
		// Observe the original absolute lifetime before durable recertification.
		// Runtime activation is a separate step after relationship validation.
		observed := time.Now()
		next, err := resourcegrant.ObserveCatalogViewEnvelope(state, observed)
		if err != nil {
			return err
		}
		g.timeUncertain = next.Clock.Uncertain
		if err := c.writeCatalogViewsBound(next, b); err != nil {
			return err
		}
		g.anchorBootDeadline(observed)
		return c.observeCatalogViewsBound(b, time.Now())
	})
	if err != nil {
		g.freeze()
	}
}

// Re-read exact state under retained profile/lock/directory ownership. After a
// directory has been observed in this process, absence never becomes first use.
func (c *Core) currentCatalogViewsBound(b *resourcePathBinding) error {
	g := c.catalogViews
	if g == nil || g.frozen || g.state.Target.ResourceID != c.resourceIdentity {
		return resourcegrant.ErrInvalid
	}
	if err := b.check(); err != nil {
		g.freeze()
		return err
	}
	if g.firstUse {
		_, err := os.Lstat(filepath.Dir(resourceCatalogViewStatePath(c.dir)))
		if !errors.Is(err, os.ErrNotExist) {
			g.freeze()
			return resourcegrant.ErrInvalid
		}
		return b.check()
	}
	if err := b.openCatalogView(g.directory); err != nil {
		g.freeze()
		return err
	}
	state, file, err := readCatalogViewEnvelope(b)
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

func (c *Core) writeCatalogViewsBound(state resourcegrant.CatalogViewEnvelope, b *resourcePathBinding) error {
	g := c.catalogViews
	if g == nil || g.frozen || state.Validate() != nil || state.Target != g.state.Target || state.Version < g.state.Version || g.state.HighWater == nil || *state.HighWater < *g.state.HighWater || b.catalog == nil {
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
	if err := b.write(resourceCatalogViewStatePath(c.dir), data, c.atomicWrite); err != nil {
		g.freeze()
		return err
	}
	current, file, err := readCatalogViewEnvelope(b)
	if err != nil || resourceDigest(current) != resourceDigest(state) {
		g.freeze()
		return resourcegrant.ErrInvalid
	}
	if err := b.check(); err != nil {
		g.freeze()
		return err
	}
	g.state, g.directory, g.file, g.firstUse = current, b.catalogInfo, file, false
	return nil
}

// The caller must have just rechecked reviewed absence under the same owned
// binding. Directory creation alone never enables a grant or listener.
func (c *Core) initializeCatalogViewDirectoryBound(b *resourcePathBinding) error {
	g := c.catalogViews
	if g == nil || !g.firstUse || g.frozen {
		return resourcegrant.ErrInvalid
	}
	// Mark observed before any attempt: even ambiguous mkdir/sync failure cannot
	// be retried as pristine first use by this process.
	g.firstUse = false
	child, err := config.CreatePrivateChildDirectoryBound(c.dir, "resource-catalog-views", b.profileInfo)
	if err != nil {
		g.freeze()
		return err
	}
	b.catalog = child
	b.catalogInfo, err = child.Stat()
	if err != nil || b.check() != nil {
		g.freeze()
		return errResourceBinding
	}
	g.directory = b.catalogInfo
	return nil
}

// Terminal observations are persisted as denial before any later activation.
// Ordinary reads advance only memory; there is no per-request durable write.
func (c *Core) observeCatalogViewsBound(b *resourcePathBinding, now time.Time) error {
	g := c.catalogViews
	_ = g.observe(now)
	if g.firstUse {
		return nil
	}
	timingExpired := false
	for _, record := range g.state.Records {
		if record.State != resourcegrant.Active {
			continue
		}
		expired, uncertain := g.fence.TimingObservation(resourcegrant.CatalogViewSelector{ProtocolVersion: resourcegrant.ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision})
		timingExpired = expired
		g.timeUncertain = g.timeUncertain || uncertain
	}
	next, err := resourcegrant.ObserveCatalogViewEnvelope(g.state, now)
	if err != nil {
		g.freeze()
		return err
	}
	if timingExpired || !g.bootDeadline.IsZero() && !now.Before(g.bootDeadline) {
		next, err = resourcegrant.ExpireCatalogViewEnvelope(next)
		if err != nil {
			g.freeze()
			return err
		}
	}
	next.Clock.Uncertain = next.Clock.Uncertain || g.timeUncertain
	if next.Clock.Uncertain {
		g.timeUncertain = true
		g.closeFences()
	}
	reduced := *next.HighWater != *g.state.HighWater
	if reduced {
		g.closeFences()
	}
	if reduced || next.Clock.Uncertain && !g.state.Clock.Uncertain {
		return c.writeCatalogViewsBound(next, b)
	}
	return nil
}

// Called once after owned publication using its pre-publication observation.
// The cutoff has no authority itself and is never recalculated on reconnect.
func (g *resourceCatalogViewCoordinator) anchorBootDeadline(observed time.Time) {
	record := g.state.ActiveRecord()
	if record.ID == "" {
		return
	}
	if g.bootGrantID == record.ID && g.bootRevision == record.Revision {
		return
	}
	g.closeFences()
	g.fence = nil
	g.bootGrantID, g.bootRevision = record.ID, record.Revision
	g.bootDeadline = observed.Add(time.Unix(record.ExpiresAt, 0).Sub(observed))
}
