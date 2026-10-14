package lanlink

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"tailscale.com/types/key"
)

// All lifetime boundaries run inside a virtual-time bubble. No operating-system
// sockets, clock changes, or production callbacks are needed to end consent.
func inboundConsentFixture(t *testing.T, boundary string) (*Node, Invitation, RemotePeer, []byte, func()) {
	t.Helper()
	host, client := testNode(), testNode()
	setupCtx, cancelSetup := context.WithCancel(context.Background())
	t.Cleanup(cancelSetup)
	end := func() { time.Sleep(time.Second) }
	issueCtx, cancelIssue := context.WithCancel(context.Background())
	var inv Invitation
	var err error
	switch boundary {
	case "invitation-expiry":
		inv, err = host.IssueInvitation(issueCtx, Peer{client.PublicKey(), "client"}, "host", time.Second)
	case "admission-expiry":
		inv, err = host.IssueInvitation(issueCtx, Peer{client.PublicKey(), "client"}, "host", time.Minute)
	default:
		expires := time.Now().Add(time.Minute)
		if boundary == "setup-plan-expiry" {
			expires = time.Now().Add(time.Second)
		}
		if boundary == "setup-context-expiry" {
			var cancel context.CancelFunc
			setupCtx, cancel = context.WithDeadline(setupCtx, time.Now().Add(time.Second))
			t.Cleanup(cancel)
		}
		if boundary == "setup-cancellation" {
			end = cancelSetup
		}
		inv, err = host.IssueSetupInvitation(issueCtx, setupCtx, Peer{client.PublicKey(), "client"}, "host", expires)
	}
	// Core ends this short context immediately after issuance. It must never
	// become the lifetime of a successfully issued invitation.
	cancelIssue()
	if err != nil {
		t.Fatal("invitation fixture failed")
	}
	role := key.NewNode()
	if !host.allowClient(role.Public()) {
		t.Fatal("live consent did not admit pairing transport")
	}
	if boundary == "admission-expiry" {
		host.admissions[keyString(role.Public())] = time.Now().Add(time.Second)
	}
	frame, _, req, err := client.makeRequest(inv.Host, inv.Token, role)
	if err != nil {
		t.Fatal("request fixture failed")
	}
	_, peer, _, err := host.readRequest(frame, req.RoleKey)
	if err != nil {
		t.Fatal("issuance-context cancellation invalidated live consent")
	}
	return host, inv, RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: key.NewNode(), IncomingClientKey: req.RoleKey}, frame, end
}

var inboundConsentBoundaries = []string{"invitation-expiry", "admission-expiry", "setup-plan-expiry", "setup-context-expiry", "setup-cancellation"}

func TestInboundPairCommitRechecksConsentAtPersistence(t *testing.T) {
	for _, boundary := range inboundConsentBoundaries {
		for _, phase := range []string{"before-save", "during-save"} {
			t.Run(boundary+"/"+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					host, inv, record, _, end := inboundConsentFixture(t, boundary)
					writes := 0
					var saved atomicPairState
					host.cfg.Persist = func(snapshot Snapshot, remotes []RemotePeer) error {
						writes++
						saved = atomicPairState{snapshot, CloneRemotePeers(remotes)}
						end()
						return nil
					}
					if phase == "before-save" {
						end()
					}
					err := host.commitPair(context.Background(), record, inv.Token, nil)
					wantErr := ErrInvite
					if boundary == "admission-expiry" {
						wantErr = ErrUntrusted
					}
					if !errors.Is(err, wantErr) {
						t.Fatal("ended permission was not rejected")
					}
					published := phase == "during-save"
					if errors.Is(err, config.ErrAtomicCommitted) != published || host.pairingRecovery != published {
						t.Fatal("recovery did not match durable publication")
					}
					if published && (writes != 1 || len(saved.Trust.Peers) != 1 || len(saved.Remotes) != 1) || !published && writes != 0 {
						t.Fatal("unexpected durable snapshot count")
					}
					if len(host.RemoteSnapshot()) != 0 || len(host.cfg.Trust.Snapshot().Peers) != 0 {
						t.Fatal("ended permission activated live authority")
					}
					if _, err := host.cfg.Trust.Epoch(record.Peer.Key); !errors.Is(err, ErrUntrusted) {
						t.Fatal("ended permission gained live trust")
					}
					if published {
						other := atomicPairRecord(testNode())
						if err := host.commitPair(context.Background(), other, "", registeredPairAttemptFixture(t, host, other.Peer.Key)); !errors.Is(err, config.ErrAtomicRecovery) || writes != 1 {
							t.Fatal("recovery allowed a later stale snapshot write")
						}
					}
				})
			})
		}
	}
}

