//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type browserOutboxRecord struct {
	Version         int              `json:"version"`
	Kind            string           `json:"kind"`
	OutboxID        string           `json:"outboxId"`
	RequestID       string           `json:"requestId"`
	ProcessID       string           `json:"processId"`
	ReviewRevision  string           `json:"reviewRevision"`
	Admission       string           `json:"admission"`
	Status          string           `json:"status"`
	DeliveryOutcome string           `json:"deliveryOutcome"`
	PeerIDs         []string         `json:"peerIds"`
	Entries         []transfer.Entry `json:"entries"`
	ExpiresAt       time.Time        `json:"expiresAt"`
	Children        []struct {
		PeerID          string `json:"peerId"`
		DeliveryID      string `json:"deliveryId"`
		DeliveryOutcome string `json:"deliveryOutcome"`
		Files           []struct {
			BatchID string `json:"batchId"`
			FileID  string `json:"fileId"`
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
		} `json:"files"`
	} `json:"children"`
}

type browserOutboxSnapshot struct {
	ProcessID string `json:"resourceCatalogProcessId"`
	Peers     []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	} `json:"peers"`
	Transfers []dispatchProcessTransfer `json:"transfers"`
}

// Reuse real paired Core processes and the existing authenticated browser
// fixture. The only substituted event is a lost successful admission response.
func TestOutboxThreeProcessBrowser(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_TRANSFER_BROWSER_NATIVE") != "1" || os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unperformed: explicit native process/browser acceptance required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs, peers, keys := startDispatchProcessPeers(t, ctx)
	snapshot := func(p *routeCoreProcess) browserOutboxSnapshot {
		return catalogProcessDecode[browserOutboxSnapshot](t, p.invoke(t, "fixture.snapshot", struct{}{}))
	}
	inventory := snapshot(peers[0])
	recipients := []map[string]string{}
	for _, key := range keys[1:] {
		for _, peer := range inventory.Peers {
			if peer.ID == key {
				recipients = append(recipients, map[string]string{"id": peer.ID, "name": peer.Name})
			}
		}
	}
	if len(recipients) != 2 {
		t.Fatal("actual outbox recipient inventory unavailable")
	}
	// Stop real receivers before reviewing the opt-in queued send.
	for i := 1; i < len(peers); i++ {
		peers[i].stop(t)
	}
	for _, key := range keys[1:] {
		_ = peers[0].invokeResult(t, "discovery.refresh", map[string]string{"peerId": key})
	}
	until := time.Now().Add(20 * time.Second)
	for {
		offline := 0
		for _, peer := range snapshot(peers[0]).Peers {
			if (peer.ID == keys[1] || peer.ID == keys[2]) && !peer.Online {
				offline++
			}
		}
		if offline == 2 {
			break
		}
		if time.Now().After(until) || ctx.Err() != nil {
			t.Fatal("actual stopped recipients did not project offline; no synthetic state substituted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	viewer := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "fixture.web", struct{}{}))
	var admission browserOutboxRecord
	saved := map[string]string{}
	private := runTransferProcessBrowser(t, ctx, map[string]any{"schema": 1, "outbox": true, "viewer": viewer, "peerId": keys[1], "peerName": recipients[0]["name"], "recipients": recipients, "processId": inventory.ProcessID}, false, func(private string) {
		marker := filepath.Join(private, "outbox-browser-admitted.json")
		until := time.Now().Add(35 * time.Second)
		for {
			raw, err := os.ReadFile(marker)
			if err == nil {
				result := catalogProcessDecode[struct {
					Prepared browserOutboxRecord `json:"prepared"`
					Admitted browserOutboxRecord `json:"admitted"`
				}](t, raw)
				review := result.Prepared
				admission = result.Admitted
				if review.Admission != "prepared" || review.ProcessID != inventory.ProcessID || admission.Admission != "enqueued" || admission.OutboxID != review.OutboxID || admission.RequestID != review.RequestID || admission.ReviewRevision != review.ReviewRevision || !admission.ExpiresAt.Equal(review.ExpiresAt) || !reflect.DeepEqual(admission.PeerIDs, keys[1:]) || len(admission.Children) != 2 || len(admission.Entries) != 1 {
					t.Fatal("browser admission changed the reviewed intent")
				}
				for i, child := range admission.Children {
					if child.DeliveryID != review.Children[i].DeliveryID || child.PeerID != keys[i+1] {
						t.Fatal("browser admission changed exact child identities")
					}
				}
				break
			}
			if !os.IsNotExist(err) || time.Now().After(until) || ctx.Err() != nil {
				t.Fatal("actual browser admission bridge did not arrive")
			}
			time.Sleep(20 * time.Millisecond)
		}
		for i := 1; i < len(peers); i++ {
			peers[i] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[i], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
		}
		for i, child := range admission.Children {
			until := time.Now().Add(20 * time.Second)
			for {
				ready := false
				for _, row := range snapshot(peers[i+1]).Transfers {
					if row.ID == child.DeliveryID && row.Status == "awaiting-acceptance" {
						ready = true
					}
				}
				if ready {
					break
				}
				if time.Now().After(until) || ctx.Err() != nil {
					t.Fatal("real durable receiver offer unavailable through normal acceptance seam")
				}
				time.Sleep(20 * time.Millisecond)
			}
			parent := t.TempDir()
			accepted := catalogProcessDecode[transfer.Batch](t, peers[i+1].invoke(t, "transfer.accept", map[string]any{"transferId": child.DeliveryID, "destination": parent}))
			if accepted.ID != child.DeliveryID || accepted.Peer.ID != keys[0] {
				t.Fatal("receiver accepted a different durable delivery")
			}
			saved[child.PeerID] = parent
		}
	})
	if _, err := os.Stat(filepath.Join(private, "outbox-browser-result.json")); err != nil {
		t.Fatal("browser did not observe final durable saved evidence")
	}
	final := catalogProcessDecode[browserOutboxRecord](t, peers[0].invoke(t, "outbox.status", map[string]string{"requestId": admission.RequestID}))
	if final.Status != "completed" || final.DeliveryOutcome != "confirmed" || final.OutboxID != admission.OutboxID || len(final.Children) != 2 {
		t.Fatal("real receiver evidence did not complete original outbox")
	}
	content := []byte("synthetic-browser-outbox-content")
	digest := sha256.Sum256(content)
	for i, child := range final.Children {
		if child.DeliveryID != admission.Children[i].DeliveryID || child.DeliveryOutcome != "confirmed" || len(child.Files) != 1 || child.Files[0].BatchID != child.DeliveryID || child.Files[0].SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("receiver receipt did not bind original bytes and delivery")
		}
		completed := catalogProcessDecode[transfer.Batch](t, peers[i+1].invoke(t, "fixture.transfer", map[string]string{"transferId": child.DeliveryID}))
		if completed.ID != child.DeliveryID || completed.State != transfer.Completed || completed.Peer.ID != keys[0] || filepath.Dir(completed.Destination) != saved[child.PeerID] {
			t.Fatal("completed receiver destination was not bound to original delivery")
		}
		roots, err := os.ReadDir(saved[child.PeerID])
		if err != nil || len(roots) != 1 || !roots[0].IsDir() || roots[0].Name() != filepath.Base(completed.Destination) {
			t.Fatal("receiver created missing or duplicate batch roots")
		}
		if admission.Entries[0].Path != "browser-outbox.txt" {
			t.Fatal("browser output changed the selected file name")
		}
		output := assertOutboxSavedInventory(t, completed.Destination, admission.Entries[0])
		data, err := os.ReadFile(output)
		if err != nil || string(data) != string(content) {
			t.Fatal("actual saved content mismatch")
		}
		if len(snapshot(peers[i+1]).Transfers) != 1 {
			t.Fatal("browser retry or reload created duplicate receiver batches")
		}
	}
	for _, p := range peers {
		p.stop(t)
	}
	if path := os.Getenv("SOBA_OUTBOX_BROWSER_SUMMARY"); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("sanitized outbox summary unavailable")
		}
		_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"selectedRecipients\":2,\"offlineEnqueue\":true,\"commitResponseLost\":true,\"statusRecovered\":true,\"browserReloaded\":true,\"widths\":[375,320],\"savedCopies\":2,\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("sanitized outbox summary save failed")
		}
	}
}
