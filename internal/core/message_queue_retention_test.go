package core

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func hiddenKnownText(t *testing.T, c *Core, created time.Time) *messageOutgoing {
	row := storedOutgoing(t, c, created, "synthetic-peer", "synthetic retired body")
	row.Visible, row.Active, row.NoMoreSend = false, false, true
	row.Text, row.BodyRetained = "", false
	row.View.Status = "cancelled"
	return row
}

func TestMessageQueueEvidenceRetirementIsBoundedAndPreservesObligations(t *testing.T) {
	c := messageStoreFixture(t)
	now := time.Now().UTC().Add(time.Minute)
	c.messageStore.now = func() time.Time { return now }
	created := now.Add(-9 * 24 * time.Hour)
	var eligible []*messageOutgoing
	for range messageEvidenceRetirePerTick + 3 {
		eligible = append(eligible, hiddenKnownText(t, c, created))
	}
	visible := hiddenKnownText(t, c, created)
	visible.Visible, visible.BodyRetained = true, true
	visible.Text = "synthetic retired body"
	active := hiddenKnownText(t, c, created)
	active.Active, active.BodyRetained = true, true
	active.Text = "synthetic retired body"
	unknown := hiddenKnownText(t, c, created)
	lease := storedTextLease(unknown.Offer)
	unknown.Lease = &lease
	unknown.Attempted, unknown.ExposurePossible = true, true
	unknown.View.DeliveryOutcome = "unknown"
	young := hiddenKnownText(t, c, now)
	run := hiddenKnownText(t, c, created)
	completion := hiddenKnownText(t, c, created)
	pending := hiddenKnownText(t, c, created)
	keep := []*messageOutgoing{visible, active, unknown, young, run, completion, pending}
	mutateStoredMessages(t, c, func(next *messageEnvelope) error {
		for _, row := range append(eligible, keep...) {
			next.Outgoing[row.View.OutboxID] = row
		}
		return nil
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.messageQueueRuntimeLocked()
	q.runs[run.View.OutboxID] = &messageQueueRun{}
	q.completions[completion.View.OutboxID] = &messageQueueCompletion{joined: true}
	q.pendingCancel[pending.View.OutboxID] = true
	before := cloneMessageEnvelope(c.messageStore.state)
	if err := c.retireMessageQueueEvidenceLocked(); err != nil {
		t.Fatal(err)
	}
	if len(c.messageStore.state.Outgoing) != len(before.Outgoing)-messageEvidenceRetirePerTick {
		t.Fatal("retirement exceeded or missed bounded batch")
	}
	if err := c.retireMessageQueueEvidenceLocked(); err != nil {
		t.Fatal(err)
	}
	if len(c.messageStore.state.Outgoing) != len(keep) {
		t.Fatal("eligible hidden evidence did not retire")
	}
	for _, row := range keep {
		if !reflect.DeepEqual(before.Outgoing[row.View.OutboxID], c.messageStore.state.Outgoing[row.View.OutboxID]) {
			t.Fatal("retirement changed retained evidence")
		}
	}
	if !c.messageStore.state.HighWater.Equal(now) {
		t.Fatal("retirement did not certify monotonic horizon")
	}
	if _, err := c.messageQueueStatusLocked(eligible[0].View.OutboxID); networkErrorCode(err) != "message_retired" {
		t.Fatal("retired key became fresh", err)
	}
	writes := 0
	c.messageStore.afterWrite = func() error { writes++; return nil }
	if err := c.retireMessageQueueEvidenceLocked(); err != nil || writes != 0 {
		t.Fatal("no-op retirement rewrote evidence", err, writes)
	}
}

func TestMessageQueueFailedRetirementKeepsCertifiedEvidence(t *testing.T) {
	c := messageStoreFixture(t)
	now := time.Now().UTC().Add(time.Minute)
	c.messageStore.now = func() time.Time { return now }
	row := hiddenKnownText(t, c, now.Add(-9*24*time.Hour))
	id := row.View.OutboxID
	mutateStoredMessages(t, c, func(next *messageEnvelope) error { next.Outgoing[id] = row; return nil })
	c.messageStore.afterWrite = func() error { return os.ErrPermission }
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.retireMessageQueueEvidenceLocked(); err == nil || c.messageStore.state.Outgoing[id] == nil || c.messageStore.candidate == nil {
		t.Fatal("uncertain retirement erased certified evidence", err)
	}
	c.messageStore.afterWrite = nil
	if _, err := c.messageQueueStatusLocked(id); err == nil || c.messageStore.frozen == "" {
		t.Fatal("deleted candidate locator implicitly reconciled unrelated snapshot", err)
	}
}

func TestMessageQueueExplicitOriginalStatusReconcilesOnlyChangedRow(t *testing.T) {
	c := messageQueueFixture(t)
	old := messageQueueCommitFixture(t, c, messageQueuePrepareFixture(t, c, "peer-b", "synthetic unchanged row"))
	review := messageQueuePrepareFixture(t, c, "peer-b", "synthetic uncertain row")
	c.messageStore.afterWrite = func() error { return os.ErrPermission }
	if _, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true); err == nil {
		t.Fatal("uncertain commit was certified")
	}
	c.messageStore.afterWrite = nil
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range []string{old.OutboxID, newTextRequestID(time.Now())} {
		if _, err := c.reconcileMessageQueueStatusLocked(id); err == nil || c.messageStore.frozen == "" {
			t.Fatal("unrelated locator cleared uncertainty", err)
		}
	}
	got, err := c.reconcileMessageQueueStatusLocked(review.OutboxID)
	if err != nil || got.OutboxID != review.OutboxID || got.Admission != "enqueued" || got.Status != "queued" || len(c.messageStore.state.Outgoing) != 2 || c.messageStore.candidate != nil {
		t.Fatal("exact original status did not re-certify published intent", got, err)
	}
	if len(c.messageQueue.runs) != 0 || c.outgoingActive != 0 {
		t.Fatal("status itself dispatched text")
	}
}

func TestMessageQueueOriginalOneMinuteDeadlineSurvivesIPCDelay(t *testing.T) {
	c := messageQueueFixture(t)
	now := time.Now().UTC()
	id := newTextRequestID(now.Add(-2 * time.Second))
	created, _ := textRequestCreated(id)
	expiry := created.Add(time.Minute)
	result, err := c.prepareMessageQueue(context.Background(), id, resourceCatalogProcessToken(c.resourceNonce), "peer-b", "synthetic one-minute text", expiry)
	if err != nil {
		t.Fatal("valid original minimum rejected after IPC delay", err)
	}
	got := result.(TextOutboxRecord)
	if !got.ExpiresAt.Equal(expiry) || got.OutboxID != id {
		t.Fatal("IPC delay extended original lifetime")
	}
}

func TestMessageQueueCancelUncertainNewAdmissionNeverPromotesSendAuthority(t *testing.T) {
	c := messageQueueFixture(t)
	review := messageQueuePrepareFixture(t, c, "peer-b", "synthetic uncertain cancellation")
	c.messageStore.afterWrite = func() error { return os.ErrPermission }
	if _, err := c.commitMessageQueue(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true); err == nil {
		t.Fatal("uncertain commit certified")
	}
	c.messageStore.afterWrite = nil
	_, err := c.cancelMessageQueue(context.Background(), review.OutboxID)
	if networkErrorCode(err) != "message_storage_uncertain" || c.messageStore.frozen == "" || c.messageStore.state.Outgoing[review.OutboxID] != nil || !c.messageQueue.pendingCancel[review.OutboxID] {
		t.Fatal("cancel promoted uncertain admission or lost pending fence", err)
	}
	c.mu.Lock()
	_, err = c.reconcileMessageQueueStatusLocked(review.OutboxID)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c.pumpMessageQueue()
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.messageStore.state.Outgoing[review.OutboxID]
	if row == nil || !row.NoMoreSend || !row.Cancelled || row.ExposurePossible || row.Attempted || len(c.messageQueue.runs) != 0 || c.outgoingActive != 0 {
		t.Fatal("status recovery bypassed original pending cancellation")
	}
}
