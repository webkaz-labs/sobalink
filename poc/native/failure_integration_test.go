package main

// These are headless UI/adapter integration tests with a scripted caller. They
// do not establish daemon deduplication, real delivery, or OS-dialog acceptance.
import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func failureCommand(t *testing.T, wire string) webui.Command {
	t.Helper()
	var cmd webui.Command
	if err := json.Unmarshal([]byte(wire), &cmd); err != nil {
		t.Error(err)
	}
	return cmd
}
func failureNextCommand(t *testing.T, commands <-chan webui.Command) webui.Command {
	t.Helper()
	select {
	case cmd := <-commands:
		return cmd
	case <-time.After(5 * time.Second):
		t.Fatal("expected scripted command was not sent")
		return webui.Command{}
	}
}
func failureReply(out, answer any) error {
	b, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func failureWaitFile(t *testing.T, tt *ui.Tester, p *filePane) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.busy && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if p.busy {
		t.Fatal("scripted caller did not complete")
	}
	tt.Frame()
}
func failureFileTester(t *testing.T, p *filePane, c *exchangeClient, process, peer *string, transfers *[]exchangeTransfer) *ui.Tester {
	t.Helper()
	return ui.NewTester(func(ctx *ui.Context) {
		ui.Column(ctx).Fill().Children(func() { p.view(ctx, t.Context(), false, false, true, c, *process, *peer, *transfers) })
	}, 760, 1600)
}
func failureClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func TestFailureIntegrationMessagePostAcknowledgmentError(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "transport-error-after-scripted-acceptance"
		if malformed {
			name = "mismatched-success-response"
		}
		t.Run(name, func(t *testing.T) {
			p := messagePane{draft: "fictional reviewed message"}
			p.beginReview("fictional-peer", "Fictional device")
			original := *p.review
			var calls atomic.Int32
			commands := make(chan webui.Command, 2)
			c := newExchangeClient("fictional-profile", true, func(_ context.Context, _, wire string, out any) error {
				calls.Add(1)
				commands <- failureCommand(t, wire)
				// Simulate the side effect having happened before the response is lost.
				if !malformed {
					return errors.New("scripted response lost after acceptance")
				}
				return failureReply(out, exchangeMessage{ID: "fictional-message", PeerID: "other-peer", Direction: "outgoing", Text: original.text, Status: "sent"})
			})
			p.send(t.Context(), nil, false, true, c, original.peerID)
			waitMessageEvent(t, &p)
			for range 3 {
				p.send(t.Context(), nil, false, true, c, original.peerID)
				p.beginReview("other-peer", "Other")
			}
			cmd := failureNextCommand(t, commands)
			if calls.Load() != 1 || cmd.RequestID != original.requestID || !p.uncertain || !p.review.attempted || p.review.requestID != original.requestID || p.review.text != original.text || p.draft != original.text {
				t.Fatal("lost request identity or retried uncertain send")
			}
		})
	}
}

