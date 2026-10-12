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

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// The actual browser selects and uploads to two real receiving processes. Only
// the successful commit response is lost; Core replies and file bytes are real.
func TestTransferDispatchThreeProcessBrowser(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_TRANSFER_BROWSER_NATIVE") != "1" || os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unperformed: explicit process/browser acceptance required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs, peers, keys := startDispatchProcessPeers(t, ctx)
	snapshot := func(p *routeCoreProcess) dispatchProcessSnapshot {
		return catalogProcessDecode[dispatchProcessSnapshot](t, p.invoke(t, "fixture.snapshot", struct{}{}))
	}
	inventory := catalogProcessDecode[struct {
		Peers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"peers"`
	}](t, peers[0].invoke(t, "fixture.snapshot", struct{}{}))
	recipients := []map[string]string{}
	for _, key := range keys[1:] {
		for _, peer := range inventory.Peers {
			if peer.ID == key {
				recipients = append(recipients, map[string]string{"id": key, "name": peer.Name})
			}
		}
	}
	if len(recipients) != 2 || recipients[0]["name"] == "" || recipients[1]["name"] == "" || recipients[0]["name"] == recipients[1]["name"] {
		t.Fatal("distinct actual dispatch recipient names unavailable")
	}
	processID := snapshot(peers[0]).ProcessID
	viewer := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "fixture.web", struct{}{}))
	private := runTransferProcessBrowser(t, ctx, map[string]any{
		"schema": 1, "dispatch": true, "viewer": viewer, "peerId": keys[1], "peerName": recipients[0]["name"], "recipients": recipients, "processId": processID,
	}, true)
	raw, err := os.ReadFile(filepath.Join(private, "dispatch-browser-result.json"))
	if err != nil {
		t.Fatal("private dispatch browser receipt unavailable")
	}
	result := catalogProcessDecode[struct {
		Prepared  TransferDispatchReview  `json:"prepared"`
		Committed TransferDispatchReceipt `json:"committed"`
	}](t, raw)
	review, receipt := result.Prepared, result.Committed
	if review.Status != "prepared" || review.ProcessID != processID || !reflect.DeepEqual(review.PeerIDs, keys[1:]) || len(review.Entries) != 1 || review.Entries[0].Path != "browser-dispatch.txt" {
		t.Fatal("browser preparation did not bind exact selected recipients and file")
	}
	if receipt.Status != "committed" || receipt.ProcessID != processID || receipt.PreparationID != review.PreparationID || receipt.DispatchID != review.PreparationID || len(receipt.Children) != 2 || receipt.Children[0].TransferID == receipt.Children[1].TransferID {
		t.Fatal("browser recovery did not retain committed dispatch identity")
	}
	status := map[string]any{"processId": processID, "preparationId": receipt.PreparationID}
	if got := catalogProcessDecode[TransferDispatchReceipt](t, peers[0].invoke(t, "transfer.dispatch.status", status)); !reflect.DeepEqual(got, receipt) {
		t.Fatal("browser recovered receipt differed from retained Core receipt")
	}
	wait := func(p *routeCoreProcess, id, state string) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for {
			for _, row := range snapshot(p).Transfers {
				if row.ID == id && row.Status == state {
					return
				}
				if row.ID == id && row.Status == "failed" {
					t.Fatal("actual browser dispatch delivery failed")
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("actual browser dispatch delivery state did not arrive")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for i, child := range receipt.Children {
		if child.PeerID != keys[i+1] || child.TransferID == "" {
			t.Fatal("browser selected recipient changed at commit")
		}
		wait(peers[i+1], child.TransferID, "awaiting-acceptance")
		accepted := catalogProcessDecode[transfer.Batch](t, peers[i+1].invoke(t, "transfer.accept", map[string]any{"transferId": child.TransferID, "destination": t.TempDir()}))
		wait(peers[0], child.TransferID, "completed")
		wait(peers[i+1], child.TransferID, "completed")
		files, err := os.ReadDir(accepted.Destination)
		if err != nil || len(files) != 1 || files[0].Name() != "browser-dispatch.txt" {
			t.Fatal("browser recovery produced missing or duplicate saved files")
		}
		data, err := os.ReadFile(filepath.Join(accepted.Destination, files[0].Name()))
		if err != nil || string(data) != "synthetic-browser-dispatch-content" {
			t.Fatal("browser upload changed real saved bytes")
		}
		if len(snapshot(peers[i+1]).Transfers) != 1 {
			t.Fatal("browser recovery produced duplicate recipient transfers")
		}
	}
	if len(snapshot(peers[0]).Transfers) != 2 {
		t.Fatal("browser recovery created extra outgoing children")
	}
	if spools, err := os.ReadDir(filepath.Join(dirs[0], "outgoing")); err != nil || len(spools) != 0 {
		t.Fatal("browser dispatch final owner retained staged content")
	}
	for _, p := range peers {
		p.stop(t)
	}
	if path := os.Getenv("SOBA_DISPATCH_BROWSER_SUMMARY"); path != "" {
		// Fixed fields only. Identity-bearing receipts remain in private TempDir.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("sanitized dispatch browser summary unavailable")
		}
		_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"selectedRecipients\":2,\"commitResponseLost\":true,\"statusRecovered\":true,\"savedCopies\":2,\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("sanitized dispatch browser summary save failed")
		}
	}
}
