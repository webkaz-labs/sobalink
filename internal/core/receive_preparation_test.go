package core

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// The child uses Core.Open's actual file-backed store. Only the free-space probe
// is replaced to stop precisely after the root exists but before staging.
func TestCoreReceivePreparationChild(t *testing.T) {
	dir := os.Getenv("SOBALINK_CORE_PREPARATION_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	route := os.Getenv("SOBALINK_CORE_PREPARATION_ROUTE")
	death := os.Getenv("SOBALINK_CORE_PREPARATION_DEATH") == "true"
	destination := filepath.Join(dir, "receive")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	diskspace.Process = diskspace.New(func(*os.File) (uint64, error) {
		calls++
		if calls == 2 {
			if death {
				os.Exit(84)
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 1 {
				t.Fatalf("root fixture %v %v", entries, err)
			}
			if err := os.WriteFile(filepath.Join(destination, entries[0].Name(), "unknown"), []byte("user data"), 0600); err != nil {
				t.Fatal(err)
			}
			return 0, nil
		}
		return 1 << 40, nil
	})
	c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
	profile := c.profileCopy()
	profile.Settings.Network = "tailnet"
	profile.Peers = append(profile.Peers, Trust{ID: peer.ID, Name: "fixture", Generation: peer.Generation, Network: profile.Settings.Network})
	if err := c.saveProfile(profile); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile = profile
	c.node = &pipeNode{hub: &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}, state: identity.State{Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: peer.ID}}}}}
	c.mu.Unlock()
	if err := c.transfers.BindPeer(peer); err != nil {
		t.Fatal(err)
	}
	if route == "default" || route == "default-autosave" {
		mustCommand(t, c, "settings.update", map[string]any{"receiveDirectory": destination})
	}
	if route == "peer-autosave" {
		mustCommand(t, c, "peer.autosave", map[string]any{"peerId": peer.ID, "enabled": true, "directory": destination})
	}
	if route == "default-autosave" {
		mustCommand(t, c, "peer.autosave", map[string]any{"peerId": peer.ID, "enabled": true})
	}
	manifest := coreCrashManifest("preparing", "12345678")
	_, err = c.transfers.Offer(peer, manifest)
	if route == "manual" || route == "default" {
		if err != nil {
			t.Fatal(err)
		}
		payload := map[string]any{"transferId": manifest.ID}
		if route == "manual" {
			payload["destination"] = destination
		}
		_, err = command(c, randomID(), "transfer.accept", payload)
	}
	if !errors.Is(err, transfer.ErrReceiveRecovery) {
		t.Fatal("denied cleanup missing", err)
	}
	if c.transfers.ReceiveRecovery().ReservedBytes != nil {
		t.Fatal("unknown preparation published zero")
	}
	if err := c.Close(); !errors.Is(err, transfer.ErrReceiveRecovery) {
		t.Fatal("ordinary Close lost pending cleanup", err)
	}
	os.Exit(85)
}

