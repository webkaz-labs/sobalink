package lanlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"tailscale.com/types/key"
)

func routeNodesFixture(t *testing.T) (*Node, *Node, time.Time, []RouteCandidate) {
	t.Helper()
	a, b := testNode(), testNode()
	ar, br := key.NewNode(), key.NewNode()
	ab := RemotePeer{Peer: Peer{b.PublicKey(), "peer-b"}, Address: b.Address(), ClientPrivate: ar, IncomingClientKey: keyString(br.Public())}
	ba := RemotePeer{Peer: Peer{a.PublicKey(), "peer-a"}, Address: a.Address(), ClientPrivate: br, IncomingClientKey: keyString(ar.Public())}
	if err := a.commitPair(context.Background(), ab, "", registeredPairAttemptFixture(t, a, b.PublicKey())); err != nil {
		t.Fatal(err)
	}
	if err := b.commitPair(context.Background(), ba, "", registeredPairAttemptFixture(t, b, a.PublicKey())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, time.Now().UTC().Add(-time.Second), []RouteCandidate{routeFixture("external", "192.0.2.20:443"), routeFixture("local", "192.168.50.2:54446")}
}

func routeExportReview(t *testing.T, a, b *Node, candidates []RouteCandidate, now time.Time) ([]byte, RouteReview) {
	t.Helper()
	raw, err := a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := b.inspectRouteUpdate(a.PublicKey(), raw, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, review
}

func routeApplyFixture(t *testing.T, a, b *Node, candidates []RouteCandidate, now time.Time) ([]byte, RouteReview) {
	t.Helper()
	raw, review := routeExportReview(t, a, b, candidates, now)
	var ids []string
	for _, c := range candidates {
		ids = append(ids, c.ID())
	}
	if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, ids, now.Add(30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	return raw, review
}

func clearRouteEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
}

func TestRouteStateReviewApplyAndIndependentExpiry(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	before := b.RemoteSnapshot()
	epoch, _ := b.cfg.Trust.Epoch(a.PublicKey())
	writes := 0
	b.cfg.Persist = func(Snapshot, []RemotePeer) error { writes++; return nil }
	raw, review := routeExportReview(t, a, b, candidates, now)
	if writes != 0 || !reflect.DeepEqual(before, b.RemoteSnapshot()) {
		t.Fatal("inspection mutated state")
	}
	if review.Update.Sequence != 1 || review.Update.PairBinding == "" || review.Digest == "" {
		t.Fatal("incomplete review")
	}
	if snapshot, _ := a.RouteSnapshot(b.PublicKey()); !snapshot.Legacy || snapshot.IssuedSequence != 1 {
		t.Fatal("export changed local outgoing permission")
	}
	if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, nil, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	s := routeSnapshot(b.RemoteSnapshot()[0], now)
	if s.Legacy || len(s.Permitted) != 0 || s.ReceivedSequence != 1 || writes != 1 {
		t.Fatal("authenticated offer granted permission")
	}
	if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute), now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("incoming replay granted permission", err)
	}
	if err := b.approveRoutes(a.PublicKey(), review.Digest, []string{candidates[1].ID()}, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	s = routeSnapshot(b.RemoteSnapshot()[0], now)
	if len(s.Permitted) != 1 || s.Permitted[0] != candidates[1] || !s.NextExpiry.Equal(now.Add(time.Minute)) {
		t.Fatal("exact independent permission missing")
	}
	if got := routeSnapshot(b.RemoteSnapshot()[0], now.Add(time.Minute)); len(got.Permitted) != 0 || got.ReceivedSequence != 1 {
		t.Fatal("expired local approval survived or reset replay protection")
	}
	if after, _ := b.cfg.Trust.Epoch(a.PublicKey()); after != epoch || !reflect.DeepEqual(b.RemoteSnapshot()[0].ClientPrivate, before[0].ClientPrivate) || b.RemoteSnapshot()[0].IncomingClientKey != before[0].IncomingClientKey || b.RemoteSnapshot()[0].Address != before[0].Address {
		t.Fatal("route change changed pair or application trust")
	}
}

func TestRouteStateRejectsUnreviewedAndUnselectedChanges(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	raw, review := routeExportReview(t, a, b, candidates, now)
	before := b.RemoteSnapshot()
	writes := 0
	b.cfg.Persist = func(Snapshot, []RemotePeer) error { writes++; return nil }
	for name, run := range map[string]func() error{
		"wrong-review": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, strings.Repeat("f", 64), nil, time.Time{}, now)
		},
		"not-offered": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{strings.Repeat("f", 64)}, now.Add(time.Minute), now)
		},
		"duplicate": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID(), candidates[0].ID()}, now.Add(time.Minute), now)
		},
		"forever": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, time.Time{}, now)
		},
		"expired-approval": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, now, now)
		},
		"long-approval": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, now.Add(MaxRouteUpdateLifetime+time.Second), now)
		},
		"expired-proof": func() error {
			return b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, nil, time.Time{}, now.Add(time.Hour))
		},
		"unpaired": func() error {
			return b.applyRouteUpdate(GenerateIdentity().PublicKey(), raw, review.Digest, nil, time.Time{}, now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if run() == nil || writes != 0 || !reflect.DeepEqual(before, b.RemoteSnapshot()) {
				t.Fatal("invalid apply saved or changed state")
			}
		})
	}
}

