package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

func applyLANTestPolicy(t *testing.T, c *Core, policy capacity.Policy) error {
	t.Helper()
	preview := mustCommand(t, c, "policy.preview", map[string]any{"policy": policy}).(map[string]any)
	raw, err := json.Marshal(map[string]any{"policy": policy, "expectedRevision": preview["revision"]})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.capacityCommand("policy.apply", raw)
	return err
}

func TestLANPairedPolicyExceeds128AndPreservesExistingDataWhenLowered(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	saved := store.copy()
	for i := 1; i <= 129; i++ {
		peer := lanlink.Peer{Key: fmt.Sprintf("%064x", i), Name: "paired peer"}
		saved.Trust.Peers = append(saved.Trust.Peers, peer)
		saved.Remotes = append(saved.Remotes, lanlink.RemotePeer{Peer: peer})
	}
	if err := store.save(saved); networkErrorCode(err) != "peer_capacity" {
		t.Fatal("default admission policy missing", err)
	}
	policy := capacity.Defaults()
	policy.Logical["trustedPeers"] = capacity.Unlimited()
	if err := applyLANTestPolicy(t, c, policy); err != nil {
		t.Fatal(err)
	}
	if err := validateCapacityBackend("lan", policy); err != nil {
		t.Fatal("old LAN ceiling remains", err)
	}
	if err := store.save(saved); err != nil {
		t.Fatal("explicit unlimited policy failed at 129", err)
	}
	before, _ := os.ReadFile(store.path)
	policy.Logical["trustedPeers"] = capacity.Limited(1)
	if err := applyLANTestPolicy(t, c, policy); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(store.path)
	if string(before) != string(after) || len(c.profileCopy().Peers) != 0 {
		t.Fatal("policy change deleted pairs or granted application trust")
	}
	loaded, err := readLANStore(store.path)
	if err != nil || len(loaded.copy().Remotes) != 129 {
		t.Fatal("lowered admission policy prevented restore", err)
	}
	usage := c.capacityUsage()
	if usage["lanPairedPeers"] != 129 || usage["lanStateBytes"] != int64(len(before)) || usage["trustedPeers"] != 0 {
		t.Fatalf("namespace usage mismatch: %v", usage)
	}
	next := store.copy()
	peer := lanlink.Peer{Key: fmt.Sprintf("%064x", 130), Name: "another peer"}
	next.Trust.Peers = append(next.Trust.Peers, peer)
	next.Remotes = append(next.Remotes, lanlink.RemotePeer{Peer: peer})
	if err := store.save(next); networkErrorCode(err) != "peer_capacity" {
		t.Fatal("lowered policy permitted new pair", err)
	}
	if err := (offlineLANRevoker{store}).Revoke(saved.Trust.Peers[0].Key); err != nil {
		t.Fatal("lowered policy blocked revocation", err)
	}
	if len(store.copy().Remotes) != 128 {
		t.Fatal("explicit revocation failed")
	}
}

func TestLANStateByteBudgetIsExplicitAndCannotStrandSavedData(t *testing.T) {
	c := openLANTestCore(t)
	store, err := c.ensureLANIdentity()
	if err != nil {
		t.Fatal(err)
	}
	policy := capacity.Defaults()
	policy.Resources["lanStateBytes"] = capacity.Limited(3 << 20)
	if err := applyLANTestPolicy(t, c, policy); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	padded := append(append([]byte(nil), original...), []byte(strings.Repeat(" ", 2<<20))...)
	if err := os.WriteFile(store.path, padded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLANStore(store.path); err != nil {
		t.Fatal("configured budget retained old 2 MiB ceiling", err)
	}
	if c.capacityUsage()["lanStateBytes"] != int64(len(padded)) {
		t.Fatal("storage usage omitted saved JSON framing")
	}
	if _, err := readLANStoreWithPolicy(store.path, capacity.Defaults()); networkErrorCode(err) != "lan_state_capacity" {
		t.Fatal("smaller reader budget was not enforced", err)
	}
	policy.Resources["lanStateBytes"] = capacity.Limited(2 << 20)
	if err := applyLANTestPolicy(t, c, policy); networkErrorCode(err) != "policy_in_use" {
		t.Fatal("lowered budget stranded existing state", err)
	}
	if c.limit("resources", "lanStateBytes") != 3<<20 {
		t.Fatal("rejected budget was published")
	}
	if _, err := readLANStore(store.path); err != nil {
		t.Fatal("rejected policy made saved state unreadable", err)
	}
	if err := os.WriteFile(store.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	current, _ := json.MarshalIndent(store.copy(), "", "  ")
	policy.Resources["lanStateBytes"] = capacity.Limited(max(int64(len(original)), int64(len(current))+1))
	if err := applyLANTestPolicy(t, c, policy); err != nil {
		t.Fatal(err)
	}
	next := store.copy()
	next.Selection = testLANSelection()
	if err := store.save(next); networkErrorCode(err) != "lan_state_capacity" {
		t.Fatal("write ignored selected byte budget", err)
	}
	if store.copy().Selection != nil {
		t.Fatal("failed budget check published state")
	}
}

func TestLANPolicyApplySerializesWithPairPersistence(t *testing.T) {
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	policy := capacity.Defaults()
	policy.Logical["trustedPeers"] = capacity.Unlimited()
	policy.Resources["lanStateBytes"] = capacity.Limited(4096)
	if err := applyLANTestPolicy(t, c, policy); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 50; i++ {
			next := store.copy()
			peer := lanlink.Peer{Key: fmt.Sprintf("%064x", i), Name: "peer"}
			next.Trust.Peers = append(next.Trust.Peers, peer)
			next.Remotes = append(next.Remotes, lanlink.RemotePeer{Peer: peer})
			_ = store.persist(next.Trust, next.Remotes)
		}
	}()
	for i := 0; i < 20; i++ {
		next := policy.Clone()
		next.Logical["trustedPeers"] = capacity.Limited(int64(1 + i))
		next.Resources["lanStateBytes"] = capacity.Limited(int64(4096 + i%3*1024))
		if err := applyLANTestPolicy(t, c, next); err != nil && networkErrorCode(err) != "policy_in_use" {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if _, err := readLANStore(store.path); err != nil {
		t.Fatal("concurrent policy apply stranded saved state", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, Version: "test"})
	if err != nil {
		t.Fatal("private LAN restore failed after lowering policy", err)
	}
	_ = reopened.Close()
}
