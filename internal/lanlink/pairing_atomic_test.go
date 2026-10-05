package lanlink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/types/key"
)

type atomicPairState struct {
	Trust   Snapshot     `json:"trust"`
	Remotes []RemotePeer `json:"remotes"`
}

func atomicPairRecord(n *Node) RemotePeer {
	offer := n.Offer("peer")
	return RemotePeer{Peer: offer.Peer, Address: offer.Address, ClientPrivate: key.NewNode(), IncomingClientKey: keyString(key.NewNode().Public())}
}

func TestPublishedPairFreezesSnapshotsUntilReopen(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		for _, next := range []string{"pair", "revoke"} {
			t.Run(direction+"/"+next, func(t *testing.T) {
				for _, entry := range os.Environ() {
					name, _, _ := strings.Cut(entry, "=")
					upper := strings.ToUpper(name)
					if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
						t.Setenv(name, "")
					}
				}
				host, client, inv, clientRole, frame, plain, req := pairFixture(t)
				n, uncertainPeer := host, client
				if direction == "outbound" {
					n, uncertainPeer = client, host
				}
				t.Cleanup(func() { n.Close() })
				path := filepath.Join(t.TempDir(), "lan.json")
				writes := 0
				n.cfg.Persist = func(s Snapshot, remotes []RemotePeer) error {
					writes++
					if err := config.WriteJSON(path, atomicPairState{s, remotes}); err != nil {
						return err
					}
					if writes == 2 {
						return fmt.Errorf("sync: %w", config.ErrAtomicCommitted)
					}
					return nil
				}
				existing := atomicPairRecord(testNode())
				if err := n.commitPair(context.Background(), existing, "", registeredPairAttemptFixture(t, n, existing.Peer.Key)); err != nil {
					t.Fatal(err)
				}
				epoch, err := n.cfg.Trust.Epoch(existing.Peer.Key)
				if err != nil {
					t.Fatal(err)
				}
				flow := &closer{}
				release, err := n.cfg.Trust.Track(existing.Peer.Key, epoch, flow)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				verified, peer, opened, err := host.readRequest(frame, keyString(clientRole.Public()))
				if err != nil {
					t.Fatal(err)
				}
				hostRole := key.NewNode()
				record := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: hostRole, IncomingClientKey: req.RoleKey}
				if direction == "inbound" {
					err = host.commitPair(context.Background(), record, req.Token, nil)
				} else {
					if err := host.commitPair(context.Background(), record, req.Token, nil); err != nil {
						t.Fatal(err)
					}
					reply, e := host.makeReply(verified, opened, hostRole)
					if e != nil {
						t.Fatal(e)
					}
					live := &tailcat.Client{Key: clientRole, Server: inv.Host.Address}
					defer live.Close()
					err = client.acceptPairReply(context.Background(), reply, inv.Host, req, plain, clientRole, registeredPairAttemptFixture(t, client, host.PublicKey()), live)
					if !errors.Is(err, ErrRemotePairedLocalSave) {
						t.Fatal(err)
					}
				}
				if !errors.Is(err, config.ErrAtomicCommitted) || !n.pairingRecovery {
					t.Fatal("published pairing did not enter recovery", err)
				}
				if len(n.RemoteSnapshot()) != 1 || len(n.cfg.Trust.Snapshot().Peers) != 1 {
					t.Fatal("uncertain pair activated")
				}
				if _, err := n.cfg.Trust.Epoch(uncertainPeer.PublicKey()); !errors.Is(err, ErrUntrusted) {
					t.Fatal("uncertain trust activated", err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if next == "pair" {
					other := atomicPairRecord(testNode())
					err = n.commitPair(context.Background(), other, "", registeredPairAttemptFixture(t, n, other.Peer.Key))
				} else {
					err = n.Revoke(existing.Peer.Key)
					if flow.closed.Load() != 1 {
						t.Fatal("recovery prevented local revocation from closing its flow")
					}
				}
				if !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("later snapshot writer not blocked", err)
				}
				if errors.Is(err, config.ErrAtomicCommitted) {
					t.Fatal("blocked operation falsely claimed a new replacement", err)
				}
				for i := 0; i < 32; i++ {
					if err := n.Revoke(GenerateIdentity().PublicKey()); !errors.Is(err, config.ErrAtomicRecovery) {
						t.Fatal(err)
					}
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) || writes != 2 {
					t.Fatal("recovery wrote stale state", writes, err)
				}
				if _, err := n.IssueInvitation(context.Background(), Peer{GenerateIdentity().PublicKey(), "next"}, "local", time.Minute); !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("recovery issued new invitation", err)
				}
				if err := n.Pair(context.Background(), testNode().Offer("next"), strings.Repeat("a", 43)); !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("recovery contacted next peer", err)
				}
				if err := n.Close(); err != nil {
					t.Fatal(err)
				}
				if err := n.Revoke(existing.Peer.Key); !errors.Is(err, config.ErrAtomicRecovery) || writes != 2 {
					t.Fatal("closed recovery wrote stale state", err)
				}
				var saved atomicPairState
				if err := config.ReadJSON(path, &saved); err != nil {
					t.Fatal(err)
				}
				restored := NewBook()
				if err := restored.Restore(saved.Trust); err != nil {
					t.Fatal(err)
				}
				loaded, err := NewNode(NodeConfig{Identity: n.cfg.Identity, Relay: n.cfg.Relay, Trust: restored, Remotes: saved.Remotes, Persist: n.cfg.Persist})
				if err != nil {
					t.Fatal("reopen rejected published snapshot", err)
				}
				defer loaded.Close()
				if loaded.pairingRecovery || !reflect.DeepEqual(loaded.RemoteSnapshot(), saved.Remotes) || !reflect.DeepEqual(loaded.cfg.Trust.Snapshot(), saved.Trust) {
					t.Fatal("reopen disk/runtime disagreement")
				}
				for _, r := range loaded.clients {
					if r.client != nil || r.started {
						t.Fatal("reopen started work")
					}
				}
				if err := loaded.Revoke(uncertainPeer.PublicKey()); err != nil || writes != 3 {
					t.Fatal("explicit revoke after reopen failed", err, writes)
				}
				t.Logf("writesBeforeReopen=2 publishedPeers=%d uncertainActive=false savedRuntimeAgreeAfterReopen=true writesAfterExplicitRevoke=%d", len(saved.Trust.Peers), writes)
			})
		}
	}
}

