package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func releasedOutboxPreparation(t *testing.T) (*Core, *outboxPayload) {
	t.Helper()
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "retirement-fixture")
	p := c.outbox.state.Payloads[c.outbox.prepared[r.OutboxID].intent.PayloadID]
	if _, err := c.discardOutbox(r.OutboxID, r.ProcessID); err != nil {
		t.Fatal(err)
	}
	if !p.Released || c.outbox.payloadReferencedLocked(p.ID) {
		t.Fatal("fixture was not released and unreferenced")
	}
	return c, p
}

func TestOutboxNamespaceRetirementResumesEveryRemovalCut(t *testing.T) {
	for _, cut := range []string{"intent", "marker-unlinked", "directory-removed"} {
		t.Run(cut, func(t *testing.T) {
			c, p := releasedOutboxPreparation(t)
			o := c.outbox
			path := filepath.Join(o.path(), "payloads", p.ID)
			err := o.withOwnedLocked(func() error {
				p.NamespaceRemoval = true
				if err := o.saveLocked(); err != nil {
					return err
				}
				if cut == "intent" {
					return nil
				}
				root, err := o.payloadRootLocked(p)
				if err != nil {
					return err
				}
				err = root.Remove("owner")
				if err == nil {
					err = outboxSyncDirectory(root)
				}
				err = errors.Join(err, root.Close())
				if err != nil {
					return err
				}
				if cut == "marker-unlinked" {
					return nil
				}
				parent, err := o.root.OpenRoot("payloads")
				if err != nil {
					return err
				}
				err = parent.Remove(p.ID)
				if err == nil {
					err = outboxSyncDirectory(parent)
				}
				return errors.Join(err, parent.Close())
			})
			if err != nil {
				t.Fatal(err)
			}
			outboxReopenFixture(t, c)
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned removal cut was not reconciled", err)
			}
			if c.outbox.state.Payloads[p.ID] != nil || len(c.outbox.state.Intents) != 0 || c.outbox.frozen != "" {
				t.Fatal("retirement retained authority or frozen accounting")
			}
		})
	}
}

func TestOutboxNamespaceRetirementPreservesForeignReplacement(t *testing.T) {
	c, p := releasedOutboxPreparation(t)
	o := c.outbox
	p.NamespaceRemoval = true
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.path(), "payloads", p.ID)
	moved := filepath.Join(t.TempDir(), "owned-evidence")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.resumeNamespaceRemovalLocked(); err == nil {
		t.Fatal("replacement namespace was adopted")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("foreign directory was removed or changed", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "owner")); err != nil {
		t.Fatal("original ownership evidence changed", err)
	}
}

func TestOutboxNamespaceRetirementBusyDoesNotMutate(t *testing.T) {
	c, p := releasedOutboxPreparation(t)
	o := c.outbox
	before, _ := json.Marshal(o.state)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.resourceLock.WithOwnershipInfo(c.dir, func(os.FileInfo, os.FileInfo) error { close(entered); <-release; return nil })
	}()
	<-entered
	err := o.retireLocked()
	close(release)
	if finish := <-done; finish != nil {
		t.Fatal(finish)
	}
	after, _ := json.Marshal(o.state)
	if networkErrorCode(err) != "outbox_storage_busy" || string(before) != string(after) || p.NamespaceRemoval || o.frozen != "" {
		t.Fatal("busy retirement changed state", err)
	}
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Payloads[p.ID] != nil {
		t.Fatal("released namespace did not retire after busy owner left")
	}
}

func TestOutboxRetirementDoesNotHideUnknownEvidence(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "unknown-retirement")
	outboxCommitFixture(t, c, r)
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err != nil {
		t.Fatal(err)
	}
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	row.View.DeliveryOutcome = "unknown"
	row.View.Children[0].DeliveryOutcome = "unknown"
	row.Deliveries[0].Attempted = true
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	horizon, _ := outboxRequestHorizon(r.OutboxID)
	o.now = func() time.Time { return horizon }
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Intents[r.OutboxID] == nil {
		t.Fatal("unknown evidence was retired")
	}
	raw, _ := json.Marshal(map[string]string{"outboxId": r.OutboxID})
	value, err := c.outboxCommand(context.Background(), "outbox.status", "status", raw)
	if err != nil || value.(OutboxRecord).DeliveryOutcome != "unknown" {
		t.Fatal("aged unknown key lost evidence", err)
	}
}

func TestOutboxRetirementWaitsForHardHorizonAndOwners(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "retirement-boundary")
	outboxCommitFixture(t, c, r)
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err != nil {
		t.Fatal(err)
	}
	o := c.outbox
	horizon, _ := outboxRequestHorizon(r.OutboxID)
	o.now = func() time.Time { return horizon.Add(-time.Nanosecond) }
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Intents[r.OutboxID] == nil {
		t.Fatal("record retired before stateless key horizon")
	}
	o.now = func() time.Time { return horizon }
	o.runs[r.Children[0].DeliveryID] = &outgoingBatch{}
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Intents[r.OutboxID] == nil {
		t.Fatal("record retired while a child owner remained")
	}
	delete(o.runs, r.Children[0].DeliveryID)
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.state.Intents[r.OutboxID] != nil || len(o.state.Payloads) != 0 {
		t.Fatal("joined terminal record did not release all namespace metadata")
	}
}
