package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestOutboxObserverPreservesGateAndBusyDecisions(t *testing.T) {
	for _, kind := range []string{"busy", "owner-refused", "profile", "namespace", "clock", "context"} {
		t.Run(kind, func(t *testing.T) {
			c := outboxFixture(t)
			r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "observed-gate"))
			o := c.outbox
			observer := new(outboxObserver)
			c.outboxObserver = observer // Fixture has no maintenance goroutine.
			before, _ := json.Marshal(o.state)
			want := outboxGateNone
			switch kind {
			case "busy":
				release := holdOutboxLifecycle(t, c)
				defer release()
			case "owner-refused":
				c.resourceLock = nil
			case "profile":
				c.durableProfileError = "outbox_recovery_required"
				want = outboxGateProfile
			case "namespace":
				info, err := os.Stat(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				o.info = info
				want = outboxGateNamespace
			case "clock":
				o.now = func() time.Time { return o.state.HighWater.Add(-time.Second) }
				want = outboxGateClock
			case "context":
				c.cancel()
				want = outboxGateContext
			}
			c.pumpOutbox()
			after, _ := json.Marshal(o.state)
			if string(before) != string(after) || len(o.runs) != 0 || len(o.next) != 0 || o.state.Intents[r.OutboxID].View.Status != "queued" {
				t.Fatal("observation changed a refused pump decision")
			}
			if observer.pumpEntered.Load() != 1 || observer.planned.Load() != 0 || outboxObservedGate(observer.lastGate.Load()) != want {
				t.Fatal("pump refusal history did not match the actual gate")
			}
			if kind == "busy" {
				if observer.ownerBusy.Load() != 1 || observer.ownerAdmitted.Load() != 0 || observer.gated.Load() != 0 {
					t.Fatal("busy ownership became an admitted gate")
				}
			} else if kind == "owner-refused" {
				if observer.ownerRefused.Load() != 1 || observer.ownerAdmitted.Load() != 0 || observer.gated.Load() != 0 {
					t.Fatal("ownership refusal became an admitted gate")
				}
			} else if observer.ownerAdmitted.Load() != 1 || observer.gated.Load() != 1 {
				t.Fatal("admitted gate was not observed")
			}
		})
	}
}

func TestOutboxObserverRecordsPlanningAndRunAdmissionRefusal(t *testing.T) {
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "observed-plan"))
	o := c.outbox
	c.outboxObserver = new(outboxObserver)
	now := time.Now().UTC()
	calls := 0
	o.now = func() time.Time {
		calls++
		// Existing pump order: clock guard first, then planning time. Cancelling
		// after the guard preserves planning but refuses the global worker.
		if calls == 2 {
			c.cancel()
		}
		return now
	}
	c.pumpOutbox()
	if c.outboxObserver.planned.Load() != uint64(len(r.Children)) || c.outboxObserver.runAdmissionRefused.Load() != uint64(len(r.Children)) || len(o.runs) != 0 || len(c.outgoingQueue) != 0 {
		t.Fatal("planned descriptors or global run refusal were not observed")
	}
	for _, child := range r.Children {
		if _, known := o.next[child.DeliveryID]; !known {
			t.Fatal("refused worker lost its existing retry-map history")
		}
	}
}

func TestOutboxObserverRecordsFinishOwnerAndPersistenceOutcome(t *testing.T) {
	for _, kind := range []string{"busy", "saved", "save-failed"} {
		t.Run(kind, func(t *testing.T) {
			c := outboxFixture(t)
			r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "observed-finish"))
			o := c.outbox
			observer := new(outboxObserver)
			c.outboxObserver = observer
			if kind == "busy" {
				release := holdOutboxLifecycle(t, c)
				defer release()
			}
			if kind == "save-failed" {
				o.write = func(string, []byte) error { return errors.New("synthetic storage refusal") }
			}
			b := &outgoingBatch{ID: r.Children[0].DeliveryID, outboxID: r.OutboxID, running: true, runDone: make(chan struct{})}
			o.runs[b.ID] = b
			done := false
			run := &outgoingRun{batch: b, stop: func() bool { return true }, done: func() { done = true }}
			c.finishOutboxRun(run, nil, outboxError("outbox_peer_offline"), false)
			if !done || observer.finishEntered.Load() != 1 || len(o.runs) != 0 {
				t.Fatal("observer changed physical completion")
			}
			switch kind {
			case "busy":
				if observer.finishOwnerBusy.Load() != 1 || observer.finishSaved.Load() != 0 || o.state.Intents[r.OutboxID].View.Status != "queued" {
					t.Fatal("busy finish did not retain its prior unpersisted view")
				}
			case "saved":
				if observer.finishOwnerAdmitted.Load() != 1 || observer.finishSaved.Load() != 1 || observer.finishSaveFailed.Load() != 0 {
					t.Fatal("successful finish persistence was not observed")
				}
			case "save-failed":
				if observer.finishOwnerAdmitted.Load() != 1 || observer.finishSaveFailed.Load() != 1 || o.frozen != "outbox_storage_uncertain" {
					t.Fatal("uncertain finish persistence was not observed")
				}
			}
		})
	}
}

func TestOutboxObserverInstalledAtConstruction(t *testing.T) {
	dir := t.TempDir()
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	observer := new(outboxObserver)
	c, err := Open(context.Background(), Options{Directory: dir, LifecycleLock: owner, SkipNetworkStart: true, outboxObserver: observer})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.outboxObserver != observer || c.outbox == nil {
		t.Fatal("constructor did not install exact observer before starting workers")
	}
	c.pumpOutbox()
	if observer.pumpEntered.Load() == 0 || observer.ownerAdmitted.Load() == 0 {
		t.Fatal("constructor-installed observer did not cover the first explicit pump")
	}
}

func TestOutboxNilObserverRetainsGateDecision(t *testing.T) {
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "nil-observer"))
	c.durableProfileError = "outbox_recovery_required"
	before, _ := json.Marshal(c.outbox.state)
	c.pumpOutbox()
	after, _ := json.Marshal(c.outbox.state)
	if c.outboxObserver != nil || string(before) != string(after) || len(c.outbox.runs) != 0 || len(c.outbox.next) != 0 || c.outbox.state.Intents[r.OutboxID].View.Status != "queued" {
		t.Fatal("nil observer changed existing gate behavior")
	}
}

func TestOutboxObserverRecordsPumpPersistenceRefusal(t *testing.T) {
	c := outboxFixture(t)
	outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "pump-persist"))
	c.outboxObserver = new(outboxObserver)
	c.profile.Peers = nil
	c.outbox.write = func(string, []byte) error { return errors.New("synthetic storage refusal") }
	c.pumpOutbox()
	if c.outboxObserver.gated.Load() != 1 || outboxObservedGate(c.outboxObserver.lastGate.Load()) != outboxGatePersist || c.outboxObserver.planned.Load() != 0 || c.outbox.frozen != "outbox_storage_uncertain" {
		t.Fatal("pump persistence refusal was hidden or changed")
	}
}