func TestCoreReceivePreparationReopenAllDestinations(t *testing.T) {
	for _, route := range []string{"manual", "default", "default-autosave", "peer-autosave"} {
		for _, death := range []string{"false", "true"} {
			t.Run(route+"/death="+death, func(t *testing.T) {
				dir := t.TempDir()
				cmd := exec.Command(os.Args[0], "-test.run=^TestCoreReceivePreparationChild$")
				cmd.Env = append(os.Environ(), "SOBALINK_CORE_PREPARATION_DIR="+dir, "SOBALINK_CORE_PREPARATION_ROUTE="+route, "SOBALINK_CORE_PREPARATION_DEATH="+death)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				want := 85
				if death == "true" {
					want = 84
				}
				if !errors.As(err, &exit) || exit.ExitCode() != want {
					t.Fatalf("Core child exit %v: %s", err, output)
				}
				store := transfer.FileReceiveAccountingStore{Path: filepath.Join(dir, "receive-accounting.json")}
				state, err := store.LoadReceiveAccounting()
				if err != nil || state.Preparation == nil {
					t.Fatalf("durable preparation %+v %v", state, err)
				}
				c, err := Open(context.Background(), Options{Directory: dir, SkipNetworkStart: true})
				if err != nil {
					t.Fatal("blocked receive stopped Core", err)
				}
				defer c.Close()
				if v := c.transfers.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
					t.Fatalf("Core reopen escaped intent: %+v", v)
				}
				if _, err := c.Snapshot(context.Background()); err != nil {
					t.Fatal("management unavailable", err)
				}
				mustCommand(t, c, "settings.update", map[string]any{"theme": "dark"})
				mustCommand(t, c, "profile.export", map[string]any{})
				peer := transfer.Peer{ID: "fixture-peer", Generation: 1}
				for i := 0; i < 100; i++ {
					if _, err := c.transfers.Offer(peer, coreCrashManifest(randomID(), "1")); !errors.Is(err, transfer.ErrReceiveRecovery) {
						t.Fatal("reopen admitted receive", err)
					}
					if _, err := command(c, randomID(), "transfer.accept", map[string]any{"transferId": "preparing", "destination": state.Preparation.Destination}); err == nil {
						t.Fatal("reopen accepted unavailable batch")
					}
				}
				entries, err := os.ReadDir(state.Preparation.Destination)
				if err != nil || len(entries) != 1 {
					t.Fatal("repeated failures allocated roots", err)
				}
				if _, err := command(c, randomID(), "receive.recovery.confirm", map[string]bool{"reviewed": true}); err == nil {
					t.Fatal("confirmation guessed empty root ownership")
				}
				if death == "false" {
					data, err := os.ReadFile(filepath.Join(state.Preparation.Destination, state.Preparation.Root, "unknown"))
					if err != nil || string(data) != "user data" {
						t.Fatal("ordinary Close lost unknown payload", err)
					}
				}
			})
		}
	}
}

func TestCorePreparationIntentBlocksReceiveKeepsSendAvailable(t *testing.T) {
	testCorePreparationBlocksReceiveKeepsSendAvailable(t, "")
}

func TestCoreRetirementGuardBlocksReceiveKeepsSendAvailable(t *testing.T) {
	for _, mode := range []string{"before", "after", "root", "destination", "stage", "marker", "partial"} {
		t.Run(mode, func(t *testing.T) {
			if runtime.GOOS == "windows" && (mode == "root" || mode == "destination" || mode == "stage" || mode == "marker") {
				t.Skip("open-directory rename")
			}
			testCorePreparationBlocksReceiveKeepsSendAvailable(t, mode)
		})
	}
}