func TestRouteStateWithdrawalRevocationAndReapprovalAreSticky(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	_, review := routeApplyFixture(t, a, b, candidates, now)
	if err := b.RevokeRoutes(a.PublicKey(), []string{candidates[0].ID()}); err != nil {
		t.Fatal(err)
	}
	if got := routeSnapshot(b.RemoteSnapshot()[0], now); len(got.Permitted) != 1 || got.Permitted[0] != candidates[1] {
		t.Fatal("local revocation did not remove exact candidate")
	}
	if err := b.approveRoutes(a.PublicKey(), review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute), now); err != nil {
		t.Fatal("explicit current-offer reapproval failed", err)
	}
	withdrawal, review := routeExportReview(t, a, b, nil, now)
	if err := b.applyRouteUpdate(a.PublicKey(), withdrawal, review.Digest, nil, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if got := routeSnapshot(b.RemoteSnapshot()[0], now); got.Legacy || len(got.Permitted) != 0 || len(got.Approvals) != 0 || got.ReceivedSequence != 2 {
		t.Fatal("withdrawal reverted to legacy or left approvals")
	}
	raw, review := routeExportReview(t, a, b, candidates, now)
	if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, nil, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if got := routeSnapshot(b.RemoteSnapshot()[0], now); len(got.Permitted) != 0 || got.ReceivedSequence != 3 {
		t.Fatal("reintroduced candidate revived old permission")
	}
}

func TestRouteStatePrepublicationFailureDoesNotActivate(t *testing.T) {
	for _, operation := range []string{"export", "apply", "approve"} {
		t.Run(operation, func(t *testing.T) {
			a, b, now, candidates := routeNodesFixture(t)
			raw, review := routeExportReview(t, a, b, candidates, now)
			if operation != "export" {
				// Begin with managed empty authority: moving from a legacy
				// anchor to a different endpoint is a reduction, not a grant.
				if err := b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, nil, time.Time{}, now); err != nil {
					t.Fatal(err)
				}
				if operation == "apply" {
					raw, review = routeExportReview(t, a, b, candidates, now)
				}
			}
			n := b
			if operation == "export" {
				n = a
			}
			before := n.RemoteSnapshot()
			n.cfg.Persist = func(Snapshot, []RemotePeer) error { return os.ErrPermission }
			var err error
			switch operation {
			case "export":
				var frame []byte
				frame, err = a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now)
				if len(frame) != 0 {
					t.Fatal("unsaved sequence published")
				}
			case "apply":
				err = b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute), now)
			case "approve":
				err = b.approveRoutes(a.PublicKey(), review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute), now)
			}
			if !errors.Is(err, os.ErrPermission) || n.pairingRecovery || !reflect.DeepEqual(before, n.RemoteSnapshot()) {
				t.Fatal("failed save activated or froze retry", err)
			}
		})
	}
}

