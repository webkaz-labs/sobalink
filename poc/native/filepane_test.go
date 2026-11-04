package main

import (
	"context"
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