func testCorePreparationBlocksReceiveKeepsSendAvailable(t *testing.T, guardMode string) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.a, "peer.autosave", map[string]any{"peerId": "peer-b", "enabled": true, "directory": t.TempDir()})
	peer, _ := p.b.trust("peer-a")
	if _, err := p.b.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, coreCrashManifest("intent-source", "1")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.b.transfers.Accept("intent-source", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	store := transfer.FileReceiveAccountingStore{Path: filepath.Join(p.b.dir, "receive-accounting.json")}
	state, err := store.LoadReceiveAccounting()
	if err != nil || len(state.Roots) != 1 {
		t.Fatal(err)
	}
	record := state.Roots[0]
	if err := p.b.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the exact persisted intermediate snapshot left between the
	// promotion and retirement writes; no test-only store replaces Core.Open.
	state.Version = 2
	state.Preparation = &transfer.ReceivePreparation{Destination: record.Destination, DestinationIdentity: record.DestinationIdentity, Root: filepath.Base(record.OwnedRoot), Stage: record.Stage, OwnerToken: record.OwnerToken}
	if guardMode == "" {
		if err := store.SaveReceiveAccounting(state); err != nil {
			t.Fatal(err)
		}
	} else {
		faulty := &coreRetirementFailureStore{FileReceiveAccountingStore: store, mode: guardMode, t: t}
		m, err := transfer.NewManager(transfer.Options{AccountingStore: faulty, ExistingState: true})
		if err != nil {
			t.Fatal(err)
		}
		localPeer := transfer.Peer{ID: "guard-fixture", Generation: 1}
		if err := m.BindPeer(localPeer); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Offer(localPeer, coreCrashManifest("guard-fixture", "1")); err != nil {
			t.Fatal(err)
		}
		if b, err := m.Accept("guard-fixture", t.TempDir()); !errors.Is(err, transfer.ErrReceiveRecovery) || b.State != transfer.Accepted {
			t.Fatalf("guard fixture %+v %v", b, err)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(store.Path + ".retirement"); err != nil {
			t.Fatal("fixture has no independent guard", err)
		}
	}
	nb := &pipeNode{hub: p.hub, ip: p.nb.ip, who: map[netip.Addr]string{p.na.ip: "peer-a"}, state: p.nb.state}
	c, err := Open(context.Background(), Options{Directory: p.b.dir, NodeFactory: func(string, string) (NetworkBackend, error) { return nb, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.op.Lock()
	err = c.startPeerServer([]netip.Addr{nb.ip})
	c.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if v := c.transfers.ReceiveRecovery(); v.State != "blocked" || v.ReservedBytes != nil {
		t.Fatal("preparation/guard evidence not blocked")
	}
	mustCommand(t, c, "settings.update", map[string]any{"theme": "dark"})
	mustCommand(t, c, "message.send", map[string]any{"peerId": "peer-a", "text": "send with pending intent"})
	source := filepath.Join(t.TempDir(), "outgoing.txt")
	if err := os.WriteFile(source, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	sent, err := c.SendPaths(context.Background(), "peer-a", []string{source})
	if err != nil {
		t.Fatal("send unavailable", err)
	}
	id := sent.(map[string]string)["id"]
	c.mu.RLock()
	outgoing := c.outgoing[id]
	c.mu.RUnlock()
	outgoing.mu.Lock()
	done := outgoing.runDone
	outgoing.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send stalled")
	}
	if received, err := p.a.transfers.Get(id); err != nil || received.State != transfer.Completed {
		t.Fatalf("send failed %+v %v", received, err)
	}
	if _, err := c.transfers.Offer(transfer.Peer{ID: peer.ID, Generation: peer.Generation}, coreCrashManifest("blocked", "1")); !errors.Is(err, transfer.ErrReceiveRecovery) {
		t.Fatal("receiving escaped", err)
	}
}

type coreRetirementFailureStore struct {
	transfer.FileReceiveAccountingStore
	mode string
	t    *testing.T
}

func (s *coreRetirementFailureStore) WithReceiveAccountingLimits(l transfer.AccountingLimits) transfer.ReceiveAccountingStore {
	s.Limits = l
	return s
}
func (s *coreRetirementFailureStore) AcquireReceiveRetirementGuard(g transfer.ReceiveRetirementGuard, l transfer.AccountingLimits) (transfer.ReceiveRetirementLease, error) {
	if s.mode == "partial" {
		if err := os.WriteFile(s.Path+".retirement", []byte(`{"version":1,`), 0600); err != nil {
			s.t.Fatal(err)
		}
		return nil, errors.New("partial guard creation")
	}
	return s.FileReceiveAccountingStore.AcquireReceiveRetirementGuard(g, l)
}
func (s *coreRetirementFailureStore) SaveReceiveAccounting(a transfer.ReceiveAccounting, leases ...transfer.ReceiveRetirementLease) error {
	if a.Preparation != nil || len(a.Roots) == 0 {
		return s.FileReceiveAccountingStore.SaveReceiveAccounting(a, leases...)
	}
	if s.mode == "before" {
		return errors.New("before forgetting commit")
	}
	if err := s.FileReceiveAccountingStore.SaveReceiveAccounting(a, leases...); err != nil {
		return err
	}
	if s.mode == "after" {
		return errors.New("after forgetting commit")
	}
	r := a.Roots[0]
	name := r.OwnedRoot
	switch s.mode {
	case "destination":
		name = r.Destination
	case "stage":
		name = filepath.Join(name, r.Stage)
	case "marker":
		name = filepath.Join(name, r.Stage, ".sobalink-owner")
	}
	if err := os.Rename(name, name+"-original"); err != nil {
		s.t.Fatal(err)
	}
	if s.mode == "marker" {
		if err := os.WriteFile(name, []byte(r.OwnerToken), 0600); err != nil {
			s.t.Fatal(err)
		}
	} else {
		if err := os.Mkdir(name, 0700); err != nil {
			s.t.Fatal(err)
		}
	}
	return nil
}
