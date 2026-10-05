package lanlink

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
)

// All addresses and identities in these regressions are synthetic. Transport
// factories remain in-process so no real relay is contacted.
type routeReductionCase struct {
	issuer, receiver *Node
	peer             string
	now              time.Time
	candidates       []RouteCandidate
	mutate           func() error
	selected         []string
	sequence         uint64
}

func routeReductionFixture(t *testing.T, operation string) routeReductionCase {
	t.Helper()
	a, b, now, candidates := routeNodesFixture(t)
	candidates = append(candidates, routeFixture("external", "192.0.2.21:443"))
	raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{candidates[0].ID(), candidates[1].ID()}
	if operation != "legacy-to-managed" {
		lifetime, expiry := RouteLifetimeUntilRevoked, time.Time{}
		if operation == "approve-finite-shorter" {
			lifetime, expiry = RouteLifetimeFinite, now.Add(10*time.Minute)
		}
		if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), ids, lifetime, expiry, now); err != nil {
			t.Fatal(err)
		}
	}
	out := routeReductionCase{issuer: a, receiver: b, peer: a.PublicKey(), now: now, candidates: candidates, selected: ids[:1], sequence: 1}
	lifetime, expiry := RouteLifetimeUntilRevoked, time.Time{}
	switch operation {
	case "apply-remove", "apply-mixed", "apply-empty", "apply-shorter-offer", "withdrawal":
		out.sequence = 2
		nextCandidates := candidates[:1]
		if operation == "apply-mixed" {
			nextCandidates = []RouteCandidate{candidates[0], candidates[2]}
			out.selected = []string{candidates[0].ID(), candidates[2].ID()}
		}
		if operation == "apply-empty" {
			nextCandidates, out.selected = candidates, nil
		}
		if operation == "withdrawal" {
			nextCandidates, out.selected = nil, nil
		}
		if operation == "apply-shorter-offer" {
			nextCandidates, out.selected = candidates, ids
			lifetime, expiry = RouteLifetimeFinite, now.Add(5*time.Minute)
		}
		raw, err = a.exportRouteUpdateWithLifetime(b.PublicKey(), nextCandidates, RouteUpdateVersion, lifetime, expiry, now.Add(time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
	case "approve-remove", "legacy-to-managed":
	case "approve-empty":
		out.selected = nil
	case "approve-mixed":
		out.selected = []string{candidates[0].ID(), candidates[2].ID()}
	case "approve-until-revoked-to-finite", "approve-finite-shorter":
		out.selected, lifetime, expiry = ids, RouteLifetimeFinite, now.Add(5*time.Minute)
	default:
		t.Fatal("unknown reduction fixture", operation)
	}
	if operation == "legacy-to-managed" || out.sequence == 2 {
		out.mutate = func() error {
			return b.applyRouteUpdateWithLifetime(out.peer, raw, routeReviewDigest(raw), out.selected, lifetime, expiry, now.Add(time.Millisecond))
		}
	} else {
		out.mutate = func() error {
			return b.approveRoutesWithLifetime(out.peer, routeReviewDigest(raw), out.selected, lifetime, expiry, now.Add(time.Millisecond))
		}
	}
	return out
}

func routeReductionIDs(approvals []RouteApproval) []string {
	ids := make([]string, 0, len(approvals))
	for _, approval := range approvals {
		ids = append(ids, approval.CandidateID)
	}
	return ids
}

func TestRouteReductionStopsBeforeSaveAndFailsClosed(t *testing.T) {
	operations := []string{"apply-remove", "apply-mixed", "apply-empty", "apply-shorter-offer", "approve-remove", "approve-empty", "approve-mixed", "approve-until-revoked-to-finite", "approve-finite-shorter", "legacy-to-managed", "withdrawal"}
	outcomes := []struct {
		name string
		err  error
	}{{"confirmed", nil}, {"ordinary", os.ErrPermission}, {"busy", config.ErrAtomicBusy}, {"recovery", config.ErrAtomicRecovery}, {"committed", config.ErrAtomicCommitted}}
	for _, operation := range operations {
		for _, outcome := range outcomes {
			t.Run(operation+"/"+outcome.name, func(t *testing.T) {
				f := routeReductionFixture(t, operation)
				b, peer := f.receiver, f.peer
				old := b.clients[peer]
				fake := &fakePeerTransport{}
				old.makeClient = func(tailcat.Addr) peerTransport { return fake }
				flow, err := b.DialPeer(context.Background(), peer, "tcp", 8080)
				if err != nil {
					t.Fatal(err)
				}
				defer flow.Close()
				epoch, _ := b.cfg.Trust.Epoch(peer)
				writes := 0
				b.cfg.Persist = func(trust Snapshot, records []RemotePeer) error {
					writes++
					if !old.closed || !old.retired.Load() || old.runCtx.Err() == nil || fake.closed.Load() != 1 {
						t.Error("save preceded completed retirement")
					}
					interim := routeSnapshot(b.clients[peer].remote, f.now.Add(time.Millisecond))
					if b.clients[peer] == old || interim.Legacy || len(interim.Approvals) != 0 || len(interim.Permitted) != 0 {
						t.Error("intermediate runtime exposed route authority")
					}
					if len(trust.Peers) != 1 || len(records) != 1 || records[0].Routes.ReceivedSequence != f.sequence || !reflect.DeepEqual(routeReductionIDs(records[0].Routes.Approvals), append([]string{}, f.selected...)) {
						t.Error("saved state differs from exact reviewed selection")
					}
					if err := ValidateRouteState(b.cfg.Identity, records[0]); err != nil {
						t.Error("invalid staged authority", err)
					}
					return outcome.err
				}
				err = f.mutate()
				if writes != 1 {
					t.Fatal("wrong save count", writes)
				}
				if _, err := flow.Write([]byte("must remain blocked")); err == nil {
					t.Fatal("retired outgoing flow retained authority")
				}
				if currentEpoch, _ := b.cfg.Trust.Epoch(peer); currentEpoch != epoch {
					t.Fatal("route reduction altered pair trust")
				}
				snapshot, snapshotErr := b.RouteSnapshot(peer)
				if snapshotErr != nil || snapshot.ReceivedSequence != f.sequence || snapshot.Legacy {
					t.Fatal("reduction lost reviewed proof/high-water", snapshotErr)
				}
				if outcome.err == nil {
					if err != nil || snapshot.RecoveryRequired || !reflect.DeepEqual(routeReductionIDs(snapshot.Approvals), append([]string{}, f.selected...)) {
						t.Fatal("confirmed reduction did not install exact authority", err)
					}
					if len(f.selected) != 0 {
						b.clients[peer].makeClient = func(tailcat.Addr) peerTransport { return &fakePeerTransport{} }
						conn, err := b.DialPeer(context.Background(), peer, "tcp", 8080)
						if err != nil {
							t.Fatal("confirmed successor remained blocked", err)
						}
						conn.Close()
					}
					return
				}
				if !errors.Is(err, outcome.err) || !errors.Is(err, config.ErrAtomicRecovery) || !snapshot.RecoveryRequired || len(snapshot.Approvals) != 0 || len(snapshot.Permitted) != 0 {
					t.Fatal("failed reduction retained authority or lost recovery outcome", err)
				}
				if _, err := b.client(context.Background(), peer); !errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("failed reduction allowed reconnect", err)
				}
				if _, err := b.ExportRouteUpdateWithLifetime(peer, f.candidates, RouteLifetimeUntilRevoked, time.Time{}); !errors.Is(err, config.ErrAtomicRecovery) || writes != 1 {
					t.Fatal("failed reduction permitted a later state writer", err)
				}
			})
		}
	}
}

