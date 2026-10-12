//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Sender and receiver reopen their real profile and ledger after graceful
// process stops. Every offer, acceptance and saved receipt uses the production
// durable peer protocol; the reused fixture's third process stays idle.
func TestOutboxProcessRestart(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native loopback execution required")
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
	oldProcess := snapshot(peers[0]).ProcessID
	peers[1].stop(t)
	_ = peers[0].invokeResult(t, "discovery.refresh", map[string]string{"peerId": keys[1]})
	source := t.TempDir()
	file := filepath.Join(source, "sample.txt")
	empty := filepath.Join(source, "only-empty")
	if os.WriteFile(file, []byte("synthetic-original-outbox-content"), 0600) != nil || os.Mkdir(empty, 0700) != nil {
		t.Fatal("synthetic outbox inputs unavailable")
	}
	reviews := []browserOutboxRecord{}
	for _, path := range []string{file, empty} {
		review := catalogProcessDecode[browserOutboxRecord](t, peers[0].invoke(t, "outbox.prepare", map[string]any{"processId": oldProcess, "peerIds": []string{keys[1]}, "paths": []string{path}, "expiresAt": time.Now().UTC().Add(time.Hour)}))
		if review.Admission != "prepared" || review.Status != "prepared" || review.RequestID != review.OutboxID || len(review.Children) != 1 || len(review.Entries) != 1 {
			t.Fatal("native outbox preparation invalid")
		}
		record := catalogProcessDecode[browserOutboxRecord](t, peers[0].invoke(t, "outbox.commit", map[string]any{"processId": oldProcess, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true}))
		if record.Admission != "enqueued" || record.Children[0].DeliveryID != review.Children[0].DeliveryID || record.DeliveryOutcome != "not_attempted" {
			t.Fatal("native outbox commit changed review or fabricated delivery")
		}
		reviews = append(reviews, review)
	}
	if reviews[1].Entries[0].Kind != transfer.Directory || reviews[1].Entries[0].Size != 0 {
		t.Fatal("directory-only request did not retain its explicit entry")
	}
	// The original input no longer contains the approved bytes. Recovery must
	// use its owned staged copy, never reopen this path as fresh send authority.
	if os.WriteFile(file, []byte("synthetic-source-changed"), 0600) != nil {
		t.Fatal("synthetic source mutation unavailable")
	}
	peers[0].stop(t)
	peers[0] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[0], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	if snapshot(peers[0]).ProcessID == oldProcess {
		t.Fatal("sender process generation did not change")
	}
	stable := func(review browserOutboxRecord) browserOutboxRecord {
		t.Helper()
		status := catalogProcessDecode[browserOutboxRecord](t, peers[0].invoke(t, "outbox.status", map[string]string{"requestId": review.RequestID}))
		payload := map[string]any{"processId": oldProcess, "outboxId": review.OutboxID, "reviewRevision": review.ReviewRevision, "confirm": true}
		reply, code, err := retryOutboxProcessCommit(ctx, func() (json.RawMessage, string) {
			reply := peers[0].invokeResult(t, "outbox.commit", payload)
			return reply.Value, reply.Error
		})
		if err != nil || code != "" {
			t.Fatalf("original outbox commit replay failed: retry=%v command=%q; runtime diagnostics:\n%s", err, code, peers[0].diagnostics.safeSummary())
		}
		repeated := catalogProcessDecode[browserOutboxRecord](t, reply)
		for _, record := range []browserOutboxRecord{status, repeated} {
			if record.Admission != "enqueued" || record.OutboxID != review.OutboxID || record.RequestID != review.RequestID || record.ReviewRevision != review.ReviewRevision || !record.ExpiresAt.Equal(review.ExpiresAt) || !reflect.DeepEqual(record.PeerIDs, review.PeerIDs) || !reflect.DeepEqual(record.Entries, review.Entries) || len(record.Children) != 1 || record.Children[0].DeliveryID != review.Children[0].DeliveryID {
				t.Fatal("restart/repeated commands changed original immutable delivery")
			}
		}
		return repeated
	}
	for _, review := range reviews {
		if stable(review).DeliveryOutcome != "not_attempted" {
			t.Fatal("offline receiver gained unobserved delivery evidence")
		}
	}
	peers[1] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[1], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	destinations := map[string]string{}
	for _, review := range reviews {
		until := time.Now().Add(25 * time.Second)
		for {
			ready := false
			for _, row := range snapshot(peers[1]).Transfers {
				if row.ID == review.Children[0].DeliveryID && row.Status == "awaiting-acceptance" {
					ready = true
				}
			}
			if ready {
				break
			}
			if time.Now().After(until) || ctx.Err() != nil {
				logOutboxResumeDiagnostics(t, peers[0], keys[1], review.RequestID, review.Children[0].DeliveryID, "sender")
				logOutboxResumeDiagnostics(t, peers[1], keys[0], review.RequestID, review.Children[0].DeliveryID, "receiver")
				t.Fatal("durable offer did not resume after sender restart")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Stop the real sender after offer/lease negotiation but before acceptance.
	// No sender can issue /start while we verify acceptance has zero filesystem
	// publication, including the directory-only batch. Legacy eager acceptance
	// would create a batch root and fails this nonvacuous check.
	peers[0].stop(t)
	for _, review := range reviews {
		parent := t.TempDir()
		accepted := catalogProcessDecode[transfer.Batch](t, peers[1].invoke(t, "transfer.accept", map[string]any{"transferId": review.Children[0].DeliveryID, "destination": parent}))
		if accepted.ID != review.Children[0].DeliveryID || accepted.State != transfer.Accepted || accepted.Destination != parent {
			t.Fatal("durable acceptance published or retargeted before lease start")
		}
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 0 {
			t.Fatal("receiver published output before authenticated lease start")
		}
		destinations[review.OutboxID] = parent
	}
	peers[0] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[0], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	for _, review := range reviews {
		until := time.Now().Add(25 * time.Second)
		for {
			record := catalogProcessDecode[browserOutboxRecord](t, peers[0].invoke(t, "outbox.status", map[string]string{"requestId": review.RequestID}))
			if record.DeliveryOutcome == "confirmed" {
				break
			}
			if time.Now().After(until) || ctx.Err() != nil {
				t.Fatal("durable receiver saved evidence did not arrive")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// A receiver restart after saved evidence must retain the same delivery and
	// must not reconstruct a new ordinary transfer or another saved output.
	peers[1].stop(t)
	peers[1] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[1], false, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	for i, review := range reviews {
		record := stable(review)
		if record.Status != "completed" || record.DeliveryOutcome != "confirmed" || len(record.Children[0].Files) != 1 {
			t.Fatal("both-side restart lost durable saved evidence")
		}
		receipt := record.Children[0].Files[0]
		entry := review.Entries[0]
		if receipt.BatchID != review.Children[0].DeliveryID || receipt.FileID != entry.ID || receipt.Size != entry.Size || receipt.SHA256 != entry.SHA256 {
			t.Fatal("durable receipt did not cover exact original entry")
		}
		completed := catalogProcessDecode[transfer.Batch](t, peers[1].invoke(t, "fixture.transfer", map[string]string{"transferId": review.Children[0].DeliveryID}))
		if completed.ID != review.Children[0].DeliveryID || completed.State != transfer.Completed || completed.Peer.ID != keys[0] || filepath.Dir(completed.Destination) != destinations[review.OutboxID] {
			t.Fatal("reopened receiver lost original completed destination")
		}
		roots, err := os.ReadDir(destinations[review.OutboxID])
		if err != nil || len(roots) != 1 || !roots[0].IsDir() || roots[0].Name() != filepath.Base(completed.Destination) {
			t.Fatal("restart created missing or duplicate batch roots")
		}
		output := assertOutboxSavedInventory(t, completed.Destination, entry)
		if i == 0 {
			data, err := os.ReadFile(output)
			if err != nil || string(data) != "synthetic-original-outbox-content" {
				t.Fatal("sender restart did not use approved staged bytes")
			}
		} else {
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 0 {
				t.Fatal("directory-only durable output was not saved exactly once")
			}
		}
	}
	restored := snapshot(peers[1]).Transfers
	if len(restored) != 2 {
		t.Fatal("receiver restart changed durable incoming batch count")
	}
	for _, review := range reviews {
		found := false
		for _, row := range restored {
			if row.ID == review.Children[0].DeliveryID && row.Status == "completed" {
				found = true
			}
		}
		if !found {
			t.Fatal("receiver restart did not retain original completed delivery")
		}
	}
	for _, p := range peers {
		p.stop(t)
	}
}