func TestFailureIntegrationCommitLockedAfterStatus(t *testing.T) {
	for _, status := range []string{"prepared", "committed", "error"} {
		t.Run(status, func(t *testing.T) {
			review := fixtureReview()
			p := filePane{review: review, process: review.ProcessID, peer: review.PeerIDs[0], preparation: review.PreparationID}
			process, peer := p.process, p.peer
			transfers := []exchangeTransfer{}
			var calls atomic.Int32
			commands := make(chan webui.Command, 8)
			c := newExchangeClient("fictional-profile", true, func(_ context.Context, _, wire string, out any) error {
				calls.Add(1)
				cmd := failureCommand(t, wire)
				commands <- cmd
				if cmd.Name == "transfer.dispatch.commit" {
					return errors.New("scripted post-acceptance disconnect")
				}
				if cmd.Name != "transfer.dispatch.status" {
					return errors.New("unexpected mutation")
				}
				if status == "error" {
					return errors.New("scripted status unavailable")
				}
				state := fileDispatchState{fileReview: review}
				state.Status = status
				if status == "committed" {
					state.DispatchID = review.PreparationID
					state.Children = []fileChild{{PeerID: peer, TransferID: "fictional-transfer"}}
				}
				return failureReply(out, state)
			})
			tt := failureFileTester(t, &p, c, &process, &peer, &transfers)
			failureClick(t, tt, "Confirm send")
			failureWaitFile(t, tt, &p)
			commit := failureNextCommand(t, commands)
			id := p.actionID
			if commit.RequestID != id || id == "" || !p.uncertain || !reflect.DeepEqual(p.review, review) {
				t.Fatal("uncertain commit lost its review or ID")
			}
			for range 3 {
				failureClick(t, tt, "Confirm send")
				failureClick(t, tt, "Discard")
			}
			if calls.Load() != 1 || p.actionID != id {
				t.Fatal("uncertain commit retried")
			}
			failureClick(t, tt, "Check staging status")
			failureWaitFile(t, tt, &p)
			query := failureNextCommand(t, commands)
			var payload map[string]string
			if err := json.Unmarshal(query.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if query.Name != "transfer.dispatch.status" || payload["processId"] != review.ProcessID || payload["preparationId"] != review.PreparationID || !p.uncertain || p.actionID != id || calls.Load() != 2 {
				t.Fatal("status changed original action or retried mutation")
			}
			if status == "committed" {
				if len(p.receipt.Children) != 1 || p.review.PreparationID != "" {
					t.Fatal("committed recovery did not retain receipt")
				}
			} else {
				failureClick(t, tt, "Confirm send")
				if calls.Load() != 2 || p.actionID != id {
					t.Fatal("status unlocked uncertain commit")
				}
			}
		})
	}
}

func TestFailureIntegrationStaleReviewSelection(t *testing.T) {
	for _, change := range []string{"process", "peer"} {
		t.Run(change, func(t *testing.T) {
			review := fixtureReview()
			p := filePane{review: review, process: review.ProcessID, peer: review.PeerIDs[0], preparation: review.PreparationID, actionID: "retained-prepare-id"}
			process, peer := p.process, p.peer
			if change == "process" {
				process = "fictional-restarted-generation"
			} else {
				peer = "other-peer"
			}
			transfers := []exchangeTransfer{}
			var calls atomic.Int32
			c := newExchangeClient("fictional-profile", true, func(context.Context, string, string, any) error { calls.Add(1); return errors.New("must not call") })
			tt := failureFileTester(t, &p, c, &process, &peer, &transfers)
			failureClick(t, tt, "Confirm send")
			if calls.Load() != 0 || p.busy || p.actionID != "retained-prepare-id" || !reflect.DeepEqual(p.review, review) {
				t.Fatal("stale selection allowed commit or changed review")
			}
		})
	}
}

func TestFailureIntegrationReceivePinsOfferDestinationAndAttempt(t *testing.T) {
	for _, decline := range []bool{false, true} {
		name := "accept"
		if decline {
			name = "decline"
		}
		t.Run(name, func(t *testing.T) {
			offer := exchangeTransfer{ID: "fictional-raw-transfer", PeerID: "fictional-peer", Direction: "incoming", Status: "awaiting-acceptance", TotalBytes: 7, Entries: []exchangeTransferEntry{{ID: "entry", Path: "fictional.txt", Kind: "file", Size: 7}}}
			p := filePane{incoming: &offer, destination: "fictional destination/選択", decline: decline}
			process, peer := "fictional-generation", offer.PeerID
			transfers := []exchangeTransfer{offer}
			var calls atomic.Int32
			commands := make(chan webui.Command, 2)
			c := newExchangeClient("fictional-profile", true, func(_ context.Context, _, wire string, _ any) error {
				calls.Add(1)
				commands <- failureCommand(t, wire)
				return errors.New("scripted response lost after acceptance")
			})
			tt := failureFileTester(t, &p, c, &process, &peer, &transfers)
			label := "Confirm accept"
			if decline {
				label = "Confirm decline"
			}
			failureClick(t, tt, label)
			failureWaitFile(t, tt, &p)
			cmd := failureNextCommand(t, commands)
			id := p.actionID
			var payload map[string]string
			if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"transferId": offer.ID}
			if !decline {
				want["destination"] = p.destination
			}
			if cmd.Name != "transfer."+name || cmd.RequestID != id || id == "" || !reflect.DeepEqual(payload, want) || !p.acted[offer.ID] || !p.uncertain || p.incoming == nil {
				t.Fatal("receive did not retain exact target, destination, or attempted state")
			}
			for range 3 {
				failureClick(t, tt, label)
			}
			if calls.Load() != 1 || p.actionID != id {
				t.Fatal("receive command retried after uncertain result")
			}
		})
	}
}

