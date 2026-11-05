package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestFilePaneBaseNamesOnly(t *testing.T) {
	for input, want := range map[string]string{"/private/example/report.txt": "report.txt", `C:\private\example\report.txt`: "report.txt", "plain.txt": "plain.txt"} {
		if got := fileLabel(input); got != want {
			t.Fatalf("label %q != %q", got, want)
		}
	}
}
func TestFilePaneSingleFlightAndMainThreadDrain(t *testing.T) {
	var p filePane
	started, release, wake := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	p.launch(context.Background(), func() { wake <- struct{}{} }, func() filePaneEvent {
		close(started)
		<-release
		return filePaneEvent{kind: "prepare", review: fileReview{PreparationID: "fictional"}}
	})
	<-started
	p.launch(context.Background(), func() {}, func() filePaneEvent { t.Error("second flight started"); return filePaneEvent{} })
	if p.review.PreparationID != "" {
		t.Fatal("worker mutated review")
	}
	close(release)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no wake")
	}
	if !p.busy || p.review.PreparationID != "" {
		t.Fatal("state changed before drain")
	}
	p.drain()
	if p.busy || p.review.PreparationID != "fictional" {
		t.Fatal("event not applied")
	}
}
func TestFilePaneUncertainMutationStaysLockedAfterStatus(t *testing.T) {
	p := filePane{busy: true, events: make(chan filePaneEvent, 1)}
	p.events <- filePaneEvent{kind: "commit", err: errors.New("transport interrupted")}
	p.drain()
	if !p.uncertain || p.busy || p.notice != "uncertain" {
		t.Fatal("uncertain mutation not locked")
	}
	p.events <- filePaneEvent{kind: "status", state: fileDispatchState{fileReview: fileReview{Status: "prepared"}}}
	p.drain()
	if !p.uncertain {
		t.Fatal("status silently enabled retry")
	}
}
func TestFilePanePickerCancelDoesNotMutate(t *testing.T) {
	p := filePane{busy: true, events: make(chan filePaneEvent, 1)}
	p.events <- filePaneEvent{kind: "pick"}
	p.drain()
	if p.busy || p.uncertain || p.review.PreparationID != "" {
		t.Fatal("cancel created review or locked action")
	}
}
func TestFilePaneCancelledContextDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var p filePane
	p.launch(ctx, func() { t.Error("wake") }, func() filePaneEvent { t.Error("started"); return filePaneEvent{} })
	if p.busy {
		t.Fatal("cancelled work started")
	}
}
func TestFilePaneIncomingReviewPinsExactOffer(t *testing.T) {
	a := exchangeTransfer{ID: "raw-transfer-id", PeerID: "fictional-peer", Direction: "incoming", Status: "awaiting-acceptance", TotalBytes: 12, Entries: []exchangeTransferEntry{{Path: "sample.txt", Size: 12}}}
	if !sameOffer(a, a) {
		t.Fatal("equal offer rejected")
	}
	for _, change := range []func(*exchangeTransfer){func(b *exchangeTransfer) { b.ID = "incoming:raw-transfer-id" }, func(b *exchangeTransfer) { b.PeerID = "other-peer" }, func(b *exchangeTransfer) { b.Status = "receiving" }, func(b *exchangeTransfer) { b.TotalBytes++ }, func(b *exchangeTransfer) { b.Entries = []exchangeTransferEntry{{Path: "other.txt", Size: 12}} }} {
		b := a
		change(&b)
		if sameOffer(a, b) {
			t.Fatal("changed offer retained acceptance")
		}
	}
}