func TestUnpublishedPairFailureRemainsRetryable(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		t.Run(direction, func(t *testing.T) {
			host, client, inv, role, frame, plain, req := pairFixture(t)
			verified, peer, opened, err := host.readRequest(frame, keyString(role.Public()))
			if err != nil {
				t.Fatal(err)
			}
			hostRole := key.NewNode()
			record := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: hostRole, IncomingClientKey: req.RoleKey}
			n := host
			attempt := registeredPairAttemptFixture(t, client, host.PublicKey())
			reply, err := host.makeReply(verified, opened, hostRole)
			if err != nil {
				t.Fatal(err)
			}
			commit := func() error { return host.commitPair(context.Background(), record, req.Token, nil) }
			if direction == "outbound" {
				n = client
				if err := host.commitPair(context.Background(), record, req.Token, nil); err != nil {
					t.Fatal(err)
				}
				commit = func() error {
					return client.acceptPairReply(context.Background(), reply, inv.Host, req, plain, role, attempt)
				}
			}
			path := filepath.Join(t.TempDir(), "lan.json")
			calls := 0
			n.cfg.Persist = func(s Snapshot, r []RemotePeer) error {
				calls++
				if calls == 1 {
					return os.ErrPermission
				}
				return config.WriteJSON(path, atomicPairState{s, r})
			}
			if err := commit(); !errors.Is(err, os.ErrPermission) {
				t.Fatal(err)
			}
			if n.pairingRecovery || len(n.RemoteSnapshot()) != 0 || len(n.cfg.Trust.Snapshot().Peers) != 0 {
				t.Fatal("unpublished failure changed authority or locked retries")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unpublished failure wrote file", err)
			}
			if err := commit(); err != nil || calls != 2 {
				t.Fatal("valid live retry rejected", err, calls)
			}
			var saved atomicPairState
			if err := config.ReadJSON(path, &saved); err != nil || !reflect.DeepEqual(saved.Remotes, n.RemoteSnapshot()) || !reflect.DeepEqual(saved.Trust, n.cfg.Trust.Snapshot()) {
				t.Fatal("retry disk/runtime differ", err)
			}
		})
	}
}