func TestRouteReductionBlockedCloseDeniesMixedGrants(t *testing.T) {
	for _, outcome := range []struct {
		name string
		err  error
	}{{"confirmed", nil}, {"ordinary", os.ErrPermission}} {
		t.Run(outcome.name, func(t *testing.T) {
			f := routeReductionFixture(t, "apply-mixed")
			b, peer := f.receiver, f.peer
			old := b.clients[peer]
			fake := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(fake.allowClose) }) }
			defer release()
			old.makeClient = func(tailcat.Addr) peerTransport { return fake }
			flow, err := b.DialPeer(context.Background(), peer, "tcp", 8080)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			var writes atomic.Int32
			b.cfg.Persist = func(Snapshot, []RemotePeer) error { writes.Add(1); return outcome.err }
			done := make(chan error, 1)
			go func() { done <- f.mutate() }()
			select {
			case <-fake.closeStarted:
			case <-time.After(time.Second):
				t.Fatal("reduction did not start teardown")
			}
			if writes.Load() != 0 || fake.closed.Load() != 0 {
				t.Fatal("save or closure completed before teardown release")
			}
			if _, err := flow.Write([]byte("blocked during close")); err == nil {
				t.Fatal("old authority survived blocked Close")
			}
			snapshot, err := b.RouteSnapshot(peer)
			if err != nil || len(snapshot.Approvals) != 0 || len(snapshot.Permitted) != 0 {
				t.Fatal("mixed replacement granted authority during Close", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			if conn, err := b.DialPeer(ctx, peer, "tcp", 8080); conn != nil || !errors.Is(err, context.DeadlineExceeded) {
				if conn != nil {
					conn.Close()
				}
				t.Fatal("intermediate dial bypassed predecessor", err)
			}
			release()
			select {
			case err := <-done:
				if outcome.err == nil && err != nil || outcome.err != nil && (!errors.Is(err, outcome.err) || !errors.Is(err, config.ErrAtomicRecovery)) {
					t.Fatal("wrong persistence outcome", err)
				}
			case <-time.After(time.Second):
				t.Fatal("mutation did not finish after teardown release")
			}
			if writes.Load() != 1 || fake.closed.Load() != 1 {
				t.Fatal("teardown or save repeated")
			}
		})
	}
}