func TestRouteStateRevokeStopsOutgoingEvenWhenSaveFails(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, config.ErrAtomicCommitted} {
		t.Run(failure.Error(), func(t *testing.T) {
			a, b, now, candidates := routeNodesFixture(t)
			routeApplyFixture(t, a, b, candidates, now)
			old := b.clients[a.PublicKey()]
			old.runCtx, old.cancel = context.WithCancel(context.Background())
			epoch, _ := b.cfg.Trust.Epoch(a.PublicKey())
			b.cfg.Persist = func(s Snapshot, records []RemotePeer) error {
				if !old.closed || old.runCtx.Err() == nil || len(records[0].Routes.Approvals) != 0 || len(s.Peers) != 1 {
					t.Fatal("revoke saved before stopping outgoing or changed trust")
				}
				return failure
			}
			if err := b.RevokeRoutes(a.PublicKey(), nil); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if got := routeSnapshot(b.RemoteSnapshot()[0], now); len(got.Permitted) != 0 || got.Legacy || got.ReceivedSequence != 1 {
				t.Fatal("failed revoke revived route")
			}
			if got, _ := b.cfg.Trust.Epoch(a.PublicKey()); got != epoch {
				t.Fatal("route revoke changed application trust")
			}
			if b.pairingRecovery != errors.Is(failure, config.ErrAtomicCommitted) {
				t.Fatal("wrong uncertainty latch")
			}
		})
	}
}

func TestRouteStateConcurrentUncertainSaveFreezesWriterAndReopens(t *testing.T) {
	clearRouteEnvironment(t)
	a, b, now, candidates := routeNodesFixture(t)
	raw, review := routeExportReview(t, a, b, candidates, now)
	path := filepath.Join(t.TempDir(), "state.json")
	entered, proceed := make(chan struct{}), make(chan struct{})
	writes := 0
	b.cfg.Persist = func(s Snapshot, records []RemotePeer) error {
		writes++
		close(entered)
		<-proceed
		if err := config.WriteJSON(path, atomicPairState{s, records}); err != nil {
			return err
		}
		return fmt.Errorf("directory sync: %w", config.ErrAtomicCommitted)
	}
	first, queued := make(chan error, 1), make(chan error, 1)
	go func() {
		first <- b.applyRouteUpdate(a.PublicKey(), raw, review.Digest, []string{candidates[0].ID()}, now.Add(time.Minute), now)
	}()
	<-entered
	go func() {
		_, err := b.exportRouteUpdate(a.PublicKey(), candidates, now.Add(time.Hour), now)
		queued <- err
	}()
	close(proceed)
	for i, result := range []chan error{first, queued} {
		want := config.ErrAtomicCommitted
		if i == 1 {
			want = config.ErrAtomicRecovery
		}
		select {
		case err := <-result:
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("route writer deadlocked")
		}
	}
	if writes != 1 || len(routeSnapshot(b.RemoteSnapshot()[0], now).Permitted) != 0 || !b.pairingRecovery {
		t.Fatal("uncertain update activated or overwritten")
	}
	if _, err := b.client(context.Background(), a.PublicKey()); !errors.Is(err, config.ErrAtomicRecovery) {
		t.Fatal("recovery allowed reconnect", err)
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
		t.Fatal("reopen with original identity rejected", err)
	}
	defer reopened.Close()
	if reopened.pairingRecovery || !reflect.DeepEqual(reopened.RemoteSnapshot(), saved.Remotes) {
		t.Fatal("reopen did not use durable state")
	}
	if got := routeSnapshot(reopened.RemoteSnapshot()[0], now); len(got.Permitted) != 1 || got.ReceivedSequence != 1 {
		t.Fatal("durable route not restored")
	}
	if _, err := reopened.inspectRouteUpdate(a.PublicKey(), raw, now); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("reopen lost replay barrier", err)
	}
	for _, r := range reopened.clients {
		if r.client != nil || r.started {
			t.Fatal("reopen started network work")
		}
	}
}