func TestInboundPublishedSaveDoesNotDeadlockPairingShutdown(t *testing.T) {
	host, _, _, role, frame, _, _ := pairFixture(t)
	path := filepath.Join(t.TempDir(), "lan.json")
	entered, proceed := make(chan struct{}), make(chan struct{})
	host.cfg.Persist = func(s Snapshot, r []RemotePeer) error {
		close(entered)
		<-proceed
		if err := config.WriteJSON(path, atomicPairState{s, r}); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &PairingServer{node: host, ctx: ctx, cancel: cancel, listener: newFakeListener(), conns: make(map[net.Conn]string)}
	host.pairing = p
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	p.conns[left] = keyString(role.Public())
	p.wg.Add(1)
	go func() { defer p.wg.Done(); defer left.Close(); p.handle(left, keyString(role.Public())) }()
	if err := writePairFrame(right, frame); err != nil {
		t.Fatal(err)
	}
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- host.Close() }()
	close(proceed)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on persistence/book/client mutexes or its own pairing handler")
	}
	right.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := readPairFrame(right); err == nil {
		t.Fatal("uncertain inbound pairing sent success")
	}
	if !host.pairingRecovery || len(host.RemoteSnapshot()) != 0 {
		t.Fatal("uncertain inbound pair activated")
	}
	if err := host.Revoke(GenerateIdentity().PublicKey()); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal(err)
	}
}

func TestPublishedPairBlocksQueuedSnapshotWriter(t *testing.T) {
	for _, operation := range []string{"pair", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			n := testNode()
			path := filepath.Join(t.TempDir(), "lan.json")
			entered, proceed := make(chan struct{}), make(chan struct{})
			writes := 0
			n.cfg.Persist = func(s Snapshot, r []RemotePeer) error {
				writes++
				close(entered)
				<-proceed
				if err := config.WriteJSON(path, atomicPairState{s, r}); err != nil {
					return err
				}
				return config.ErrAtomicCommitted
			}
			first, other := atomicPairRecord(testNode()), atomicPairRecord(testNode())
			firstAttempt := registeredPairAttemptFixture(t, n, first.Peer.Key)
			nextAttempt := registeredPairAttemptFixture(t, n, other.Peer.Key)
			firstDone, nextDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- n.commitPair(context.Background(), first, "", firstAttempt) }()
			<-entered
			go func() {
				if operation == "pair" {
					nextDone <- n.commitPair(context.Background(), other, "", nextAttempt)
				} else {
					nextDone <- n.Revoke(other.Peer.Key)
				}
			}()
			close(proceed)
			for i, done := range []chan error{firstDone, nextDone} {
				sentinel := config.ErrAtomicCommitted
				if i == 1 {
					sentinel = config.ErrAtomicRecovery
				}
				select {
				case err := <-done:
					if !errors.Is(err, sentinel) {
						t.Fatal("queued operation lost recovery latch", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("queued snapshot writer deadlocked")
				}
			}
			if writes != 1 || !n.pairingRecovery || len(n.RemoteSnapshot()) != 0 {
				t.Fatal("queued writer activated or overwrote uncertainty", writes)
			}
			var saved atomicPairState
			if err := config.ReadJSON(path, &saved); err != nil || len(saved.Remotes) != 1 || saved.Remotes[0].Peer.Key != first.Peer.Key {
				t.Fatal("published pair lost", err)
			}
			n.retirePairAttempt(first.Peer.Key, firstAttempt)
			n.retirePairAttempt(other.Peer.Key, nextAttempt)
			if len(n.attempts) != 0 {
				t.Fatal("completed recovery attempts accumulated")
			}
			if err := n.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