func TestRouteReductionCloseFailureBlocksPersistenceAndSuccessors(t *testing.T) {
	f := routeReductionFixture(t, "approve-mixed")
	b, peer := f.receiver, f.peer
	closeErr := errors.New("synthetic route close failure")
	fake := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{}), closeErr: closeErr}
	close(fake.allowClose)
	b.clients[peer].makeClient = func(tailcat.Addr) peerTransport { return fake }
	flow, err := b.DialPeer(context.Background(), peer, "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	writes := 0
	b.cfg.Persist = func(Snapshot, []RemotePeer) error { writes++; return nil }
	if err := f.mutate(); !errors.Is(err, closeErr) || !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("failed engine closure did not require recovery", err)
	}
	if writes != 0 || fake.closed.Load() != 1 {
		t.Fatal("failed closure published desired grants or repeated teardown")
	}
	if _, err := b.client(context.Background(), peer); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("recovery did not block successor", err)
	}
	if err := b.clients[peer].waitForPredecessor(context.Background()); !errors.Is(err, closeErr) || !errors.Is(err, ErrRoutePermission) {
		t.Fatal("successor lost failed predecessor", err)
	}
	if _, err := flow.Write([]byte("blocked")); err == nil {
		t.Fatal("failed engine closure kept old flow authorized")
	}
}