func TestRouteStateConcurrentExportsDoNotReuseSequence(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	var wg sync.WaitGroup
	sequences := make(chan uint64, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			raw, err := a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now)
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
	if len(seen) != 12 || a.RemoteSnapshot()[0].Routes.IssuedSequence != 12 {
		t.Fatal("issued high-water mismatch")
	}
}

func TestRouteStateProofHighWaterCloningAndPairBinding(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	raw, _ := routeApplyFixture(t, a, b, candidates, now)
	original := b.RemoteSnapshot()[0]
	if err := ValidateRouteState(b.cfg.Identity, original); err != nil {
		t.Fatal(err)
	}
	if got := routeSnapshot(original, now.Add(2*time.Hour)); len(got.Permitted) != 0 || got.ReceivedSequence != 1 {
		t.Fatal("expired offer reset high-water")
	}
	cloned := CloneRemotePeers([]RemotePeer{original})[0]
	cloned.Routes.Received.Candidates[0].Scope = "bad"
	cloned.Routes.ReceivedProof[0] ^= 1
	cloned.Routes.Approvals[0].CandidateID = "bad"
	if got := b.RemoteSnapshot()[0]; !reflect.DeepEqual(got, original) || !bytes.Equal(got.Routes.ReceivedProof, raw) {
		t.Fatal("mutable snapshot alias")
	}
	for name, mutate := range map[string]func(*RemotePeer){
		"version":       func(r *RemotePeer) { r.Routes.Version++ },
		"high-water":    func(r *RemotePeer) { r.Routes.ReceivedSequence++ },
		"missing-proof": func(r *RemotePeer) { r.Routes.ReceivedProof = nil },
		"changed-proof": func(r *RemotePeer) {
			// Flip an authenticated ciphertext byte, not a base64 character:
			// unused trailing base64 bits may decode to the same ciphertext.
			var envelope pairEnvelope
			_ = json.Unmarshal(r.Routes.ReceivedProof, &envelope)
			envelope.Box[len(envelope.Box)-1] ^= 1
			r.Routes.ReceivedProof, _ = json.Marshal(envelope)
		},
		"changed-current":     func(r *RemotePeer) { r.Routes.Received.Candidates[0].Relay.CertificateSHA256 = strings.Repeat("b", 64) },
		"unknown-approval":    func(r *RemotePeer) { r.Routes.Approvals[0].CandidateID = strings.Repeat("f", 64) },
		"duplicate-approval":  func(r *RemotePeer) { r.Routes.Approvals = append(r.Routes.Approvals, r.Routes.Approvals[0]) },
		"indefinite-approval": func(r *RemotePeer) { r.Routes.Approvals[0].Expires = time.Time{} },
		"beyond-offer":        func(r *RemotePeer) { r.Routes.Approvals[0].Expires = r.Routes.Received.Expires.Add(time.Second) },
		"new-pair":            func(r *RemotePeer) { r.ClientPrivate = key.NewNode() },
	} {
		t.Run(name, func(t *testing.T) {
			changed := CloneRemotePeers([]RemotePeer{original})[0]
			mutate(&changed)
			if err := ValidateRouteState(b.cfg.Identity, changed); err == nil {
				t.Fatal("malformed saved route accepted")
			}
		})
	}
	if err := ValidateRouteState(GenerateIdentity(), original); err == nil {
		t.Fatal("new local identity accepted old proof")
	}
	if err := b.commitPair(context.Background(), original, "", nil); !errors.Is(err, ErrRouteUpdate) {
		t.Fatal("re-pair accepted inherited route grants", err)
	}
}