func TestFilePaneRejectsInternalPendingStatus(t *testing.T) {
	if pendingOffer(exchangeTransfer{ID: "fictional", Direction: "incoming", Status: "pending"}) {
		t.Fatal("internal batch state is not the snapshot contract")
	}
	if !pendingOffer(exchangeTransfer{ID: "fictional", Direction: "incoming", Status: "awaiting-acceptance"}) {
		t.Fatal("snapshot offer rejected")
	}
}
func TestFilePaneRetainsReceiptAndActionID(t *testing.T) {
	p := filePane{actionID: "native-explicit-action", events: make(chan filePaneEvent, 1)}
	receipt := fileReceipt{PreparationID: "prep", Children: []fileChild{{PeerID: "fictional-peer", TransferID: "fictional-transfer"}}, Status: "committed"}
	p.events <- filePaneEvent{kind: "commit", receipt: receipt}
	p.drain()
	if p.actionID != "native-explicit-action" || len(p.receipt.Children) != 1 || p.receipt.Children[0].TransferID != "fictional-transfer" {
		t.Fatal("lost diagnostic identity or receipt")
	}
	p.events <- filePaneEvent{kind: "status", state: fileDispatchState{fileReview: fileReview{Status: "committed"}, Children: receipt.Children}}
	p.drain()
	if p.actionID != "native-explicit-action" || len(p.receipt.Children) != 1 {
		t.Fatal("status lost diagnostic identity or receipt")
	}
}

