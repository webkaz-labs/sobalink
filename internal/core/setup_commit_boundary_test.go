package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

type setupBoundaryBackend struct {
	lanNetworkBackend
	peers func() []lanlink.PublicPeerSnapshot
	pair  func(context.Context, lanlink.Invitation) error
	issue func(context.Context, context.Context, lanlink.Peer, string, time.Time) (lanlink.Invitation, error)
}

func (b *setupBoundaryBackend) PublicPeers() []lanlink.PublicPeerSnapshot { return b.peers() }
func (b *setupBoundaryBackend) PairInvitation(ctx context.Context, inv lanlink.Invitation) error {
	return b.pair(ctx, inv)
}
func (b *setupBoundaryBackend) IssueSetupInvitation(ctx, consent context.Context, peer lanlink.Peer, name string, expires time.Time) (lanlink.Invitation, error) {
	return b.issue(ctx, consent, peer, name, expires)
}
func (b *setupBoundaryBackend) IssueInvitation(context.Context, lanlink.Peer, string, time.Duration) (lanlink.Invitation, error) {
	return lanlink.Invitation{}, errors.New("setup must bind independent consent")
}
func setupCommitBoundaryFixture(t *testing.T) (*setupSession, *setupBoundaryBackend) {
	return setupCommitBoundaryRoleFixture(t, false)
}
func setupCommitBoundaryRoleFixture(t *testing.T, host bool) (*setupSession, *setupBoundaryBackend) {
	t.Helper()
	a, b, now := setupRequestTestSessions(t, "tofu")
	b.local.Expires = now.Add(120 * time.Second)
	a.target = &b.local
	setupRequestTestPlan(t, a, b, now)
	if host {
		a, b = b, a
	}
	c := openLANTestCore(t)
	store, err := c.ensureLANIdentity()
	if err != nil {
		t.Fatal(err)
	}
	next := store.copy()
	next.Identity = a.identity
	if err = store.save(next); err != nil {
		t.Fatal(err)
	}
	base, engine := testLANBackend()
	engine.key = a.local.Key
	backend := &setupBoundaryBackend{lanNetworkBackend: base, peers: func() []lanlink.PublicPeerSnapshot { return nil }}
	c.mu.Lock()
	c.node = backend
	c.mu.Unlock()
	a.core = c
	a.scope.ScopeID = setupScopeID(a.scope)
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{a.scope}, nil }
	a.authority = store.copy()
	a.setupGenerations = setupGenerations(a.authority)
	a.authorityProfile = c.setupProfileReview()
	a.borrowedNode = backend
	a.commitStarted = true
	a.remoteApproved = true
	a.localApproved = true
	if !host {
		a.invitation = &lanlink.Invitation{}
	}
	return a, backend
}
func TestSetupCommitUsesEarlierPeerDeadlineForPairPersistence(t *testing.T) {
	s, backend := setupCommitBoundaryFixture(t)
	called := false
	backend.pair = func(ctx context.Context, _ lanlink.Invitation) error {
		called = true
		deadline, ok := ctx.Deadline()
		// The effective plan ends earlier than the local session and 30s cap.
		if !ok || !deadline.Equal(s.plan.Expires) || deadline.After(time.Now().Add(30*time.Second)) {
			t.Fatal("pair operation exceeded effective approval deadline")
		}
		return errors.New("synthetic attempt boundary")
	}
	// Shorten the entire immutable transcript before launch. This test uses
	// no wire exchange after the local model mutation and grants no real pair.
	s.plan.Expires = time.Now().Add(time.Second)
	s.advanceCommit()
	if !called {
		t.Fatal("pair attempt was not exercised")
	}
	if !s.pairingAttempted || s.terminalState != "recovery_required" {
		t.Fatal("attempt failure was represented as clean success")
	}
}
func TestSetupCancellationBeforePairReservationPreventsLaunch(t *testing.T) {
	s, backend := setupCommitBoundaryFixture(t)
	reached, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	backend.peers = func() []lanlink.PublicPeerSnapshot { close(reached); <-release; return nil }
	backend.pair = func(context.Context, lanlink.Invitation) error {
		t.Error("cancelled setup launched pairing")
		return nil
	}
	go func() { defer close(done); s.advanceCommit() }()
	select {
	case <-reached:
	case <-time.After(3 * time.Second):
		t.Fatal("attempt did not reach reservation boundary")
	}
	s.core.op.Lock()
	s.stop("cancelled", "", "try_again")
	s.core.op.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled attempt did not join")
	}
	if s.pairingAttempted || s.paired || s.connected {
		t.Fatal("cancelled attempt gained authority")
	}
}

func TestSetupHostInvitationUsesIndependentConsentAndExactPlanDeadline(t *testing.T) {
	s, backend := setupCommitBoundaryRoleFixture(t, true)
	s.plan.Expires = time.Now().Add(time.Second)
	var issuedCtx, consentCtx context.Context
	backend.issue = func(ctx, consent context.Context, peer lanlink.Peer, name string, expires time.Time) (lanlink.Invitation, error) {
		issuedCtx, consentCtx = ctx, consent
		if ctx == consent || consent != s.ctx || !expires.Equal(s.plan.Expires) || peer.Key != s.peer.Key || name != s.local.Alias {
			t.Fatal("host invitation lost its separate approved setup lifetime")
		}
		return lanlink.Invitation{Expires: expires}, nil
	}
	s.advanceCommit()
	if issuedCtx == nil || !errors.Is(issuedCtx.Err(), context.Canceled) || consentCtx.Err() != nil || s.invitation == nil {
		t.Fatal("short issuance context became host invitation consent")
	}
	s.stop("cancelled", "", "try_again")
	if !errors.Is(consentCtx.Err(), context.Canceled) {
		t.Fatal("borrowed host invitation outlived setup cancellation")
	}
}