func TestRouteStateExpiredAndWithdrawnProofReopensWithoutReplay(t *testing.T) {
	clearRouteEnvironment(t)
	for _, withdraw := range []bool{false, true} {
		t.Run(fmt.Sprint("withdrawn=", withdraw), func(t *testing.T) {
			a, b, now, candidates := routeNodesFixture(t)
			if withdraw {
				candidates = nil
			}
			raw, _ := routeApplyFixture(t, a, b, candidates, now)
			remotes := b.RemoteSnapshot()
			if err := ValidateRouteState(b.cfg.Identity, remotes[0]); err != nil {
				t.Fatal(err)
			}
			book := NewBook()
			if err := book.Restore(b.cfg.Trust.Snapshot()); err != nil {
				t.Fatal(err)
			}
			n, err := NewNode(NodeConfig{Identity: b.cfg.Identity, Relay: b.cfg.Relay, Trust: book, Remotes: remotes, Persist: func(Snapshot, []RemotePeer) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			future := now.Add(2 * time.Hour)
			if got := routeSnapshot(n.RemoteSnapshot()[0], future); got.ReceivedSequence != 1 || got.Legacy || len(got.Permitted) != 0 {
				t.Fatal("expired/withdrawn offer lost replay barrier")
			}
			if _, err := n.inspectRouteUpdate(a.PublicKey(), raw, future); !errors.Is(err, ErrRouteUpdate) {
				t.Fatal("old proof admitted", err)
			}
			if _, err := currentRouteReview(n.cfg.Identity, n.RemoteSnapshot()[0], future); !errors.Is(err, ErrRouteUpdate) {
				t.Fatal("expired proof made locally approvable", err)
			}
			remotes[0].Routes.ReceivedSequence = 9
			remotes[0].Routes.ReceivedProof[0] ^= 1
			if n.RemoteSnapshot()[0].Routes.ReceivedSequence != 1 {
				t.Fatal("NewNode retained caller-owned state")
			}
		})
	}
}

func TestRouteStateUnpublishedExportFailureAllowsQueuedRetry(t *testing.T) {
	a, b, now, candidates := routeNodesFixture(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	writes := 0
	a.cfg.Persist = func(_ Snapshot, records []RemotePeer) error {
		writes++
		if records[0].Routes.IssuedSequence != 1 {
			t.Fatal("failed sequence advanced")
		}
		if writes == 1 {
			close(entered)
			<-proceed
			return os.ErrPermission
		}
		return nil
	}
	type result struct {
		raw []byte
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() {
		raw, err := a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now)
		first <- result{raw, err}
	}()
	<-entered
	go func() {
		raw, err := a.exportRouteUpdate(b.PublicKey(), candidates, now.Add(time.Hour), now)
		second <- result{raw, err}
	}()
	close(proceed)
	select {
	case got := <-first:
		if len(got.raw) != 0 || !errors.Is(got.err, os.ErrPermission) {
			t.Fatal("unsaved export escaped", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first export deadlocked")
	}
	select {
	case got := <-second:
		if got.err != nil {
			t.Fatal(got.err)
		}
		u, err := OpenRouteUpdate(b.cfg.Identity, b.RemoteSnapshot()[0], got.raw, 0, now)
		if err != nil || u.Sequence != 1 {
			t.Fatal("queued retry reused or skipped unpublished sequence", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued export deadlocked")
	}
	if writes != 2 || a.pairingRecovery {
		t.Fatal("retry unexpectedly frozen")
	}
}

func TestRouteStateQueuedApplyChecksFreshTimeAfterLock(t *testing.T) {
	a, b, _, candidates := routeNodesFixture(t)
	now := time.Now().UTC()
	expires := now.Add(100 * time.Millisecond)
	raw, err := a.exportRouteUpdate(b.PublicKey(), candidates, expires, now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := b.inspectRouteUpdate(a.PublicKey(), raw, now)
	if err != nil {
		t.Fatal(err)
	}
	b.pairMu.Lock()
	done := make(chan error, 1)
	go func() { done <- b.ApplyRouteUpdate(a.PublicKey(), raw, review.Digest, nil, time.Time{}) }()
	time.Sleep(time.Until(expires) + 20*time.Millisecond)
	b.pairMu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRouteUpdate) {
			t.Fatal("queue admitted expired proof", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued apply deadlocked")
	}
	if b.RemoteSnapshot()[0].Routes != nil {
		t.Fatal("expired queued apply mutated high-water")
	}
}
