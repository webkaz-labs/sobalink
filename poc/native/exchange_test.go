package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func exchangeFixture(t *testing.T, enabled bool, answer any, fail error) (*exchangeClient, *[]webui.Command) {
	t.Helper()
	calls := []webui.Command{}
	c := newExchangeClient("fictional-profile", enabled, func(ctx context.Context, dir, raw string, out any) error {
		if dir != "fictional-profile" {
			t.Fatal("wrong profile")
		}
		var cmd webui.Command
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, cmd)
		if fail != nil {
			return fail
		}
		if out != nil {
			b, err := json.Marshal(answer)
			if err != nil {
				return err
			}
			return json.Unmarshal(b, out)
		}
		return nil
	})
	return c, &calls
}
func TestExchangeReadOnly(t *testing.T) {
	c, calls := exchangeFixture(t, false, nil, nil)
	_, err := c.SendMessage(t.Context(), "exact-id", "fictional-peer", "hello")
	if !errors.Is(err, errExchangeReadOnly) || len(*calls) != 0 {
		t.Fatal("read-only send")
	}
	if err = c.AcceptFiles(t.Context(), "accept-id", "batch", "fictional-directory", true); !errors.Is(err, errExchangeReadOnly) {
		t.Fatal(err)
	}
	if err = c.DeclineFiles(t.Context(), "decline-id", "batch", true); !errors.Is(err, errExchangeReadOnly) {
		t.Fatal(err)
	}
}
func TestExchangeMessageContract(t *testing.T) {
	c, calls := exchangeFixture(t, true, exchangeMessage{ID: "message", PeerID: "fictional-peer", Direction: "outgoing", Text: "こんにちは", Status: "sent"}, nil)
	result, err := c.SendMessage(t.Context(), "retained-id", "fictional-peer", "こんにちは")
	if err != nil || result.Status != "sent" || len(*calls) != 1 {
		t.Fatal(result, err)
	}
	cmd := (*calls)[0]
	if cmd.RequestID != "retained-id" || cmd.Name != "message.send" {
		t.Fatal(cmd)
	}
	var p map[string]string
	_ = json.Unmarshal(cmd.Payload, &p)
	if p["peerId"] != "fictional-peer" || p["text"] != "こんにちは" {
		t.Fatal(p)
	}
}
func TestExchangeUncertainNeverRetries(t *testing.T) {
	failure := errors.New("connection lost")
	c, calls := exchangeFixture(t, true, nil, failure)
	_, err := c.SendMessage(t.Context(), "retained-id", "fictional-peer", "hello")
	if !errors.Is(err, failure) || len(*calls) != 1 {
		t.Fatal(err, len(*calls))
	}
}
func fixtureReview() fileReview {
	return fileReview{Version: 1, ProcessID: "fictional-generation", PreparationID: "prepare-id", ReviewRevision: "fictional-revision", PeerIDs: []string{"fictional-peer"}, Entries: []fileEntry{{ID: "entry", Path: "example.txt", Kind: "file", Size: 4}}, TotalBytes: 4, Status: "prepared"}
}
func TestExchangeFileReviewAndCommit(t *testing.T) {
	review := fixtureReview()
	c, calls := exchangeFixture(t, true, review, nil)
	got, err := c.PrepareFiles(t.Context(), "prepare-id", "fictional-generation", []string{"fictional-peer"}, []string{"fictional-source"})
	if err != nil || got.ReviewRevision != review.ReviewRevision {
		t.Fatal(got, err)
	}
	if len(*calls) != 1 || (*calls)[0].Name != "transfer.dispatch.prepare" {
		t.Fatal(*calls)
	}
	if _, err = c.CommitFiles(t.Context(), "commit-id", got, false); !errors.Is(err, errExchangeReview) || len(*calls) != 1 {
		t.Fatal("unconfirmed commit")
	}
	receipt := fileReceipt{Version: 1, ProcessID: review.ProcessID, PreparationID: review.PreparationID, DispatchID: review.PreparationID, Children: []fileChild{{PeerID: "fictional-peer", TransferID: "batch"}}, Status: "committed"}
	c, calls = exchangeFixture(t, true, receipt, nil)
	r, err := c.CommitFiles(t.Context(), "commit-id", got, true)
	if err != nil || r.Status != "committed" || len(*calls) != 1 || (*calls)[0].RequestID != "commit-id" {
		t.Fatal(r, err)
	}
	var p map[string]any
	_ = json.Unmarshal((*calls)[0].Payload, &p)
	if p["processId"] != review.ProcessID || p["preparationId"] != review.PreparationID || p["reviewRevision"] != review.ReviewRevision || p["confirm"] != true {
		t.Fatal(p)
	}
}
func TestExchangeMismatchedReviewRejected(t *testing.T) {
	review := fixtureReview()
	review.PeerIDs = []string{"other-peer"}
	c, _ := exchangeFixture(t, true, review, nil)
	if _, err := c.PrepareFiles(t.Context(), "prepare-id", "fictional-generation", []string{"fictional-peer"}, []string{"fictional-source"}); !errors.Is(err, errExchangeResponse) {
		t.Fatal(err)
	}
}
func TestExchangeStatusDiscardAndReceive(t *testing.T) {
	review := fixtureReview()
	c, calls := exchangeFixture(t, false, review, nil)
	got, err := c.FileStatus(t.Context(), "status-id", review.ProcessID, review.PreparationID)
	if err != nil || got.Status != "prepared" || len(*calls) != 1 {
		t.Fatal(got, err)
	}
	review.Status = "discarded"
	c, calls = exchangeFixture(t, true, review, nil)
	if err = c.DiscardFiles(t.Context(), "discard-id", review.ProcessID, review.PreparationID); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].Name != "transfer.dispatch.discard" {
		t.Fatal(*calls)
	}
	c, calls = exchangeFixture(t, true, nil, nil)
	if err = c.AcceptFiles(t.Context(), "accept-id", "batch", "", true); err == nil {
		t.Fatal("implicit destination")
	}
	if err = c.AcceptFiles(t.Context(), "accept-id", "batch", "fictional-destination", false); err == nil {
		t.Fatal("implicit confirmation")
	}
	if err = c.AcceptFiles(t.Context(), "accept-id", "batch", "fictional-destination", true); err != nil {
		t.Fatal(err)
	}
	if err = c.DeclineFiles(t.Context(), "decline-id", "batch", true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].Name != "transfer.accept" || (*calls)[1].Name != "transfer.decline" {
		t.Fatal(*calls)
	}
}
func TestExchangeCancelledNoCall(t *testing.T) {
	c, calls := exchangeFixture(t, true, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.SendMessage(ctx, "id", "peer", "text")
	if !errors.Is(err, context.Canceled) || len(*calls) != 0 {
		t.Fatal(err)
	}
}

func TestExchangeMessageByteLimit(t *testing.T) {
	for _, unit := range []string{"x", "界", "😀", "&"} {
		t.Run(unit, func(t *testing.T) {
			text := strings.Repeat(unit, messageframe.TextBytes/len(unit))
			text += strings.Repeat("x", messageframe.TextBytes-len(text))
			c, calls := exchangeFixture(t, true, exchangeMessage{ID: "message", PeerID: "fictional-peer", Direction: "outgoing", Text: text, Status: "sent"}, nil)
			if _, err := c.SendMessage(t.Context(), "boundary-id", "fictional-peer", text); err != nil || len(*calls) != 1 {
				t.Fatalf("exact byte boundary rejected: %v", err)
			}
			if (*calls)[0].RequestID != "boundary-id" {
				t.Fatal("request identity changed")
			}
			if _, err := c.SendMessage(t.Context(), "oversize-id", "fictional-peer", text+unit); !errors.Is(err, errExchangeMessageSize) || len(*calls) != 1 {
				t.Fatalf("oversize message reached IPC: %v", err)
			}
			p := &messagePane{draft: text}
			p.beginReview("fictional-peer", "Device")
			if p.review == nil || p.review.text != text {
				t.Fatal("exact byte boundary cannot be reviewed")
			}
		})
	}
}

func TestExchangeMessageBoundaryFitsControlEnvelope(t *testing.T) {
	for _, unit := range []string{"&", "\x01", "\n", "\"", "\\", "界", "😀"} {
		t.Run(unit, func(t *testing.T) {
			text := "x" + strings.Repeat(unit, (messageframe.TextBytes-1)/len(unit))
			text += strings.Repeat("x", messageframe.TextBytes-len(text))
			peerID := strings.Repeat("a", messageframe.IDBytes)
			requestID := strings.Repeat("&", messageframe.RequestIDBytes)
			c, calls := exchangeFixture(t, true, exchangeMessage{ID: "message", PeerID: peerID, Direction: "outgoing", Text: text, Status: "sent"}, nil)
			if _, err := c.SendMessage(t.Context(), requestID, peerID, text); err != nil {
				t.Fatal(err)
			}
			command, err := json.Marshal((*calls)[0])
			if err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(control.Request{Command: string(command)})
			if err != nil {
				t.Fatal(err)
			}
			limits := control.DefaultLimits()
			if int64(len(command)) > limits.CommandBytes || int64(len(request))+1 > limits.RequestBytes {
				t.Fatal("native text boundary exceeds control envelope")
			}
		})
	}
}