func fileOperationFixture(t *testing.T) (fileActions, <-chan struct{}) {
	t.Helper()
	wake := make(chan struct{}, 8)
	return fileActions{ctx: t.Context(), wake: func() { wake <- struct{}{} }, demo: true, enabled: true, process: "fictional-generation", peer: "fictional-peer"}, wake
}
func drainFileOperation(t *testing.T, p *filePane, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
	}
	p.drain()
}
func TestFileOperationsChooseCommitRepeatedAndChangedSelection(t *testing.T) {
	a, wake := fileOperationFixture(t)
	var p filePane
	p.chooseFiles(a)
	prep := p.preparation
	p.chooseFiles(a)
	if p.preparation != prep {
		t.Fatal("repeated choice changed preparation identity")
	}
	drainFileOperation(t, &p, wake)
	for _, change := range []func(*fileActions){func(a *fileActions) { a.peer = "other" }, func(a *fileActions) { a.process = "other" }, func(a *fileActions) { a.enabled = false }} {
		changed := a
		change(&changed)
		p.commitFiles(changed)
		if p.busy || p.actionID != prep {
			t.Fatal("invalid selection committed or changed action identity")
		}
	}
	p.commitFiles(a)
	id := p.actionID
	p.commitFiles(a)
	if p.actionID != id {
		t.Fatal("repeated commit changed request identity")
	}
	drainFileOperation(t, &p, wake)
	p.commitFiles(a)
	if p.busy || len(p.receipt.Children) != 1 || p.review.PreparationID != "" {
		t.Fatal("commit did not consume review")
	}
}
func TestFileOperationsCancelAndDiscard(t *testing.T) {
	a, wake := fileOperationFixture(t)
	client, calls := exchangeFixture(t, true, nil, nil)
	a.demo, a.client = false, client
	a.pick = func(bool) ([]string, error) { return nil, nil }
	var p filePane
	p.chooseFiles(a)
	drainFileOperation(t, &p, wake)
	if len(*calls) != 0 || p.uncertain || p.review.PreparationID != "" {
		t.Fatal("cancel mutated staging")
	}
	a.demo = true
	p.chooseFiles(a)
	drainFileOperation(t, &p, wake)
	a.peer, a.process = "other-peer", "other-generation"
	p.discardFiles(a)
	id := p.actionID
	p.discardFiles(a)
	if p.actionID != id {
		t.Fatal("repeat discard replaced request")
	}
	drainFileOperation(t, &p, wake)
	if p.review.PreparationID != "" || p.preparation != "" || p.notice != "discarded" {
		t.Fatal("discard did not clear staging")
	}
}
func fixtureIncomingOffer() exchangeTransfer {
	return exchangeTransfer{ID: "fictional-transfer", PeerID: "fictional-peer", Direction: "incoming", Status: "awaiting-acceptance", TotalBytes: 4, Entries: []exchangeTransferEntry{{Path: "sample.txt", Size: 4}}}
}
func TestFileOperationsIncomingReviewAndGuards(t *testing.T) {
	a, wake := fileOperationFixture(t)
	offer := fixtureIncomingOffer()
	a.transfers = []exchangeTransfer{offer}
	var p filePane
	p.reviewIncoming(a, offer, false)
	offer.Entries[0].Path = "changed.txt"
	if p.incoming.Entries[0].Path != "sample.txt" {
		t.Fatal("review aliases refreshed offer")
	}
	p.confirmIncoming(a)
	p.chooseDestination(a)
	if p.busy {
		t.Fatal("changed offer permitted operation")
	}
	a.transfers = []exchangeTransfer{fixtureIncomingOffer()}
	p.confirmIncoming(a)
	if p.busy {
		t.Fatal("accept without destination")
	}
	changed := a
	changed.peer = "other-peer"
	p.chooseDestination(changed)
	if p.busy {
		t.Fatal("changed recipient permitted destination")
	}
	p.chooseDestination(a)
	p.backIncoming()
	if p.incoming == nil {
		t.Fatal("back abandoned pending dialog")
	}
	drainFileOperation(t, &p, wake)
	p.confirmIncoming(a)
	id := p.actionID
	p.confirmIncoming(a)
	if p.actionID != id {
		t.Fatal("incoming repeat changed identity")
	}
	drainFileOperation(t, &p, wake)
	p.reviewIncoming(a, fixtureIncomingOffer(), true)
	if p.incoming != nil || !p.acted[offer.ID] {
		t.Fatal("acted offer was reopened")
	}
}
func TestFileOperationsIncomingDeclineBackAndUncertainty(t *testing.T) {
	a, wake := fileOperationFixture(t)
	offer := fixtureIncomingOffer()
	a.transfers = []exchangeTransfer{offer}
	var p filePane
	p.reviewIncoming(a, offer, true)
	p.backIncoming()
	if p.incoming != nil || p.acted[offer.ID] {
		t.Fatal("back acted on offer")
	}
	p.reviewIncoming(a, offer, true)
	client, calls := exchangeFixture(t, true, nil, errors.New("lost response"))
	a.demo, a.client = false, client
	p.confirmIncoming(a)
	drainFileOperation(t, &p, wake)
	id := p.actionID
	p.confirmIncoming(a)
	p.chooseDestination(a)
	p.chooseFiles(a)
	if !p.uncertain || !p.acted[offer.ID] || len(*calls) != 1 || p.busy || p.actionID != id {
		t.Fatal("uncertain incoming action retried")
	}
}
func TestFileOperationsCancelledContextPreservesState(t *testing.T) {
	a, _ := fileOperationFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	cancel()
	a.ctx = ctx
	p := filePane{actionID: "original"}
	p.chooseFiles(a)
	p.review = fixtureReview()
	p.process = a.process
	p.peer = a.peer
	p.preparation = p.review.PreparationID
	p.commitFiles(a)
	p.discardFiles(a)
	offer := fixtureIncomingOffer()
	a.transfers = []exchangeTransfer{offer}
	p.review = fileReview{}
	p.reviewIncoming(a, offer, true)
	if p.incoming != nil || p.actionID != "original" || p.busy {
		t.Fatal("cancelled operation mutated UI state")
	}
}
func TestFilePaneIgnoresStaleEvents(t *testing.T) {
	p := filePane{generation: 2, busy: true, events: make(chan filePaneEvent, 1), destination: "current"}
	for _, e := range []filePaneEvent{{kind: "prepare", review: fixtureReview()}, {kind: "destination", destination: "stale"}, {kind: "incoming"}, {kind: "commit", err: errors.New("stale error")}} {
		e.generation = 1
		p.events <- e
		p.drain()
		if !p.busy || p.review.PreparationID != "" || p.destination != "current" || p.uncertain {
			t.Fatal("stale event changed active operation")
		}
	}
}
func TestFileOperationsStatusRetainsUncertainLock(t *testing.T) {
	a, wake := fileOperationFixture(t)
	p := filePane{uncertain: true, preparation: "prepare-id", process: a.process, peer: a.peer, review: fixtureReview(), actionID: "uncertain-id"}
	p.commitFiles(a)
	p.discardFiles(a)
	p.chooseFiles(a)
	if p.busy {
		t.Fatal("uncertain mutation allowed")
	}
	p.checkStatus(a)
	drainFileOperation(t, &p, wake)
	if !p.uncertain || p.actionID != "uncertain-id" || p.notice != "prepared" {
		t.Fatal("status enabled retry or erased identity")
	}
}

func TestFilePaneCompletionSurvivesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	release := make(chan struct{})
	var p filePane
	p.launch(ctx, nil, func() filePaneEvent { <-release; return filePaneEvent{kind: "commit", err: context.Canceled} })
	cancel()
	close(release)
	select {
	case e := <-p.events:
		p.events <- e
	case <-time.After(time.Second):
		t.Fatal("cancelled operation lost completion")
	}
	p.drain()
	if p.busy || !p.uncertain {
		t.Fatal("cancelled mutation did not preserve uncertain outcome")
	}
}

func TestFileOperationsIncomingBindsWireTarget(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept", true: "decline"}[decline], func(t *testing.T) {
			a, wake := fileOperationFixture(t)
			client, calls := exchangeFixture(t, true, nil, nil)
			a.demo, a.client = false, client
			offer := fixtureIncomingOffer()
			a.transfers = []exchangeTransfer{offer}
			var p filePane
			p.reviewIncoming(a, offer, decline)
			p.destination = "fictional-destination"
			p.confirmIncoming(a)
			drainFileOperation(t, &p, wake)
			if len(*calls) != 1 {
				t.Fatal("expected one explicit command")
			}
			cmd := (*calls)[0]
			want := "transfer.accept"
			if decline {
				want = "transfer.decline"
			}
			var payload map[string]string
			if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if cmd.Name != want || cmd.RequestID != p.actionID || payload["transferId"] != offer.ID {
				t.Fatal("lost reviewed offer or request identity")
			}
			if !decline && payload["destination"] != "fictional-destination" {
				t.Fatal("lost explicit destination")
			}
			if p.incoming != nil || p.notice != "submitted" {
				t.Fatal("successful command did not consume review")
			}
		})
	}
}
func TestFileOperationsIncomingRejectsInvalidActions(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*fileActions, *filePane)
	}{
		{"read-only", func(a *fileActions, p *filePane) { a.enabled = false }},
		{"unavailable", func(a *fileActions, p *filePane) { a.demo = false; a.client = nil }},
		{"other-peer", func(a *fileActions, p *filePane) { a.peer = "other-peer" }},
		{"busy", func(a *fileActions, p *filePane) { p.busy = true }},
		{"uncertain", func(a *fileActions, p *filePane) { p.uncertain = true }},
		{"already-acted", func(a *fileActions, p *filePane) { p.acted = map[string]bool{p.incoming.ID: true} }},
		{"withdrawn", func(a *fileActions, p *filePane) { a.transfers = nil }},
	} {
		t.Run(change.name, func(t *testing.T) {
			a, _ := fileOperationFixture(t)
			offer := fixtureIncomingOffer()
			a.transfers = []exchangeTransfer{offer}
			p := filePane{incoming: &offer, destination: "fictional-destination", actionID: "original"}
			change.apply(&a, &p)
			generation := p.generation
			p.confirmIncoming(a)
			p.chooseDestination(a)
			if p.generation != generation || p.actionID != "original" {
				t.Fatal("invalid incoming action started")
			}
		})
	}
}
