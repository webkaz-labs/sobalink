package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestAutosavePartialUpdatesPreserveIndependentPeerSettings(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	first, second := t.TempDir(), t.TempDir()
	mustCommand(t, p.b, "settings.update", map[string]any{"receiveDirectory": first})
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "paused": true})
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true})
	peer, _ := p.b.trust("peer-a")
	if !peer.Paused || !peer.Autosave || peer.Directory != first {
		t.Fatal("enabling autosave changed pause or missed the current default directory")
	}
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, fileManifest("paused-offer", "payload")); !errors.Is(err, transfer.ErrPeerPaused) {
		t.Fatal("partial autosave enable unpaused receiver permission")
	}
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "paused": false})
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "directory": second})
	peer, _ = p.b.trust("peer-a")
	policies := p.b.transfers.ReceivePolicies()
	if peer.Paused || !peer.Autosave || peer.Directory != second || len(policies) != 1 || policies[0].Destination != second {
		t.Fatal("directory-only update discarded autosave or changed the pause state")
	}
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": false})
	peer, _ = p.b.trust("peer-a")
	if peer.Autosave || peer.Directory != second || len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("disable discarded the reviewed directory or retained auto-accept")
	}
	mustCommand(t, p.b, "settings.update", map[string]any{"receiveDirectory": first})
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true})
	peer, _ = p.b.trust("peer-a")
	if peer.Directory != second {
		t.Fatal("reenable replaced an existing peer directory with the global default")
	}
	// Existing complete GUI payloads retain their explicit full-update meaning.
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": false, "paused": false, "directory": ""})
	peer, _ = p.b.trust("peer-a")
	if peer.Autosave || peer.Paused || peer.Directory != "" {
		t.Fatal("full legacy GUI payload no longer applied its explicit values")
	}
}

func TestConcurrentPartialAutosaveChangesMergeUnderCommandLock(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	first, second := t.TempDir(), t.TempDir()
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": first})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, patch := range []map[string]any{{"peerId": "peer-a", "paused": true}, {"peerId": "peer-a", "directory": second}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := command(p.b, randomID(), "peer.autosave", patch)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	peer, _ := p.b.trust("peer-a")
	if !peer.Paused || !peer.Autosave || peer.Directory != second {
		t.Fatal("an independent partial command overwrote a concurrent setting")
	}
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, fileManifest("concurrent-paused", "payload")); !errors.Is(err, transfer.ErrPeerPaused) {
		t.Fatal("merged pause was not enforced by the receiver")
	}
}

func TestAutosavePartialUpdatesFailWithoutPublishingUnsavedValues(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	before, _ := p.b.trust("peer-a")
	if _, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true}); networkErrorCode(err) != "autosave_directory_required" {
		t.Fatal("missing receive directory did not return the stable recovery code")
	}
	path := filepath.Join(p.b.dir, "sobalink.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]any{{"peerId": "peer-a", "paused": true}, {"peerId": "peer-a", "enabled": true, "paused": true, "directory": t.TempDir()}} {
		if _, err := command(p.b, randomID(), "peer.autosave", patch); err == nil || networkErrorCode(err) == "autosave_directory_required" {
			t.Fatal("failed profile save was successful or misclassified as a missing directory")
		}
		current, _ := p.b.trust("peer-a")
		if current != before || len(p.b.transfers.ReceivePolicies()) != 0 {
			t.Fatal("failed save published changed approval settings")
		}
	}
}

func TestExplicitAutosaveEnableRepairsFailClosedPolicyRemoval(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	path := filepath.Join(p.b.dir, "sobalink.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": false}); err == nil || len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("failed durable removal did not fail closed in memory")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true})
	if len(p.b.transfers.ReceivePolicies()) != 1 {
		t.Fatal("explicit re-enable could not repair the inactive runtime policy")
	}
}

func TestAutosaveCombinedPayloadKeepsProfileAndRuntimeAtomic(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": first})
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "paused": true, "directory": second})
	peer, _ := p.b.trust("peer-a")
	var saved Profile
	path := filepath.Join(p.b.dir, "sobalink.json")
	if err := config.ReadJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	if !peer.Paused || !peer.Autosave || peer.Directory != second || len(saved.Peers) != 1 || saved.Peers[0] != peer || p.b.transfers.ReceivePolicies()[0].Destination != second {
		t.Fatal("combined success did not commit the same directory, policy and pause")
	}
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, fileManifest("combined-paused", "payload")); !errors.Is(err, transfer.ErrPeerPaused) {
		t.Fatal("combined pause was not effective when autosave became visible")
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "paused": false, "directory": third}); err == nil {
		t.Fatal("combined update reported success after persistence failed")
	}
	current, _ := p.b.trust("peer-a")
	if current != peer || p.b.transfers.ReceivePolicies()[0].Destination != second {
		t.Fatal("failed combined update partially changed the live settings")
	}
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, fileManifest("failed-unpause", "payload")); !errors.Is(err, transfer.ErrPeerPaused) {
		t.Fatal("failed combined update unpaused the peer")
	}
}

func TestPauseAfterFailedAutosaveDisablePersistsReconciliationBeforeReopen(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	directory := t.TempDir()
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": directory})
	path := filepath.Join(p.b.dir, "sobalink.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": false}); err == nil {
		t.Fatal("disable unexpectedly persisted")
	}
	if len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("failed disable did not remove runtime auto-accept")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "paused": true})
	peer, _ := p.b.trust("peer-a")
	if peer.Autosave || !peer.Paused || peer.Directory != directory {
		t.Fatal("pause-only save resurrected an approval absent from the runtime policies")
	}
	if err := p.b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: p.b.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, _ := reopened.trust("peer-a")
	if restored != peer || len(reopened.transfers.ReceivePolicies()) != 0 {
		t.Fatal("reopen restored the failed autosave revocation")
	}
}
