package lanlink

import (
	"context"
	"errors"
	"testing"

	"tailscale.com/types/key"
)

func TestTokenlessPairRequiresRegisteredAttempt(t *testing.T) {
	for _, operation := range []string{"commit", "reply"} {
		for _, registration := range []string{"nil", "unregistered", "substituted", "wrong-peer", "retired", "revoked"} {
			t.Run(operation+"/"+registration, func(t *testing.T) {
				host, client, inv, role, _, plain, req := pairFixture(t)
				hostRole := key.NewNode()
				reply, err := host.makeReply(req, plain, hostRole)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				var attempt *pairAttempt
				switch registration {
				case "unregistered":
					attempt = &pairAttempt{peer: host.PublicKey()}
				case "substituted":
					registeredPairAttemptFixture(t, client, host.PublicKey())
					attempt = &pairAttempt{peer: host.PublicKey()}
				case "wrong-peer":
					attempt = registeredPairAttemptFixture(t, client, GenerateIdentity().PublicKey())
				case "retired":
					attempt = registeredPairAttemptFixture(t, client, host.PublicKey())
					client.retirePairAttempt(host.PublicKey(), attempt)
				case "revoked":
					attempt = registeredPairAttemptFixture(t, client, host.PublicKey())
					if err := client.Revoke(host.PublicKey()); err != nil {
						t.Fatal(err)
					}
				}
				persistCalls := 0
				client.cfg.Persist = func(Snapshot, []RemotePeer) error { persistCalls++; return nil }
				if operation == "commit" {
					record := RemotePeer{Peer: inv.Host.Peer, Address: inv.Host.Address, ClientPrivate: role, IncomingClientKey: keyString(hostRole.Public())}
					err = client.commitPair(ctx, record, "", attempt)
				} else {
					err = client.acceptPairReply(ctx, reply, inv.Host, req, plain, role, attempt)
				}
				if !errors.Is(err, ErrUntrusted) {
					t.Errorf("tokenless %s with %s attempt: got %v, want ErrUntrusted", operation, registration, err)
				}
				if ctx.Err() != nil {
					t.Fatal("rejection relied on canceled context")
				}
				if persistCalls != 0 || len(client.RemoteSnapshot()) != 0 || len(client.cfg.Trust.Snapshot().Peers) != 0 {
					t.Fatal("unauthorized pairing persisted or activated trust")
				}
			})
		}
	}
}

func TestPairAttemptRegistrationCountTracksOnlyLiveAttempts(t *testing.T) {
	n := testNode()
	const peers, perPeer = 4, 3
	var live []*pairAttempt
	checkCount := func(want int) {
		t.Helper()
		n.mu.Lock()
		defer n.mu.Unlock()
		count := 0
		for peer, attempts := range n.attempts {
			if len(attempts) == 0 {
				t.Errorf("empty attempt bucket retained for %s", peer)
			}
			count += len(attempts)
		}
		if count != want {
			t.Fatalf("registered attempts: got %d, want %d live attempts", count, want)
		}
	}
	for i := 0; i < peers; i++ {
		peer := GenerateIdentity().PublicKey()
		for j := 0; j < perPeer; j++ {
			live = append(live, registeredPairAttemptFixture(t, n, peer))
			checkCount(len(live))
		}
		if err := n.Revoke(peer); err != nil {
			t.Fatal(err)
		}
		checkCount(len(live))
	}
	for i, attempt := range live {
		n.retirePairAttempt(attempt.peer, attempt)
		checkCount(len(live) - i - 1)
	}
	if len(n.attempts) != 0 {
		t.Fatal("attempt buckets remained after all live attempts retired")
	}
}

func TestRevokeUnpairedPeersDoesNotRetainRevocationBookkeeping(t *testing.T) {
	n := testNode()

	const identities = 256
	for i := 0; i < identities; i++ {
		peer := GenerateIdentity().PublicKey()
		if _, err := parseNodePublic(peer); err != nil {
			t.Fatalf("generated peer identity %d is invalid: %v", i, err)
		}
		if err := n.Revoke(peer); err != nil {
			t.Fatalf("revoke unpaired peer %d: %v", i, err)
		}
	}

	if got := len(n.cfg.Trust.peers); got != 0 {
		t.Errorf("trust book retained %d peers after revoking unpaired identities", got)
	}
	if got := len(n.clients); got != 0 {
		t.Errorf("node retained %d clients after revoking unpaired identities", got)
	}
	if got := len(n.attempts); got != 0 {
		t.Errorf("node retained %d attempt registrations after %d unpaired revocations; want 0", got, identities)
	}
}

func TestPairAttemptRetirementRejectsABA(t *testing.T) {
	n := testNode()
	peerNode := testNode()
	offer := peerNode.Offer("remote")
	remote := RemotePeer{
		Peer:              offer.Peer,
		Address:           offer.Address,
		ClientPrivate:     key.NewNode(),
		IncomingClientKey: keyString(key.NewNode().Public()),
	}
	register := func() (*pairAttempt, context.Context) {
		ctx, cancel := context.WithCancel(context.Background())
		a := &pairAttempt{peer: remote.Peer.Key, cancel: cancel}
		n.mu.Lock()
		if n.attempts[remote.Peer.Key] == nil {
			n.attempts[remote.Peer.Key] = make(map[*pairAttempt]struct{})
		}
		n.attempts[remote.Peer.Key][a] = struct{}{}
		n.mu.Unlock()
		return a, ctx
	}
	a, ctxA := register()
	b, ctxB := register()
	if err := n.Revoke(remote.Peer.Key); err != nil {
		t.Fatal(err)
	}
	n.retirePairAttempt(remote.Peer.Key, a)
	if err := ctxA.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("attempt A was not canceled: %v", err)
	}
	if err := ctxB.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("attempt B was not canceled: %v", err)
	}
	if err := n.commitPair(context.Background(), remote, "", b); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("revoked attempt B committed with independent context: %v", err)
	}
	n.retirePairAttempt(remote.Peer.Key, b)
	c, _ := register()
	if err := n.commitPair(context.Background(), remote, "", c); err != nil {
		t.Fatalf("fresh attempt C could not pair after revocation: %v", err)
	}
	n.retirePairAttempt(remote.Peer.Key, c)
	if err := n.commitPair(context.Background(), remote, "", a); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("retired attempt A committed after attempt C (ABA): %v", err)
	}
	if got := len(n.attempts); got != 0 {
		t.Fatalf("attempt registrations remained after retirement: %d", got)
	}
}

func TestUnpairedRevocationBookkeepingStaysBoundedAcrossChurn(t *testing.T) {
	n := testNode()
	const identities = 4096
	for i := 0; i < identities; i++ {
		if err := n.Revoke(GenerateIdentity().PublicKey()); err != nil {
			t.Fatalf("revoke unpaired peer %d: %v", i, err)
		}
	}
	if got := len(n.attempts); got != 0 {
		t.Fatalf("unpaired revocation retained %d attempt registrations", got)
	}
}