func TestInboundPairingHandlerDoesNotAcknowledgeEndedConsent(t *testing.T) {
	for _, boundary := range inboundConsentBoundaries {
		t.Run(boundary, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				host, inv, record, frame, end := inboundConsentFixture(t, boundary)
				cleanupDone := make(chan struct{})
				writes := 0
				host.cfg.Persist = func(snapshot Snapshot, remotes []RemotePeer) error {
					writes++
					if len(snapshot.Peers) != 1 || len(remotes) != 1 {
						t.Error("missing published pair")
					}
					end()
					// Borrowed-host cleanup cannot acquire Book.mu until Persist
					// returns. Independent consent must deny activation first.
					cleanupStarted := make(chan struct{})
					go func() {
						close(cleanupStarted)
						host.CancelInvitation(inv.Token)
						close(cleanupDone)
					}()
					<-cleanupStarted
					return nil
				}
				server := &PairingServer{node: host, ctx: context.Background()}
				left, right := net.Pipe()
				defer right.Close()
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer left.Close()
					server.handle(left, record.IncomingClientKey)
				}()
				if err := writePairFrame(right, frame); err != nil {
					t.Fatal("request frame failed")
				}
				if _, err := readPairFrame(right); !errors.Is(err, io.EOF) {
					t.Fatal("ended consent received a success acknowledgement")
				}
				<-done
				<-cleanupDone
				if writes != 1 || !host.pairingRecovery || len(host.RemoteSnapshot()) != 0 || len(host.cfg.Trust.Snapshot().Peers) != 0 {
					t.Fatal("handler activated authority or lost recovery after consent ended")
				}
			})
		})
	}
}

func TestSetupInvitationBindsExactDeadlineAndIndependentConsent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host, client := testNode(), testNode()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		issueCtx, cancelIssue := context.WithTimeout(ctx, 5*time.Second)
		expires := time.Now().Add(15*time.Second + time.Nanosecond)
		inv, err := host.IssueSetupInvitation(issueCtx, ctx, Peer{client.PublicKey(), "client"}, "host", expires)
		cancelIssue()
		if err != nil || !inv.Expires.Equal(expires) {
			t.Fatal("invitation changed the exact approved expiry")
		}
		if _, err := host.cfg.Trust.invitedPeer(inv.Token, client.PublicKey(), host.cfg.Relay.Address, time.Now()); err != nil {
			t.Fatal("short issuance context became consent lifetime")
		}
		cancel()
		if host.cfg.Trust.pending(client.PublicKey()) || host.cfg.Trust.anyPending() {
			t.Fatal("cancelled setup retained pending invitation admission")
		}
		if _, err := host.cfg.Trust.invitedPeer(inv.Token, client.PublicKey(), host.cfg.Relay.Address, time.Now()); !errors.Is(err, ErrInvite) {
			t.Fatal("cancelled setup allowed invitation consumption")
		}
	})
}

func TestSetupInvitationRejectsConsentEndedDuringIssueLock(t *testing.T) {
	host, client := testNode(), testNode()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host.pairMu.Lock()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := host.IssueSetupInvitation(context.Background(), ctx, Peer{client.PublicKey(), "client"}, "host", time.Now().Add(time.Minute))
		done <- err
	}()
	<-started
	cancel()
	host.pairMu.Unlock()
	if err := <-done; !errors.Is(err, ErrInvite) || len(host.cfg.Trust.invites) != 0 {
		t.Fatal("ended setup consent issued an invitation after lock wait")
	}
}
