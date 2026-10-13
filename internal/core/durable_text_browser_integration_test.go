//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// Real embedded UI and three independent Core processes: one sender, one exact
// selected receiver and an untouched peer. Restart, lost peer ACK and hidden
// history replay are qualified separately by the native text acceptance.
func TestDurableTextThreeProcessBrowser(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_TRANSFER_BROWSER_NATIVE") != "1" || os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unperformed: explicit native process/browser acceptance required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, peers, keys := startDispatchProcessPeers(t, ctx)
	const body = "  synthetic browser text: \"exact\"\n合成テキスト\n"
	inventory := catalogProcessDecode[browserOutboxSnapshot](t, peers[0].invoke(t, "fixture.snapshot", struct{}{}))
	recipients := []map[string]string{}
	for _, key := range keys[1:] {
		for _, peer := range inventory.Peers {
			if peer.ID == key {
				recipients = append(recipients, map[string]string{"id": peer.ID, "name": peer.Name})
			}
		}
	}
	if len(recipients) != 2 || inventory.ProcessID == "" {
		t.Fatal("actual text browser recipient inventory unavailable")
	}
	viewer := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "fixture.web", struct{}{}))
	var review TextOutboxRecord
	var final TextOutboxRecord
	evidence := func(index int) durableTextProcessEvidence {
		return catalogProcessDecode[durableTextProcessEvidence](t, peers[index].invoke(t, "fixture.text-evidence", map[string]string{"messageId": review.RequestID, "text": body}))
	}
	status := func() TextOutboxRecord {
		return catalogProcessDecode[TextOutboxRecord](t, peers[0].invoke(t, "message.queue.status", map[string]string{"outboxId": review.RequestID}))
	}
	immutable := func(record TextOutboxRecord) {
		t.Helper()
		if record.Version != 1 || record.Kind != "text_outbox" || record.OutboxID != review.OutboxID || record.RequestID != review.RequestID || record.MessageID != review.MessageID || record.PeerID != review.PeerID || record.ReviewRevision != review.ReviewRevision || !record.CreatedAt.Equal(review.CreatedAt) || !record.ExpiresAt.Equal(review.ExpiresAt) || record.TextBytes != review.TextBytes || record.SHA256 != review.SHA256 || record.Text != body {
			t.Fatal("browser text admission or recovery changed immutable review")
		}
	}
	wait := func(condition func() bool, message string) {
		t.Helper()
		until := time.Now().Add(30 * time.Second)
		for !condition() {
			if time.Now().After(until) || ctx.Err() != nil {
				t.Fatal(message)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > 16384 {
			t.Fatal("bounded private text browser evidence unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("private text browser evidence unreadable")
		}
		return data
	}
	assertSaved := func() {
		t.Helper()
		final = status()
		immutable(final)
		if final.Admission != "enqueued" || final.Status != "completed" || final.DeliveryOutcome != "confirmed" || final.Receipt == nil {
			t.Fatal("original text request lacks actual receiver-saved confirmation")
		}
		receipt := final.Receipt
		if receipt.MessageID != review.MessageID || receipt.TextBytes != review.TextBytes || receipt.TextSHA256 != review.SHA256 || receipt.Lease.DeliveryID != review.MessageID || receipt.Lease.RecipientID != keys[1] || !receipt.Lease.ExpiresAt.Equal(review.ExpiresAt) {
			t.Fatal("text receipt does not bind exact selected peer and reviewed body")
		}
		sender, receiver, untouched := evidence(0), evidence(1), evidence(2)
		if sender.OutgoingRows != 1 || sender.IncomingRows != 0 || sender.VisibleRows != 1 || !sender.BodyExact || !sender.BodyRetained || !reflect.DeepEqual(sender.Receipt, receipt) || receiver.IncomingRows != 1 || receiver.OutgoingRows != 0 || receiver.VisibleRows != 1 || !receiver.BodyExact || !receiver.BodyRetained || receiver.RetainedTextBytes != len(body) || !reflect.DeepEqual(receiver.Receipt, receipt) || untouched.IncomingRows != 0 || untouched.OutgoingRows != 0 || untouched.VisibleRows != 0 {
			t.Fatal("browser recovery duplicated text, changed content, or reached an unselected peer")
		}
	}
	private := runTransferProcessBrowser(t, ctx, map[string]any{"schema": 1, "textOutbox": true, "viewer": viewer, "peerId": keys[1], "peerName": recipients[0]["name"], "recipients": recipients, "processId": inventory.ProcessID}, false, func(private string) {
		for _, stage := range []string{"review", "recovered"} {
			path := filepath.Join(private, "text-browser-"+stage+".json")
			wait(func() bool { _, err := os.Stat(path); return err == nil }, "actual text browser checkpoint did not arrive")
			if stage == "review" {
				review = catalogProcessDecode[struct {
					Prepared TextOutboxRecord `json:"prepared"`
				}](t, read(path)).Prepared
				created, ok := textRequestCreated(review.RequestID)
				if !ok || review.Version != 1 || review.Kind != "text_outbox" || review.Admission != "prepared" || review.Status != "prepared" || review.ProcessID != inventory.ProcessID || review.OutboxID != review.RequestID || review.MessageID != review.RequestID || review.PeerID != keys[1] || review.Text != body || review.TextBytes != int64(len(body)) || review.SHA256 != textSHA256(body) || review.ReviewRevision == "" || !review.CreatedAt.Equal(created) || !review.ExpiresAt.Equal(created.Add(time.Hour)) || review.DeliveryOutcome != "not_attempted" || review.Receipt != nil {
					t.Fatal("browser did not review one exact peer, body and one-hour deadline")
				}
				current := status()
				immutable(current)
				if current.Admission != "prepared" || current.Status != "prepared" {
					t.Fatal("browser sent text before explicit queue consent")
				}
				for i := range peers {
					observed := evidence(i)
					if observed.IncomingRows != 0 || observed.OutgoingRows != 0 || observed.VisibleRows != 0 || observed.Receipt != nil || observed.ExposurePossible || observed.RunActive {
						t.Fatal("text preparation created an actual send before browser consent")
					}
				}
			} else {
				result := catalogProcessDecode[struct {
					Prepared  TextOutboxRecord `json:"prepared"`
					Admitted  TextOutboxRecord `json:"admitted"`
					Recovered TextOutboxRecord `json:"recovered"`
					Prepares  int              `json:"prepares"`
					Commits   int              `json:"commits"`
					Drops     int              `json:"drops"`
					Checks    int              `json:"checks"`
				}](t, read(path))
				if !reflect.DeepEqual(result.Prepared, review) || result.Prepares != 1 || result.Commits != 1 || result.Drops != 1 || result.Checks != 1 || result.Admitted.Admission != "enqueued" || result.Recovered.Admission != "enqueued" {
					t.Fatal("browser did not recover one original lost commit without re-admission")
				}
				immutable(result.Admitted)
				immutable(result.Recovered)
				wait(func() bool { return status().DeliveryOutcome == "confirmed" }, "actual browser text receiver save did not arrive")
				assertSaved()
			}
			if err := os.WriteFile(filepath.Join(private, "text-browser-"+stage+".ok"), []byte("verified\n"), 0600); err != nil {
				t.Fatal("private text browser checkpoint acknowledgement unavailable")
			}
		}
	})
	result := catalogProcessDecode[struct {
		Schema         int      `json:"schema"`
		Prepares       int      `json:"prepares"`
		Commits        int      `json:"commits"`
		Drops          int      `json:"drops"`
		RecoveryChecks int      `json:"recoveryChecks"`
		DetailChecks   int      `json:"detailChecks"`
		Widths         []int    `json:"widths"`
		Locales        []string `json:"locales"`
	}](t, read(filepath.Join(private, "text-browser-result.json")))
	if result.Schema != 1 || result.Prepares != 1 || result.Commits != 1 || result.Drops != 1 || result.RecoveryChecks != 1 || result.DetailChecks != 1 || !reflect.DeepEqual(result.Widths, []int{375, 320}) || !reflect.DeepEqual(result.Locales, []string{"en", "ja"}) {
		t.Fatal("text browser final fixed evidence invalid")
	}
	assertSaved()
	for _, p := range peers {
		p.stop(t)
	}
	if path := os.Getenv("SOBA_TEXT_BROWSER_SUMMARY"); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("sanitized text browser summary unavailable")
		}
		_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"selectedRecipients\":1,\"explicitQueueMode\":true,\"exactReview\":true,\"explicitConsent\":true,\"prepareCalls\":1,\"commitCalls\":1,\"commitResponseLost\":true,\"originalStatusRecovered\":true,\"receiverSavedRows\":1,\"untouchedPeerRows\":0,\"receiptMatched\":true,\"locales\":[\"en\",\"ja\"],\"widths\":[375,320],\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("sanitized text browser summary save failed")
		}
	}
}
