package transfer

import (
	"errors"
	"io"
)

// Startup inventories only the recorded private stage, never user output trees.
// Unknown entries, partial identity publication and byte-budget violations fence
// new admissions. Historical certified receipts stay queryable.
func (m *Manager) inventoryDurableLocked() error {
	d := m.durable
	if err := d.verify(); err != nil {
		return err
	}
	var blocked error
	for _, r := range d.state.Records {
		if r.Preparing {
			blocked = errors.Join(blocked, ErrDurableRecovery)
			continue
		}
		if r.Destination == "" || r.StageIdentity == "" {
			continue
		}
		parent, err := openDestination(r.Destination)
		if err != nil {
			if !terminal(r.State) {
				blocked = errors.Join(blocked, ErrDurableRecovery)
			}
			continue
		}
		root, err := parent.OpenRoot(r.Root)
		if err == nil {
			identity, e := durableIdentity(parent, r.Root, true)
			if e != nil || identity != r.RootIdentity {
				err = ErrDurableRecovery
			}
		}
		if err == nil {
			err = verifyPreparationParent(r.Destination, r.DestinationIdentity)
		}
		if err != nil {
			if root != nil {
				_ = root.Close()
			}
			_ = parent.Close()
			if !terminal(r.State) {
				blocked = errors.Join(blocked, ErrDurableRecovery)
			}
			continue
		}
		stage, err := root.OpenRoot(durableStage)
		if err != nil {
			_ = root.Close()
			_ = parent.Close()
			if !r.StageCleanup && !terminal(r.State) {
				blocked = errors.Join(blocked, ErrDurableRecovery)
			}
			continue
		}
		identity, e := rootIdentity(stage)
		if e != nil || identity != r.StageIdentity {
			err = ErrDurableRecovery
		}
		expected := map[string]durableFile{}
		for _, f := range r.Files {
			if f.Anchor != "" {
				expected[f.Anchor] = f
			}
		}
		if err == nil {
			dir, e := stage.Open(".")
			err = e
			if e == nil {
				names, readErr := dir.Readdirnames(len(expected) + 1)
				_ = dir.Close()
				if readErr != nil && readErr != io.EOF {
					err = readErr
				}
				if len(names) > len(expected) {
					err = ErrDurableRecovery
				}
				for _, name := range names {
					f, ok := expected[name]
					if !ok || f.Identity == "" {
						err = ErrDurableRecovery
						break
					}
					info, e := stage.Lstat(name)
					if e != nil || !info.Mode().IsRegular() || info.Size() > f.Entry.Size {
						err = ErrDurableRecovery
						break
					}
					identity, e := durableIdentity(stage, name, false)
					if e != nil || identity != f.Identity {
						err = ErrDurableRecovery
						break
					}
					delete(expected, name)
				}
				for _, missing := range expected {
					if !missing.Cleanup {
						err = ErrDurableRecovery
					}
				}
			}
		}
		_ = stage.Close()
		_ = root.Close()
		_ = parent.Close()
		if err != nil {
			blocked = errors.Join(blocked, ErrDurableRecovery)
			continue
		}
		if err = m.reconcileDurableLocked(r); err != nil {
			blocked = errors.Join(blocked, err)
		}
	}
	d.admissionBlocked = blocked
	return blocked
}
func (d *durableReceiver) verifyAdmission() error {
	if err := d.verify(); err != nil {
		return err
	}
	return d.admissionBlocked
}

// CheckDurableReceiver reconciles owned stages after reopen, before any sender
// scheduler or capability publication. It does not infer fresh acceptance.
func (m *Manager) CheckDurableReceiver() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inventoryDurableLocked()
}
