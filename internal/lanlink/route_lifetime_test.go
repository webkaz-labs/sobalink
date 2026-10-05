package lanlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/types/key"
)

func persistentRouteFixture(t *testing.T) (*Node, *Node, time.Time, []RouteCandidate, []byte, RouteReview) {
	t.Helper()
	a, b, now, candidates := routeNodesFixture(t)
	raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := b.inspectRouteUpdate(a.PublicKey(), raw, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{candidates[0].ID(), candidates[1].ID()}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, review.Digest, ids, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	return a, b, now, candidates, raw, review
}

func TestRouteV2LifetimeRequiresExplicitValidMode(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	candidate := routeFixture("external", "192.0.2.20:443")
	for _, lifetime := range []string{RouteLifetimeFinite, RouteLifetimeUntilRevoked} {
		expires := time.Time{}
		if lifetime == RouteLifetimeFinite {
			expires = now.Add(400 * 24 * time.Hour)
		}
		raw, err := SealRouteUpdateWithLifetime(a, ab, 1, []RouteCandidate{candidate}, lifetime, now, expires)
		if err != nil {
			t.Fatal(err)
		}
		u, err := OpenRouteUpdate(b, ba, raw, 0, now.Add(31*24*time.Hour))
		if err != nil || u.Version != RouteUpdateVersion || u.Lifetime != lifetime {
			t.Fatal("v2 lifetime not preserved", err)
		}
		if got, err := PermittedRoutes(u, nil, now.Add(31*24*time.Hour)); err != nil || len(got) != 0 {
			t.Fatal("authentication granted authority", err)
		}
		if got, err := PermittedRoutes(u, []string{candidate.ID()}, now.Add(31*24*time.Hour)); err != nil || len(got) != 1 {
			t.Fatal("explicit lifetime lost", err)
		}
		changed := ba
		changed.ClientPrivate = key.NewNode()
		if _, err := OpenRouteUpdate(b, changed, raw, 0, now); err == nil {
			t.Fatal("v2 proof survived re-pair")
		}
		if _, err := OpenRouteUpdate(a, ab, raw, 0, now); err == nil {
			t.Fatal("reflected v2 proof accepted")
		}
		if _, err := OpenRouteUpdate(b, ba, raw, 1, now); err == nil {
			t.Fatal("v2 replay accepted")
		}
	}
	for _, tc := range []struct {
		mode   string
		expiry time.Time
	}{
		{"", time.Time{}}, {"", now.Add(time.Hour)}, {"forever", time.Time{}},
		{RouteLifetimeFinite, time.Time{}}, {RouteLifetimeFinite, now},
		{RouteLifetimeFinite, now.AddDate(500, 0, 0)},
		{RouteLifetimeUntilRevoked, now.Add(time.Hour)},
	} {
		if _, err := SealRouteUpdateWithLifetime(a, ab, 1, []RouteCandidate{candidate}, tc.mode, now, tc.expiry); !errors.Is(err, ErrRouteUpdate) {
			t.Fatal("invalid lifetime accepted", tc.mode, err)
		}
	}
}

func TestRouteV1CanonicalProofAndFiniteRulesRemainUnchanged(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	candidates := []RouteCandidate{routeFixture("local", "192.168.50.2:54446")}
	raw, err := SealRouteUpdate(a, ab, 1, candidates, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var u RouteUpdate
	plain, err := openMessage(b, raw, a.PublicKey(), &u)
	if err != nil {
		t.Fatal(err)
	}
	// Original field order and absence of the new lifetime remain signed bytes.
	type originalWire struct {
		Version     int              `json:"version"`
		Domain      string           `json:"domain"`
		Issuer      string           `json:"issuer"`
		Recipient   string           `json:"recipient"`
		PairBinding string           `json:"pair_binding"`
		Sequence    uint64           `json:"sequence"`
		Issued      time.Time        `json:"issued"`
		Expires     time.Time        `json:"expires"`
		Candidates  []RouteCandidate `json:"candidates"`
	}
	original, _ := json.Marshal(originalWire{1, legacyRouteUpdateDomain, u.Issuer, u.Recipient, u.PairBinding, 1, now, now.Add(time.Hour), candidates})
	if !bytes.Equal(plain, original) || u.Lifetime != "" || u.EffectiveLifetime() != RouteLifetimeFinite {
		t.Fatal("v1 canonical contract changed")
	}
	if _, err := OpenRouteUpdate(b, ba, raw, 0, now.Add(time.Hour)); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("expired v1 revived", err)
	}
	if _, err := SealRouteUpdate(a, ab, 2, candidates, now, now.Add(31*24*time.Hour)); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("v1 lifetime widened", err)
	}
	u.Expires = time.Time{}
	u.Lifetime = RouteLifetimeUntilRevoked
	forged, _, _ := sealMessage(a, b.PublicKey(), u)
	if _, err := OpenRouteUpdate(b, ba, forged, 0, now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("v1 accepted permanent semantics", err)
	}
}