func TestRouteGrantOnlyFailurePreservesExistingRuntime(t *testing.T) {
	for _, operation := range []string{"approve-add", "approve-extend", "apply-add", "legacy-preserving-add"} {
		t.Run(operation, func(t *testing.T) {
			a, b, now, candidates := routeNodesFixture(t)
			if operation == "legacy-preserving-add" {
				candidates = append([]RouteCandidate{legacyCandidate(b.cfg.Relay)}, candidates...)
			}
			raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
			if err != nil {
				t.Fatal(err)
			}
			lifetime, expiry := RouteLifetimeUntilRevoked, time.Time{}
			if operation == "approve-extend" {
				lifetime, expiry = RouteLifetimeFinite, now.Add(time.Minute)
			}
			if operation != "legacy-preserving-add" {
				if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), []string{candidates[0].ID()}, lifetime, expiry, now); err != nil {
					t.Fatal(err)
				}
			}
			old := b.clients[a.PublicKey()]
			fake := &fakePeerTransport{}
			old.makeClient = func(tailcat.Addr) peerTransport { return fake }
			flow, err := b.DialPeer(context.Background(), a.PublicKey(), "tcp", 8080)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			before := b.RemoteSnapshot()
			writes := 0
			b.cfg.Persist = func(Snapshot, []RemotePeer) error {
				writes++
				if b.clients[a.PublicKey()] != old || old.retired.Load() || fake.closed.Load() != 0 || !reflect.DeepEqual(before, b.remoteSnapshotLocked()) {
					t.Error("grant-only change disturbed runtime before confirmed save")
				}
				return os.ErrPermission
			}
			selected := []string{candidates[0].ID(), candidates[1].ID()}
			if operation == "approve-extend" {
				selected, expiry = selected[:1], now.Add(2*time.Minute)
			}
			if operation == "apply-add" {
				raw, err = a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now.Add(time.Millisecond))
				if err != nil {
					t.Fatal(err)
				}
			}
			mutate := func() error {
				if operation == "apply-add" || operation == "legacy-preserving-add" {
					return b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), selected, lifetime, expiry, now.Add(time.Millisecond))
				}
				return b.approveRoutesWithLifetime(a.PublicKey(), routeReviewDigest(raw), selected, lifetime, expiry, now.Add(time.Millisecond))
			}
			for range 2 {
				if err := mutate(); !errors.Is(err, os.ErrPermission) || errors.Is(err, config.ErrAtomicRecovery) {
					t.Fatal("ordinary grant-only failure stopped being retryable", err)
				}
			}
			if writes != 2 || b.pairingRecovery || !reflect.DeepEqual(before, b.RemoteSnapshot()) || old.retired.Load() || fake.closed.Load() != 0 {
				t.Fatal("grant-only failure activated or retired authority")
			}
			if _, err := flow.Write([]byte("existing grant remains valid")); err != nil {
				t.Fatal("grant-only save failure interrupted old flow", err)
			}
		})
	}
}

// Keep net.ErrClosed in the shutdown outcome matrix separate from write errors.
func TestRouteReductionConcurrentNodeCloseDoesNotSave(t *testing.T) {
	f := routeReductionFixture(t, "approve-remove")
	b, peer := f.receiver, f.peer
	old := b.clients[peer]
	fake := &handoffCloseTransport{closeStarted: make(chan struct{}), allowClose: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(fake.allowClose) }) }
	defer release()
	old.makeClient = func(tailcat.Addr) peerTransport { return fake }
	flow, err := b.DialPeer(context.Background(), peer, "tcp", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	var writes atomic.Int32
	b.cfg.Persist = func(Snapshot, []RemotePeer) error { writes.Add(1); return nil }
	mutationDone := make(chan error, 1)
	go func() { mutationDone <- f.mutate() }()
	select {
	case <-fake.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("reduction did not reach teardown")
	}
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		stopping := b.closed
		b.mu.Unlock()
		if stopping {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Node.Close did not publish closed state")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	select {
	case err := <-mutationDone:
		if !errors.Is(err, net.ErrClosed) || !errors.Is(err, config.ErrAtomicRecovery) {
			t.Fatal("concurrent close did not retain reduction recovery", err)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation blocked after Close")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Node.Close did not finish")
	}
	if writes.Load() != 0 {
		t.Fatal("desired authority saved after concurrent Node.Close")
	}
}

