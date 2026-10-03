package lanlink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/types/key"
)

func TestPeerAdmissionCanExceedDefaultAndLowerWithoutRevocation(t *testing.T) {
	var limit atomic.Int64
	limit.Store(130)
	book := NewBookWithPeerLimit(limit.Load)
	for i := 1; i <= 130; i++ {
		p := Peer{Key: fmt.Sprintf("%064x", i), Name: "peer"}
		now := time.Now()
		token, err := book.Issue(context.Background(), p, cfg(), now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := book.Redeem(context.Background(), token, p.Key, cfg().Listen, now); err != nil {
			t.Fatal(err)
		}
	}
	limit.Store(1)
	saved := book.Snapshot()
	if len(saved.Peers) != 130 {
		t.Fatal("lowering removed approval")
	}
	if _, err := book.Epoch(saved.Peers[0].Key); err != nil {
		t.Fatal("existing approval lost", err)
	}
	var capacityError *PeerCapacityError
	if _, err := book.Issue(context.Background(), Peer{Key: fmt.Sprintf("%064x", 131), Name: "next"}, cfg(), time.Now(), time.Minute); !errors.As(err, &capacityError) {
		t.Fatal("lowered policy admitted another invitation", err)
	}
	restored := NewBookWithPeerLimit(limit.Load)
	if err := restored.Restore(saved); err != nil {
		t.Fatal("lowered policy prevented existing trust restore", err)
	}
	book.Revoke(saved.Peers[0].Key)
	if _, err := book.Epoch(saved.Peers[0].Key); !errors.Is(err, ErrUntrusted) {
		t.Fatal("revocation lost", err)
	}
	limit.Store(131)
	if err := book.approveVerified(Peer{Key: fmt.Sprintf("%064x", 131), Name: "next"}); err != nil {
		t.Fatal("raised policy ineffective", err)
	}
}

func TestPairCommitUsesLiveAdmissionAndRestoresManyDistinctRoles(t *testing.T) {
	// NewNode validates configuration only. No listeners or relay contact occur.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		u := strings.ToUpper(name)
		if u == "HTTP_PROXY" || u == "HTTPS_PROXY" || u == "ALL_PROXY" || strings.HasPrefix(u, "TS_") && u != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	node := testNode()
	var limit atomic.Int64
	limit.Store(130)
	node.cfg.Trust = NewBookWithPeerLimit(limit.Load)
	var saved Snapshot
	var remotes []RemotePeer
	node.cfg.Persist = func(s Snapshot, records []RemotePeer) error { saved, remotes = s, records; return nil }
	var first RemotePeer
	for i := 0; i < 129; i++ {
		remote := testNode()
		r := RemotePeer{Peer: Peer{remote.PublicKey(), "peer"}, Address: remote.Address(), ClientPrivate: key.NewNode(), IncomingClientKey: keyString(key.NewNode().Public())}
		if i == 0 {
			first = r
		}
		if err := node.commitPair(context.Background(), r, "", 0); err != nil {
			t.Fatalf("peer %d: %v", i, err)
		}
	}
	limit.Store(1)
	restored := NewBookWithPeerLimit(limit.Load)
	if err := restored.Restore(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewNode(NodeConfig{Identity: node.cfg.Identity, Relay: node.cfg.Relay, Trust: restored, Remotes: remotes, Persist: node.cfg.Persist})
	if err != nil {
		t.Fatal("129 distinct paired records failed restore", err)
	}
	t.Cleanup(func() { _ = loaded.Close() })
	if len(loaded.PublicPeers()) != 129 {
		t.Fatal("restored peers truncated")
	}
	for _, client := range loaded.clients {
		if client.client != nil {
			t.Fatal("restore allocated an active client")
		}
	}
	other := testNode()
	newRemote := RemotePeer{Peer: Peer{other.PublicKey(), "next"}, Address: other.Address(), ClientPrivate: key.NewNode(), IncomingClientKey: keyString(key.NewNode().Public())}
	var capacityError *PeerCapacityError
	if err := node.commitPair(context.Background(), newRemote, "", 0); !errors.As(err, &capacityError) {
		t.Fatal("lowered policy admitted pairing", err)
	}
	limit.Store(130)
	alias := newRemote
	alias.IncomingClientKey = first.IncomingClientKey
	if err := node.commitPair(context.Background(), alias, "", 0); err == nil {
		t.Fatal("raised policy allowed cross-peer role reuse")
	}
	if err := node.commitPair(context.Background(), newRemote, "", 0); err != nil {
		t.Fatal("live raise failed", err)
	}
}

func TestConcurrentBookPolicyChangesPreserveExistingTrust(t *testing.T) {
	var limit atomic.Int64
	limit.Store(256)
	book := NewBookWithPeerLimit(limit.Load)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			limit.Store(int64(1 + i%256))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 1; i <= 256; i++ {
			_ = book.approveVerified(Peer{Key: fmt.Sprintf("%064x", i), Name: "peer"})
			_ = book.Snapshot()
		}
	}()
	wg.Wait()
	saved := book.Snapshot()
	limit.Store(1)
	for _, p := range saved.Peers {
		if _, err := book.Epoch(p.Key); err != nil {
			t.Fatal("policy change revoked an identity", err)
		}
	}
}