func TestPersistentRouteStateReopensWithoutRenewalOrTimer(t *testing.T) {
	clearRouteEnvironment(t)
	a, b, now, candidates, raw, _ := persistentRouteFixture(t)
	before := b.RemoteSnapshot()[0]
	if err := ValidateRouteState(b.cfg.Identity, before); err != nil {
		t.Fatal(err)
	}
	future := now.Add(400 * 24 * time.Hour)
	s := routeSnapshot(before, future)
	if s.Legacy || s.Lifetime != RouteLifetimeUntilRevoked || !s.Expires.IsZero() || !s.NextExpiry.IsZero() || len(s.Permitted) != 2 || s.ReceivedSequence != 1 {
		t.Fatal("permanent approval expired or lost replay evidence")
	}
	book := NewBook()
	if err := book.Restore(b.cfg.Trust.Snapshot()); err != nil {
		t.Fatal(err)
	}
	writes := 0
	n, err := NewNode(NodeConfig{Identity: b.cfg.Identity, Relay: b.cfg.Relay, Trust: book, Remotes: b.RemoteSnapshot(), Persist: func(Snapshot, []RemotePeer) error { writes++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	r, err := n.client(context.Background(), a.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if r.expiryTimer != nil || !r.expires.IsZero() || r.client != nil || writes != 0 || len(r.candidates) != len(candidates) {
		t.Fatal("reopen renewed, timed out, or started permanent route")
	}
	if after := n.RemoteSnapshot()[0]; !reflect.DeepEqual(before, after) || !bytes.Equal(after.Routes.ReceivedProof, raw) {
		t.Fatal("reopen changed identity, approval or proof")
	}
	if _, err := n.inspectRouteUpdate(a.PublicKey(), raw, future); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("permanent replay accepted after reopen", err)
	}
}

func TestPersistentRouteMixedFiniteApprovalExpiry(t *testing.T) {
	_, b, now, candidates, _, _ := persistentRouteFixture(t)
	remote := b.RemoteSnapshot()[0]
	remote.Routes.Approvals[0].Lifetime = RouteLifetimeFinite
	remote.Routes.Approvals[0].Expires = now.Add(time.Minute)
	if err := ValidateRouteState(b.cfg.Identity, remote); err != nil {
		t.Fatal(err)
	}
	before := routeSnapshot(remote, now)
	if len(before.Permitted) != 2 || !before.NextExpiry.Equal(now.Add(time.Minute)) {
		t.Fatal("finite deadline lost beside permanent approval")
	}
	after := routeSnapshot(remote, now.Add(time.Minute))
	if len(after.Permitted) != 1 || after.Permitted[0] != candidates[1] || !after.NextExpiry.IsZero() {
		t.Fatal("finite expiry revived or removed permanent route")
	}
	if _, err := selectedApprovalsWithLifetime(*remote.Routes.Received, []string{candidates[0].ID()}, RouteLifetimeFinite, now.Add(400*24*time.Hour), now); err != nil {
		t.Fatal("arbitrary finite policy cap remains", err)
	}
	for _, tc := range []struct {
		mode   string
		expiry time.Time
	}{{"", time.Time{}}, {RouteLifetimeFinite, time.Time{}}, {RouteLifetimeUntilRevoked, now.Add(time.Hour)}, {RouteLifetimeFinite, now.AddDate(500, 0, 0)}} {
		if _, err := selectedApprovalsWithLifetime(*remote.Routes.Received, []string{candidates[0].ID()}, tc.mode, tc.expiry, now); err == nil {
			t.Fatal("invalid local lifetime accepted")
		}
	}
}

func TestRouteStateV1MigrationNeverPromotesFiniteApprovals(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	raw, review := routeApplyFixture(t, a, b, candidates, now)
	remote := b.RemoteSnapshot()[0]
	remote.Routes.Version = 1
	remote.Routes.IssuedVersion = 0
	for i := range remote.Routes.Approvals {
		remote.Routes.Approvals[i].Lifetime = ""
	}
	if err := ValidateRouteState(b.cfg.Identity, remote); err != nil {
		t.Fatal("old state rejected", err)
	}
	original := CloneRemotePeers([]RemotePeer{remote})[0]
	upgraded, err := newRouteState(b.cfg.Identity, remote)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Version != RouteStateVersion || !reflect.DeepEqual(remote, original) || !bytes.Equal(upgraded.ReceivedProof, raw) {
		t.Fatal("migration changed caller or original proof")
	}
	for i, approval := range upgraded.Approvals {
		if approval.Lifetime != RouteLifetimeFinite || !approval.Expires.Equal(original.Routes.Approvals[i].Expires) || !approval.Granted.Equal(original.Routes.Approvals[i].Granted) {
			t.Fatal("finite approval widened")
		}
	}
	remote.Routes = upgraded
	if err := ValidateRouteState(b.cfg.Identity, remote); err != nil {
		t.Fatal(err)
	}
	if s := routeSnapshot(remote, now.Add(2*time.Hour)); s.Legacy || len(s.Permitted) != 0 || s.ReceivedSequence != 1 {
		t.Fatal("expired legacy authorization revived")
	}
	if err := b.approveRoutesWithLifetime(a.PublicKey(), review.Digest, []string{candidates[0].ID()}, RouteLifetimeUntilRevoked, time.Time{}, now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("finite issuer offer silently promoted", err)
	}
	if err := b.approveRoutesWithLifetime(a.PublicKey(), review.Digest, []string{candidates[0].ID()}, RouteLifetimeFinite, now.Add(3*time.Hour), now.Add(2*time.Hour)); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("expired offer reapproved", err)
	}
}

func TestPersistentRouteMonotonicVersionsAndStickyRevocation(t *testing.T) {
	a, b, now, candidates, first, review := persistentRouteFixture(t)
	if _, err := a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("export protocol downgraded", err)
	}
	if a.RemoteSnapshot()[0].Routes.IssuedSequence != 1 {
		t.Fatal("rejected downgrade consumed sequence")
	}
	if err := b.RevokeRoutes(a.PublicKey(), nil); err != nil {
		t.Fatal(err)
	}
	if s := routeSnapshot(b.RemoteSnapshot()[0], now.Add(400*24*time.Hour)); len(s.Permitted) != 0 || s.Legacy {
		t.Fatal("revoked permanent grant revived")
	}
	if _, err := b.inspectRouteUpdate(a.PublicKey(), first, now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("replay bypassed revoke", err)
	}
	if err := b.approveRoutesWithLifetime(a.PublicKey(), review.Digest, []string{candidates[0].ID()}, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
		t.Fatal("explicit reapproval failed", err)
	}
	withdrawal, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), nil, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), withdrawal, routeReviewDigest(withdrawal), nil, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if s := routeSnapshot(b.RemoteSnapshot()[0], now.Add(400*24*time.Hour)); len(s.Permitted) != 0 || s.Legacy || s.ReceivedSequence != 2 {
		t.Fatal("withdrawal lost permanent tombstone")
	}
	lower, err := SealRouteUpdate(a.cfg.Identity, a.RemoteSnapshot()[0], 99, candidates, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.inspectRouteUpdate(a.PublicKey(), lower, now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("higher sequence downgraded accepted protocol", err)
	}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), lower, routeReviewDigest(lower), []string{candidates[0].ID()}, RouteLifetimeFinite, now.Add(time.Minute), now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("apply accepted protocol downgrade", err)
	}
	fresh, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), fresh, routeReviewDigest(fresh), nil, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if s := routeSnapshot(b.RemoteSnapshot()[0], now); len(s.Permitted) != 0 || s.ReceivedSequence != 3 {
		t.Fatal("new offer restored withdrawn approvals")
	}
}