func TestRouteReductionPublishedUncertaintyReopensExactSavedState(t *testing.T) {
	clearRouteEnvironment(t)
	for _, operation := range []string{"apply-mixed", "approve-until-revoked-to-finite", "withdrawal"} {
		t.Run(operation, func(t *testing.T) {
			f := routeReductionFixture(t, operation)
			b, peer := f.receiver, f.peer
			path := filepath.Join(t.TempDir(), "route-state.json")
			b.cfg.Persist = func(trust Snapshot, records []RemotePeer) error {
				if err := config.WriteJSON(path, atomicPairState{trust, records}); err != nil {
					return err
				}
				return config.ErrAtomicCommitted
			}
			if err := f.mutate(); !errors.Is(err, config.ErrAtomicCommitted) || !errors.Is(err, config.ErrAtomicRecovery) {
				t.Fatal("published uncertainty did not block runtime", err)
			}
			snapshot, err := b.RouteSnapshot(peer)
			if err != nil || !snapshot.RecoveryRequired || len(snapshot.Permitted) != 0 {
				t.Fatal("published unconfirmed grants activated", err)
			}
			var saved atomicPairState
			if err := config.ReadJSON(path, &saved); err != nil {
				t.Fatal(err)
			}
			book := NewBook()
			if err := book.Restore(saved.Trust); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewNode(NodeConfig{Identity: b.cfg.Identity, Relay: b.cfg.Relay, Trust: book, Remotes: saved.Remotes, Persist: func(Snapshot, []RemotePeer) error { return nil }})
			if err != nil {
				t.Fatal("published state could not reopen", err)
			}
			defer reopened.Close()
			after, err := reopened.RouteSnapshot(peer)
			if err != nil || after.RecoveryRequired || after.ReceivedSequence != f.sequence || after.Legacy || !reflect.DeepEqual(reopened.RemoteSnapshot(), saved.Remotes) || !reflect.DeepEqual(routeReductionIDs(after.Approvals), append([]string{}, f.selected...)) {
				t.Fatal("reopen did not reconcile exact published state", err)
			}
			if operation == "approve-until-revoked-to-finite" && (!after.NextExpiry.Equal(f.now.Add(5*time.Minute)) || len(after.Approvals) != 2 || after.Approvals[0].EffectiveLifetime() != RouteLifetimeFinite) {
				t.Fatal("reopen changed explicit finite deadline")
			}
			if _, err := reopened.InspectRouteUpdate(peer, saved.Remotes[0].Routes.ReceivedProof); !errors.Is(err, ErrRouteUpdate) {
				t.Fatal("reopen lost received replay barrier", err)
			}
			if r := reopened.clients[peer]; r.prepared || r.started || r.client != nil {
				t.Fatal("reconciliation started transport without an application dial")
			}
		})
	}
}

func TestRouteReductionComparesEachCandidateDeadline(t *testing.T) {
	f := routeReductionFixture(t, "approve-finite-shorter")
	before := f.receiver.RemoteSnapshot()[0]
	before.Routes.Approvals[0].Expires = f.now.Add(2 * time.Minute)
	after := CloneRemotePeers([]RemotePeer{before})[0]
	after.Routes.Approvals[1].Expires = f.now.Add(5 * time.Minute)
	if !routeSnapshot(before, f.now).NextExpiry.Equal(routeSnapshot(after, f.now).NextExpiry) {
		t.Fatal("fixture changed earliest aggregate deadline")
	}
	if !reducesRouteAuthority(before, after, f.receiver.cfg.Relay, f.now) {
		t.Fatal("shortening a later candidate deadline was hidden by the earlier candidate")
	}
	if reducesRouteAuthority(after, before, f.receiver.cfg.Relay, f.now) {
		t.Fatal("extending a later candidate deadline was treated as a reduction")
	}
}

func TestRouteWithdrawalWithoutActiveAuthorityStillLatches(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), nil, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	raw, err = a.exportRouteUpdateWithLifetime(b.PublicKey(), nil, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	old := b.clients[a.PublicKey()]
	writes := 0
	b.cfg.Persist = func(Snapshot, []RemotePeer) error {
		writes++
		if !old.closed || !old.retired.Load() {
			t.Error("withdrawal persistence preceded runtime retirement")
		}
		return os.ErrPermission
	}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), nil, RouteLifetimeUntilRevoked, time.Time{}, now.Add(time.Millisecond)); !errors.Is(err, os.ErrPermission) || !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("withdrawal with no active approvals lost recovery signal", err)
	}
	snapshot, err := b.RouteSnapshot(a.PublicKey())
	if err != nil || !snapshot.RecoveryRequired || snapshot.ReceivedSequence != 2 || len(snapshot.Permitted) != 0 || writes != 1 {
		t.Fatal("failed empty withdrawal lost fail-closed state", err)
	}
}