func TestFailureIntegrationReceiveRejectsChangedOffer(t *testing.T) {
	for _, change := range []string{"missing", "peer", "entry", "status", "destination"} {
		t.Run(change, func(t *testing.T) {
			offer := exchangeTransfer{ID: "fictional-transfer", PeerID: "fictional-peer", Direction: "incoming", Status: "awaiting-acceptance", TotalBytes: 7, Entries: []exchangeTransferEntry{{Path: "fictional.txt", Size: 7}}}
			p := filePane{incoming: &offer, destination: "fictional-destination"}
			current := offer
			current.Entries = append([]exchangeTransferEntry(nil), offer.Entries...)
			process, peer := "fictional-generation", offer.PeerID
			transfers := []exchangeTransfer{current}
			switch change {
			case "missing":
				transfers = nil
			case "peer":
				peer = "other-peer"
			case "entry":
				transfers[0].Entries[0].Path = "changed.txt"
			case "status":
				transfers[0].Status = "receiving"
			case "destination":
				p.destination = ""
			}
			var calls atomic.Int32
			c := newExchangeClient("fictional-profile", true, func(context.Context, string, string, any) error { calls.Add(1); return errors.New("must not call") })
			tt := failureFileTester(t, &p, c, &process, &peer, &transfers)
			failureClick(t, tt, "Confirm accept")
			if calls.Load() != 0 || p.busy || p.actionID != "" || p.acted[offer.ID] {
				t.Fatal("stale or incomplete receive review allowed mutation")
			}
		})
	}
}

func TestFailureIntegrationAdapterRejectsStaleResponses(t *testing.T) {
	for _, operation := range []string{"prepare", "commit", "status", "discard"} {
		for _, mismatch := range []string{"process", "preparation"} {
			t.Run(operation+"/"+mismatch, func(t *testing.T) {
				review := fixtureReview()
				stale := review
				if mismatch == "process" {
					stale.ProcessID = "fictional-old-generation"
				} else {
					stale.PreparationID = "fictional-other-review"
				}
				var answer any = stale
				switch operation {
				case "commit":
					answer = fileReceipt{Version: 1, ProcessID: stale.ProcessID, PreparationID: stale.PreparationID, DispatchID: stale.PreparationID, Status: "committed", Children: []fileChild{{PeerID: review.PeerIDs[0], TransferID: "fictional-transfer"}}}
				case "discard":
					stale.Status = "discarded"
					answer = stale
				}
				c, calls := exchangeFixture(t, true, answer, nil)
				var err error
				switch operation {
				case "prepare":
					_, err = c.PrepareFiles(t.Context(), review.PreparationID, review.ProcessID, review.PeerIDs, []string{"fictional-source"})
				case "commit":
					_, err = c.CommitFiles(t.Context(), "retained-action-id", review, true)
				case "status":
					_, err = c.FileStatus(t.Context(), "retained-action-id", review.ProcessID, review.PreparationID)
				case "discard":
					err = c.DiscardFiles(t.Context(), "retained-action-id", review.ProcessID, review.PreparationID)
				}
				if !errors.Is(err, errExchangeResponse) || len(*calls) != 1 {
					t.Fatal("stale response accepted or automatically retried", err, len(*calls))
				}
				wantID := "retained-action-id"
				if operation == "prepare" {
					wantID = review.PreparationID
				}
				if (*calls)[0].RequestID != wantID {
					t.Fatal("adapter replaced caller request ID")
				}
			})
		}
	}
}