func TestPermanentRouteWithdrawalFailureStopsBeforePersistence(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, config.ErrAtomicCommitted} {
		t.Run(failure.Error(), func(t *testing.T) {
			a, b, now, _, _, _ := persistentRouteFixture(t)
			old := b.clients[a.PublicKey()]
			fake := &fakePeerTransport{}
			old.makeClient = func(tailcat.Addr) peerTransport { return fake }
			flow, err := b.DialPeer(context.Background(), a.PublicKey(), "tcp", 8080)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			withdrawal, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), nil, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			b.cfg.Persist = func(_ Snapshot, records []RemotePeer) error {
				writes++
				if !old.retired.Load() || fake.closed.Load() != 1 || len(records[0].Routes.Approvals) != 0 || records[0].Routes.ReceivedSequence != 2 {
					t.Fatal("withdrawal saved before stopping old authority")
				}
				return failure
			}
			if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), withdrawal, routeReviewDigest(withdrawal), nil, RouteLifetimeUntilRevoked, time.Time{}, now); !errors.Is(err, failure) || !errors.Is(err, config.ErrAtomicRecovery) {
				t.Fatal("withdrawal failure did not preserve recovery signal", err)
			}
			if _, err := flow.Write([]byte("blocked")); err == nil {
				t.Fatal("old flow survived failed withdrawal")
			}
			if _, err := b.client(context.Background(), a.PublicKey()); !errors.Is(err, config.ErrAtomicRecovery) {
				t.Fatal("failed withdrawal allowed reconnect", err)
			}
			if _, err := b.ExportRouteUpdateWithLifetime(a.PublicKey(), nil, RouteLifetimeUntilRevoked, time.Time{}); !errors.Is(err, config.ErrAtomicRecovery) {
				t.Fatal("failed withdrawal permitted later writer", err)
			}
			s, err := b.RouteSnapshot(a.PublicKey())
			if err != nil || !s.RecoveryRequired || len(s.Permitted) != 0 || s.ReceivedSequence != 2 || writes != 1 {
				t.Fatal("failed withdrawal restored prior state", err)
			}
		})
	}
}

