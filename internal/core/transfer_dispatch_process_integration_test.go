//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type dispatchProcessTransfer struct {
	ID             string `json:"id"`
	PeerID         string `json:"peerId"`
	DispatchID     string `json:"dispatchId"`
	Direction      string `json:"direction"`
	Status         string `json:"status"`
	Retryable      bool   `json:"retryable"`
	CompletedBytes int64  `json:"completedBytes"`
}

// Keep diagnostic-only fields outside the original assertion DTO so adding
// observations cannot widen the successful-sibling equality assertion.
type dispatchProcessTransferObservation struct {
	dispatchProcessTransfer
	RetryBlockedReason string `json:"retryBlockedReason"`
	TotalBytes         int64  `json:"totalBytes"`
}

type dispatchProcessSnapshot struct {
	ProcessID string                               `json:"resourceCatalogProcessId"`
	Transfers []dispatchProcessTransferObservation `json:"transfers"`
}

// Three independent Core processes use unchanged native peer HTTP and real
// files. Only synthetic source damage creates the partial failure; no response,
// saved byte, receipt or transfer progress is substituted. This is loopback
// process acceptance, not browser, enrollment or physical-device evidence.
func TestTransferDispatchThreeProcessPartialRetry(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native loopback execution required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs, peers, keys := startDispatchProcessPeers(t, ctx)
	var diagnostics dispatchProcessDiagnostic
	snapshot := func(p *routeCoreProcess) dispatchProcessSnapshot {
		value := catalogProcessDecode[dispatchProcessSnapshot](t, p.invoke(t, "fixture.snapshot", struct{}{}))
		for i, peer := range peers {
			if p == peer {
				diagnostics.capture(i, value)
			}
		}
		return value
	}
	wait := func(p *routeCoreProcess, id, status string) dispatchProcessTransfer {
		deadline := time.Now().Add(25 * time.Second)
		var observations dispatchDiagnosticObservations
		logFailure := func() {
			for i, peer := range peers {
				if p == peer {
					for _, line := range diagnostics.failureLines(i, id, status, observations) {
						t.Log(line)
					}
				}
			}
		}
		for {
			current := snapshot(p)
			observations.observe(current.Transfers, id)
			for _, row := range current.Transfers {
				if row.ID != id {
					continue
				}
				// Failed is visible before physical owner join; retry is admitted
				// only when its retryable projection also becomes true.
				if row.Status == status && (status != "failed" || row.Retryable) {
					return row.dispatchProcessTransfer
				}
				if row.Status == "failed" && status != "failed" {
					logFailure()
					t.Fatal("actual dispatch failed before expected state", status)
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				logFailure()
				t.Fatal("actual dispatch state did not arrive", status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	source := t.TempDir()
	paths := []string{filepath.Join(source, "first.txt"), filepath.Join(source, "second.txt")}
	original := []byte("synthetic-dispatch-original")
	for _, path := range paths {
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal("synthetic source save failed")
		}
	}
	process := snapshot(peers[0]).ProcessID
	review := catalogProcessDecode[TransferDispatchReview](t, peers[0].invoke(t, "transfer.dispatch.prepare", map[string]any{"processId": process, "peerIds": keys[1:], "paths": paths}))
	if review.Version != 1 || review.ProcessID != process || review.PreparationID == "" || review.ReviewRevision == "" || review.Status != "prepared" || review.TotalBytes != 2*int64(len(original)) || !reflect.DeepEqual(review.PeerIDs, keys[1:]) || len(review.Entries) != 2 {
		t.Fatal("actual dispatch review contract mismatch")
	}
	for _, p := range peers {
		if len(snapshot(p).Transfers) != 0 {
			t.Fatal("preparation created a child or remote offer before commit")
		}
	}
	statusPayload := map[string]any{"processId": process, "preparationId": review.PreparationID}
	queried := catalogProcessDecode[TransferDispatchReview](t, peers[0].invoke(t, "transfer.dispatch.status", statusPayload))
	if !reflect.DeepEqual(review, queried) {
		t.Fatal("prepared status changed the reviewed selection")
	}
	spools, err := os.ReadDir(filepath.Join(dirs[0], "outgoing"))
	if err != nil || len(spools) != 1 || !spools[0].IsDir() {
		t.Fatal("dispatch did not stage exactly one owned payload")
	}
	spool := filepath.Join(dirs[0], "outgoing", spools[0].Name())
	var missing string
	hash := sha256.Sum256(original)
	for _, entry := range review.Entries {
		if entry.SHA256 != hex.EncodeToString(hash[:]) {
			t.Fatal("review did not bind staged content digest")
		}
		if entry.Path == "second.txt" {
			missing = filepath.Join(spool, entry.ID)
		}
	}
	if missing == "" {
		t.Fatal("reviewed second staged entry missing")
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("synthetic-source-was-changed"), 0600); err != nil {
			t.Fatal("synthetic source mutation failed")
		}
	}
	commitPayload := map[string]any{"processId": process, "preparationId": review.PreparationID, "reviewRevision": review.ReviewRevision, "confirm": true}
	receipt := catalogProcessDecode[TransferDispatchReceipt](t, peers[0].invoke(t, "transfer.dispatch.commit", commitPayload))
	if receipt.Version != 1 || receipt.ProcessID != process || receipt.PreparationID != review.PreparationID || receipt.DispatchID != review.PreparationID || receipt.Status != "committed" || len(receipt.Children) != 2 || receipt.Children[0].TransferID == "" || receipt.Children[1].TransferID == "" || receipt.Children[0].TransferID == receipt.Children[1].TransferID {
		t.Fatal("actual dispatch receipt contract mismatch")
	}
	diagnostics.childIDs = [2]string{receipt.Children[0].TransferID, receipt.Children[1].TransferID}
	diagnostics.phase = dispatchDiagnosticOffers
	checkReceipt := func() {
		t.Helper()
		for _, command := range []string{"transfer.dispatch.status", "transfer.dispatch.commit"} {
			payload := statusPayload
			if command == "transfer.dispatch.commit" {
				payload = commitPayload
			}
			repeated := catalogProcessDecode[TransferDispatchReceipt](t, peers[0].invoke(t, command, payload))
			if !reflect.DeepEqual(receipt, repeated) || len(snapshot(peers[0]).Transfers) != 2 {
				t.Fatal("repeated receipt changed identities or created more children")
			}
		}
	}
	checkReceipt()
	for i, child := range receipt.Children {
		if child.PeerID != keys[i+1] {
			t.Fatal("committed child recipient differed from exact ordered review")
		}
		row := wait(peers[0], child.TransferID, "awaiting-acceptance")
		if row.PeerID != child.PeerID || row.DispatchID != receipt.DispatchID || row.Direction != "outgoing" {
			t.Fatal("child snapshot lost dispatch identity")
		}
		wait(peers[i+1], child.TransferID, "awaiting-acceptance")
		if len(snapshot(peers[i+1]).Transfers) != 1 {
			t.Fatal("recipient received an unselected child")
		}
	}
	firstID, failedID := receipt.Children[0].TransferID, receipt.Children[1].TransferID
	accepted := catalogProcessDecode[transfer.Batch](t, peers[1].invoke(t, "transfer.accept", map[string]any{"transferId": firstID, "destination": t.TempDir()}))
	diagnostics.phase = dispatchDiagnosticFirstAccepted
	diagnostics.accept(0, accepted)
	firstCompleted := wait(peers[0], firstID, "completed")
	wait(peers[1], firstID, "completed")
	readOriginal := func(path string) os.FileInfo {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || string(data) != string(original) {
			t.Fatal("actual received bytes differed from immutable prepared content")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal("actual received file metadata unavailable")
		}
		return info
	}
	firstPaths := []string{filepath.Join(accepted.Destination, "first.txt"), filepath.Join(accepted.Destination, "second.txt")}
	firstInfo := []os.FileInfo{readOriginal(firstPaths[0]), readOriginal(firstPaths[1])}
	diagnostics.phase = dispatchDiagnosticFirstSaved
	if _, err := os.Stat(spool); err != nil {
		t.Fatal("successful sibling prematurely removed shared payload")
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal("synthetic staged entry damage failed")
	}
	diagnostics.phase = dispatchDiagnosticStagedEntryRemoved
	partial := catalogProcessDecode[transfer.Batch](t, peers[2].invoke(t, "transfer.accept", map[string]any{"transferId": failedID, "destination": t.TempDir()}))
	diagnostics.phase = dispatchDiagnosticSecondAccepted
	diagnostics.accept(1, partial)
	failed := wait(peers[0], failedID, "failed")
	if failed.CompletedBytes != int64(len(original)) || firstCompleted.CompletedBytes != 2*int64(len(original)) {
		t.Fatal("partial success byte counts were not independent")
	}
	partialFirst := filepath.Join(partial.Destination, "first.txt")
	partialInfo := readOriginal(partialFirst)
	if _, err := os.Stat(spool); err != nil {
		t.Fatal("retryable failed child lost shared payload ownership")
	}
	if err := os.WriteFile(missing, original, 0600); err != nil {
		t.Fatal("synthetic staged entry restore failed")
	}
	expected := transferRetryExpectation{ProcessID: process, PeerID: keys[2], Direction: "outgoing"}
	peers[0].invoke(t, "transfer.retry", map[string]any{"transferId": failedID, "expected": expected})
	diagnostics.phase = dispatchDiagnosticRetryInvoked
	completed := wait(peers[0], failedID, "completed")
	wait(peers[2], failedID, "completed")
	if completed.Retryable || completed.CompletedBytes != 2*int64(len(original)) {
		t.Fatal("selected retry did not finish exact failed child")
	}
	diagnostics.phase = dispatchDiagnosticCompleted
	for i, path := range firstPaths {
		now := readOriginal(path)
		if !os.SameFile(firstInfo[i], now) || firstInfo[i].ModTime() != now.ModTime() {
			t.Fatal("failed-child retry replaced a completed recipient's file")
		}
	}
	partialAgain := readOriginal(partialFirst)
	if !os.SameFile(partialInfo, partialAgain) || partialInfo.ModTime() != partialAgain.ModTime() {
		t.Fatal("failed-child retry replaced a previously saved file")
	}
	readOriginal(filepath.Join(partial.Destination, "second.txt"))
	if now := wait(peers[0], firstID, "completed"); !reflect.DeepEqual(now, firstCompleted) {
		t.Fatal("failed-child retry changed successful sibling state")
	}
	for _, p := range peers[1:] {
		if len(snapshot(p).Transfers) != 1 {
			t.Fatal("retry created a new recipient batch")
		}
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("final child completion retained staged payload")
	}
	checkReceipt()
	expected.PeerID = keys[1]
	if peers[0].invokeResult(t, "transfer.retry", map[string]any{"transferId": firstID, "expected": expected}).Error == "" {
		t.Fatal("completed sibling accepted a retry")
	}
	if peers[0].invokeResult(t, "transfer.dispatch.discard", statusPayload).Error == "" {
		t.Fatal("committed dispatch accepted a discard")
	}
	for _, p := range peers {
		p.stop(t)
	}
}

// Reuse the same real pairing, trust and authenticated discovery for browser acceptance.
func startDispatchProcessPeers(t *testing.T, ctx context.Context) (dirs [3]string, peers [3]*routeCoreProcess, keys [3]string) {
	t.Helper()
	dirs = [3]string{t.TempDir(), t.TempDir(), t.TempDir()}
	for i := range peers {
		peers[i] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[i], true, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
		reservation, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal("synthetic dispatch endpoint reservation failed")
		}
		listen := reservation.Endpoint().String()
		if err := reservation.Close(); err != nil {
			t.Fatal("synthetic dispatch endpoint release failed")
		}
		peers[i].invoke(t, "network.configure", map[string]any{"mode": "direct-lan", "hostname": "synthetic-dispatch-" + strconv.Itoa(i), "directLAN": DirectLANSelection{Listen: listen, Prefixes: []string{"127.0.0.0/8"}}})
		keys[i] = catalogProcessDecode[map[string]string](t, peers[i].invoke(t, "direct-lan.identity", struct{}{}))["publicKey"]
	}
	for i := 1; i < len(peers); i++ {
		invite := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "direct-lan.invite", map[string]any{"recipientPublicKey": keys[i], "name": "synthetic-dispatch-peer-" + strconv.Itoa(i), "ttlSeconds": 120}))
		peers[i].invoke(t, "direct-lan.join", map[string]any{"invitation": invite["invitation"]})
		peers[0].invoke(t, "peer.trust", map[string]any{"peerId": keys[i], "trusted": true})
		peers[i].invoke(t, "peer.trust", map[string]any{"peerId": keys[0], "trusted": true})
	}
	for i := 1; i < len(peers); i++ {
		for _, direction := range [][2]int{{0, i}, {i, 0}} {
			deadline := time.Now().Add(30 * time.Second)
			for {
				probe := catalogProcessDecode[map[string]any](t, peers[direction[0]].invoke(t, "fixture.probe", map[string]any{"Peer": keys[direction[1]], "Port": PeerPort}))
				if probe["connected"] == true && probe["networkReady"] == true {
					break
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					t.Fatal("actual dispatch peer transport did not become ready")
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		// A bare TCP connection is transport readiness, not authenticated
		// application availability. Use the same explicit discovery action
		// that supplies current recipient availability to the product UI.
		discovery := catalogProcessDecode[struct {
			Observations []struct {
				PeerID string `json:"peerId"`
				State  string `json:"state"`
			} `json:"observations"`
		}](t, peers[0].invoke(t, "discovery.refresh", map[string]string{"peerId": keys[i]}))
		if len(discovery.Observations) != 1 || discovery.Observations[0].PeerID != keys[i] || discovery.Observations[0].State != "confirmed" {
			t.Fatal("authenticated recipient availability was not confirmed")
		}
	}
	return
}
