package transfer

import (
	"errors"
	"os"
	"time"
)

// Cleanup has its own durable intent. A missing anchor is acceptable only
// after that exact cleanup was committed; it never supplies a save receipt.
func (m *Manager) cleanupDurableLocked(r *durableRecord) error {
	if err := m.durable.verify(); err != nil {
		return err
	}
	return m.durable.withOwner(func() error { return m.cleanupDurableOwnedLocked(r) })
}
func (m *Manager) cleanupDurableOwnedLocked(r *durableRecord) error {
	d := m.durable
	if err := d.verify(); err != nil {
		return err
	}
	if d.fault != nil {
		if err := d.fault("before_cleanup"); err != nil {
			return err
		}
	}
	if len(r.active) != 0 || r.Preparing {
		return nil
	}
	needed := false
	for _, f := range r.Files {
		if f.Anchor != "" && (f.Phase == "saved" || terminal(r.State)) {
			needed = true
		}
	}
	if !needed {
		return m.cleanupDurableStageLocked(r)
	}
	parent, root, err := openDurableRoot(r)
	if err != nil {
		return errors.Join(ErrDurableRecovery, err)
	}
	defer parent.Close()
	defer root.Close()
	stage, err := root.OpenRoot(durableStage)
	if err != nil {
		return errors.Join(ErrDurableRecovery, err)
	}
	defer stage.Close()
	for i := range r.Files {
		f := &r.Files[i]
		if f.Anchor == "" || f.Phase != "saved" && !terminal(r.State) {
			continue
		}
		if f.Phase == "publish_pending" {
			return ErrDurableRecovery
		}
		if !f.Cleanup {
			f.Cleanup = true
			if err = d.save(); err != nil {
				return err
			}
		}
		info, e := stage.Lstat(f.Anchor)
		if !errors.Is(e, os.ErrNotExist) {
			if e != nil || !info.Mode().IsRegular() || f.Identity == "" {
				return ErrDurableRecovery
			}
			id, e := durableIdentity(stage, f.Anchor, false)
			if e != nil || id != f.Identity {
				return ErrDurableRecovery
			}
			if e = stage.Remove(f.Anchor); e != nil {
				return errors.Join(ErrDurableRecovery, e)
			}
		}
		if err = retirementSyncDirectory(stage); err != nil {
			return errors.Join(ErrDurableRecovery, err)
		}
		f.Anchor = ""
		f.Cleanup = false
		if err = d.save(); err != nil {
			return err
		}
	}
	return m.cleanupDurableStageLocked(r)
}

// MaintainDurable is bounded by the ledger caps. It records already-published
// effects, fences expired work, and prunes only evidence beyond its immutable
// horizon with no active worker, owned anchor or unresolved publication.
func (m *Manager) MaintainDurable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.durable
	if d == nil {
		return nil
	}
	if err := d.verify(); err != nil {
		return err
	}
	now := d.now().UTC()
	changed := false
	var maintenanceErr error
	for _, r := range d.state.Records {
		if len(r.active) > 0 {
			if !now.Before(r.Lease.ExpiresAt) {
				for _, cancel := range r.active {
					cancel()
				}
			}
			continue
		}
		if err := m.reconcileDurableLocked(r); err != nil {
			return err
		}
		if terminal(r.State) || !now.Before(r.Lease.ExpiresAt) {
			// An unpublished intact intent has no effect. Preserve its anchor until the
			// cancellation fence is durable; only then may cleanup remove owned bytes.
			fenced := !terminal(r.State)
			for i := range r.Files {
				if r.Files[i].Phase == "publish_pending" {
					fenced = true
					r.Files[i].Phase = "receiving"
				}
			}
			if !terminal(r.State) {
				r.State = Cancelled
				r.Reason = DurableErrorCode(ErrDurableExpired)
			}
			if fenced {
				if err := d.save(); err != nil {
					return err
				}
			}
		}
		if err := m.cleanupDurableLocked(r); err != nil {
			if d.blocked != nil {
				return err
			}
			maintenanceErr = errors.Join(maintenanceErr, err)
		}
	}
	kept := d.state.Records[:0]
	for _, r := range d.state.Records {
		eligible := (r.Destination == "" || r.StageIdentity == "") && terminal(r.State) && len(r.active) == 0 && !r.Preparing && now.After(r.Lease.ReceiptUntil)
		for _, f := range r.Files {
			if f.Anchor != "" || f.Phase == "publish_pending" || f.Cleanup {
				eligible = false
			}
		}
		if eligible {
			changed = true
		} else {
			kept = append(kept, r)
		}
	}
	d.state.Records = kept
	if changed || now.Sub(d.state.HighWater) >= time.Minute {
		maintenanceErr = errors.Join(maintenanceErr, d.save())
	}
	return maintenanceErr
}

func (m *Manager) cleanupDurableStageLocked(r *durableRecord) error {
	d := m.durable
	if !terminal(r.State) || len(r.active) > 0 || r.Destination == "" || r.StageIdentity == "" {
		return nil
	}
	for _, f := range r.Files {
		if f.Anchor != "" || f.Phase == "publish_pending" {
			return nil
		}
	}
	parent, err := openDestination(r.Destination)
	if err != nil {
		return errors.Join(ErrDurableRecovery, err)
	}
	defer parent.Close()
	root, err := parent.OpenRoot(r.Root)
	if err != nil {
		return errors.Join(ErrDurableRecovery, err)
	}
	defer root.Close()
	if err = verifyPreparationParent(r.Destination, r.DestinationIdentity); err != nil {
		return ErrDurableRecovery
	}
	identity, err := durableIdentity(parent, r.Root, true)
	if err != nil || identity != r.RootIdentity {
		return ErrDurableRecovery
	}
	if !r.StageCleanup {
		r.StageCleanup = true
		if err = d.save(); err != nil {
			return err
		}
	}
	info, err := root.Lstat(durableStage)
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrDurableRecovery
		}
		identity, err = durableIdentity(root, durableStage, true)
		if err != nil || identity != r.StageIdentity {
			return ErrDurableRecovery
		}
		// Remove, never RemoveAll: an unknown entry leaves the stage charged/pinned.
		if err = root.Remove(durableStage); err != nil {
			return errors.Join(ErrDurableRecovery, err)
		}
	}
	if err = retirementSyncDirectory(root); err != nil {
		return errors.Join(ErrDurableRecovery, err)
	}
	r.StageIdentity = ""
	return d.save()
}