func TestPersistentRouteStateRejectsMalformedLifetimes(t *testing.T) {
	_, b, _, _, _, _ := persistentRouteFixture(t)
	original := b.RemoteSnapshot()[0]
	for name, mutate := range map[string]func(*RouteState){
		"state-downgrade":        func(s *RouteState) { s.Version = 1 },
		"missing-local-mode":     func(s *RouteState) { s.Approvals[0].Lifetime = "" },
		"permanent-with-expiry":  func(s *RouteState) { s.Approvals[0].Expires = time.Now().Add(time.Hour) },
		"finite-without-expiry":  func(s *RouteState) { s.Approvals[0].Lifetime = RouteLifetimeFinite },
		"missing-offer-mode":     func(s *RouteState) { s.Received.Lifetime = "" },
		"future-issued-version":  func(s *RouteState) { s.IssuedSequence = 1; s.IssuedVersion = 3 },
		"missing-issued-version": func(s *RouteState) { s.IssuedSequence = 1; s.IssuedVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			remote := CloneRemotePeers([]RemotePeer{original})[0]
			mutate(remote.Routes)
			if err := ValidateRouteState(b.cfg.Identity, remote); err == nil {
				t.Fatal("malformed lifetime state accepted")
			}
		})
	}
}

func TestPermanentRouteExportsRemainAtomicAndMonotonic(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	before := a.RemoteSnapshot()
	a.cfg.Persist = func(Snapshot, []RemotePeer) error { return os.ErrPermission }
	if raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now); len(raw) != 0 || !errors.Is(err, os.ErrPermission) || !reflect.DeepEqual(before, a.RemoteSnapshot()) {
		t.Fatal("unsaved permanent offer published", err)
	}
	a.cfg.Persist = func(Snapshot, []RemotePeer) error { return nil }
	var wg sync.WaitGroup
	sequences := make(chan uint64, 8)
	for range 8 {
		wg.Go(func() {
			raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
			if err != nil {
				t.Error(err)
				return
			}
			u, err := OpenRouteUpdate(b.cfg.Identity, b.RemoteSnapshot()[0], raw, 0, now)
			if err != nil {
				t.Error(err)
				return
			}
			sequences <- u.Sequence
		})
	}
	wg.Wait()
	close(sequences)
	seen := map[uint64]bool{}
	for sequence := range sequences {
		if seen[sequence] {
			t.Fatal("sequence reused")
		}
		seen[sequence] = true
	}
	if len(seen) != 8 || a.RemoteSnapshot()[0].Routes.IssuedVersion != RouteUpdateVersion {
		t.Fatal("permanent sequence/version not committed")
	}
}

