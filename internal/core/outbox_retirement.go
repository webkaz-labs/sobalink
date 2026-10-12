package core

import (
	"errors"
	"io"
	"os"
)

// Only known terminal evidence can retire. The timestamped key remains
// statelessly inadmissible after its hard horizon; uncertain rows stay visible.
func (o *outboxCoordinator) retireOwnedLocked() error {
	if err := o.clockLocked(); err != nil {
		return err
	}
	for id, row := range o.state.Intents {
		if !outboxRequestRetired(id, o.now()) || !o.retirementEligibleLocked(row) {
			continue
		}
		p := o.state.Payloads[row.PayloadID]
		if err := o.removePayloadNamespaceLocked(p); err != nil {
			return err
		}
		delete(o.state.Intents, id)
		delete(o.state.Payloads, p.ID)
		if err := o.saveLocked(); err != nil {
			return err
		}
		delete(o.certified, id)
		for _, child := range row.View.Children {
			delete(o.next, child.DeliveryID)
		}
	}
	// Uncommitted, discarded preparations have no remote authority. Once their
	// physical owner has joined and byte cleanup is durable, their empty private
	// namespace can retire without retaining a permanent marker directory.
	for id, p := range o.state.Payloads {
		if !p.Released || o.payloadReferencedLocked(id) {
			continue
		}
		if err := o.removePayloadNamespaceLocked(p); err != nil {
			return err
		}
		delete(o.state.Payloads, id)
		if err := o.saveLocked(); err != nil {
			return err
		}
	}
	return nil
}
func (o *outboxCoordinator) retirementEligibleLocked(row *outboxIntent) bool {
	p := o.state.Payloads[row.PayloadID]
	if p == nil || !p.Released || p.CleanupPending || (row.View.DeliveryOutcome != "confirmed" && row.View.DeliveryOutcome != "not_attempted") {
		return false
	}
	for _, child := range row.View.Children {
		if o.runs[child.DeliveryID] != nil {
			return false
		}
		switch child.Status {
		case "completed", "cancelled", "expired":
		default:
			return false
		}
	}
	for _, other := range o.state.Intents {
		if other != row && other.PayloadID == p.ID {
			return false
		}
	}
	for _, prep := range o.prepared {
		if prep.intent.PayloadID == p.ID && (prep.staging || !prep.discarded) {
			return false
		}
	}
	return true
}
func (o *outboxCoordinator) payloadReferencedLocked(id string) bool {
	for _, row := range o.state.Intents {
		if row.PayloadID == id {
			return true
		}
	}
	for _, prep := range o.prepared {
		if prep.intent.PayloadID == id && (prep.staging || !prep.discarded) {
			return true
		}
	}
	return false
}
func (o *outboxCoordinator) retirementSync(root *os.Root) error {
	if o.syncDirectory != nil {
		return o.syncDirectory(root)
	}
	return outboxSyncDirectory(root)
}

// NamespaceRemoval is persisted before the marker unlink. Recovery can then
// recognize only the exact native directory identity when its marker is gone,
// or certify the absent child after rmdir. No destination/output is traversed.
func (o *outboxCoordinator) removePayloadNamespaceLocked(p *outboxPayload) (err error) {
	if p == nil || !p.Released || p.CleanupPending || p.RootIdentity == "" || p.MarkerIdentity == "" {
		return outboxError("outbox_payload_recovery_required")
	}
	if !p.NamespaceRemoval {
		p.NamespaceRemoval = true
		if err := o.saveLocked(); err != nil {
			return err
		}
	}
	parent, err := o.root.OpenRoot("payloads")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	before, err := parent.Lstat(p.ID)
	if errors.Is(err, os.ErrNotExist) {
		return o.retirementSync(parent)
	}
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return outboxError("outbox_payload_recovery_required")
	}
	child, err := parent.OpenRoot(p.ID)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, child.Close())
		}
	}()
	identity, err := outboxIdentity(child, ".", true)
	if err != nil || identity != p.RootIdentity {
		return outboxError("outbox_payload_recovery_required")
	}
	handle, err := child.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := handle.ReadDir(2)
	closeErr := handle.Close()
	if readErr != nil && readErr != io.EOF || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if len(entries) > 1 || len(entries) == 1 && entries[0].Name() != "owner" {
		return outboxError("outbox_payload_recovery_required")
	}
	if len(entries) == 1 {
		checked, checkErr := o.payloadRootLocked(p)
		if checkErr != nil {
			return checkErr
		}
		if checkErr = checked.Close(); checkErr != nil {
			return checkErr
		}
		if err := child.Remove("owner"); err != nil {
			return err
		}
	}
	if err := o.retirementSync(child); err != nil {
		return err
	}
	current, err := parent.Lstat(p.ID)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, current) {
		return outboxError("outbox_payload_recovery_required")
	}
	if err = child.Close(); err != nil {
		return err
	}
	closed = true
	if err := parent.Remove(p.ID); err != nil {
		return err
	}
	if err := o.retirementSync(parent); err != nil {
		return err
	}
	if _, err := parent.Lstat(p.ID); !errors.Is(err, os.ErrNotExist) {
		return outboxError("outbox_payload_recovery_required")
	}
	return nil
}

func (o *outboxCoordinator) retireLocked() error {
	return o.withOwnedLocked(func() error {
		err := o.retireOwnedLocked()
		if err != nil {
			o.frozen = "outbox_payload_recovery_required"
		}
		return err
	})
}

// A restart completes only already-authorized namespace removals. General
// terminal age selection remains in maintenance after ready is certified.
func (o *outboxCoordinator) resumeNamespaceRemovalLocked() error {
	return o.withOwnedLocked(func() error {
		for id, p := range o.state.Payloads {
			if !p.NamespaceRemoval {
				continue
			}
			var rows []string
			for key, row := range o.state.Intents {
				if row.PayloadID != id {
					continue
				}
				if !outboxRequestRetired(key, o.now()) || !o.retirementEligibleLocked(row) {
					return outboxError("outbox_payload_recovery_required")
				}
				rows = append(rows, key)
			}
			if len(rows) == 0 && o.payloadReferencedLocked(id) {
				return outboxError("outbox_payload_recovery_required")
			}
			if err := o.removePayloadNamespaceLocked(p); err != nil {
				return err
			}
			for _, key := range rows {
				delete(o.state.Intents, key)
			}
			delete(o.state.Payloads, id)
			if err := o.saveLocked(); err != nil {
				return err
			}
			for _, key := range rows {
				delete(o.certified, key)
			}
		}
		return nil
	})
}