func TestPermanentRouteGrantRequiresConfirmedCommit(t *testing.T) {
	for _, operation := range []string{"apply", "approve"} {
		for _, failure := range []error{os.ErrPermission, config.ErrAtomicCommitted} {
			t.Run(operation+"/"+failure.Error(), func(t *testing.T) {
				a, b, now, candidates := routeNodesFixture(t)
				raw, err := a.exportRouteUpdateWithLifetime(b.PublicKey(), candidates, RouteUpdateVersion, RouteLifetimeUntilRevoked, time.Time{}, now)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "approve" {
					if err := b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), nil, RouteLifetimeUntilRevoked, time.Time{}, now); err != nil {
						t.Fatal(err)
					}
				}
				before := b.RemoteSnapshot()
				writes := 0
				b.cfg.Persist = func(_ Snapshot, records []RemotePeer) error {
					writes++
					if !reflect.DeepEqual(before, b.remoteSnapshotLocked()) {
						t.Fatal("permanent grant activated before commit")
					}
					if len(records[0].Routes.Approvals) != 1 || records[0].Routes.Approvals[0].Lifetime != RouteLifetimeUntilRevoked {
						t.Fatal("permanent grant not staged exactly")
					}
					return failure
				}
				ids := []string{candidates[0].ID()}
				if operation == "apply" {
					err = b.applyRouteUpdateWithLifetime(a.PublicKey(), raw, routeReviewDigest(raw), ids, RouteLifetimeUntilRevoked, time.Time{}, now)
				} else {
					err = b.approveRoutesWithLifetime(a.PublicKey(), routeReviewDigest(raw), ids, RouteLifetimeUntilRevoked, time.Time{}, now)
				}
				if !errors.Is(err, failure) || writes != 1 || !reflect.DeepEqual(before, b.RemoteSnapshot()) || b.pairingRecovery != errors.Is(failure, config.ErrAtomicCommitted) {
					t.Fatal("failed permanent grant activated or lost recovery state", err)
				}
			})
		}
	}
}

func TestPermanentRouteRuntimeRetainsRemainingGrantAfterFiniteExpiry(t *testing.T) {
	a, b, _, _, _, _ := persistentRouteFixture(t)
	b.mu.Lock()
	remote := b.clients[a.PublicKey()]
	remainingID := remote.remote.Routes.Approvals[1].CandidateID
	remote.remote.Routes.Approvals[0].Lifetime = RouteLifetimeFinite
	remote.remote.Routes.Approvals[0].Expires = time.Now().Add(50 * time.Millisecond)
	b.mu.Unlock()
	old, err := b.client(context.Background(), a.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if old.expiryTimer == nil {
		t.Fatal("finite approval did not establish an expiry timer")
	}
	deadline := time.Now().Add(time.Second)
	for !old.retired.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !old.retired.Load() {
		t.Fatal("finite approval did not retire its runtime")
	}
	fresh, err := b.client(context.Background(), a.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old || len(fresh.candidates) != 1 || fresh.candidates[0].ID() != remainingID || !fresh.expires.IsZero() || fresh.expiryTimer != nil {
		t.Fatal("remaining permanent approval acquired an expiry or revived expired permission")
	}
}
