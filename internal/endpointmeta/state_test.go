package endpointmeta

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const modelBudget = 128 << 10

// saveTemporaryModel exercises only a disposable snapshot's save/reopen
// consistency. It is not a production persistence owner or a crash test.
// Use the existing platform-specific writer and preserve uncertain publication.
func saveTemporaryModel(path string, b []byte) (published bool, err error) {
	err = config.AtomicWritePrivate(path, b)
	return err == nil || errors.Is(err, config.ErrAtomicCommitted), err
}

func modelFixture(t *testing.T) (Snapshot, Envelope, ed25519.PrivateKey) {
	t.Helper()
	p, e, key := fixture(t)
	state, err := InitialState(p, p.JoinerKey)
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Version: 3, Revision: "1", LocalPeer: PeerWire{p.JoinerKey, "", p.JoinerEndpoint, p.JoinerTunnelKey}, LocalScope: p.JoinerScope, PreviousLocalEndpoint: p.JoinerEndpoint, ObservedAt: testNow().Format(time.RFC3339Nano), Peers: []PeerRecord{{Peer: PeerWire{p.HostKey, "", p.HostEndpoint, p.HostTunnelKey}, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s, e, key
}

func approvalFor(t *testing.T, e Envelope, now time.Time) Approval {
	return Approval{Kind: "exact", ProofDigest: mustDigest(t, e), Endpoint: e.Update.Endpoint, Granted: now.UTC().Format(time.RFC3339Nano), Lifetime: e.Update.Lifetime, Expires: e.Update.Expires}
}

func signSequence(t *testing.T, e Envelope, key ed25519.PrivateKey, n int, withdraw bool) Envelope {
	t.Helper()
	e.Update.Sequence = strconv.Itoa(n)
	if withdraw {
		e.Update.Operation = "withdraw"
		e.Update.PriorEndpoint = e.Update.Endpoint
		e.Update.Endpoint = ""
	}
	out, err := Sign(e.Update, key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stage(t *testing.T, s Snapshot, m Mutation, now time.Time) Snapshot {
	t.Helper()
	review, err := PreviewMutation(s, m)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	fenced, err := Fence(s, m, review, id, now, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return fenced
}

func finish(t *testing.T, s Snapshot, m Mutation, now time.Time) Snapshot {
	t.Helper()
	out, err := FinishPending(stage(t, s, m, now), false, now, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func accept(t *testing.T, s Snapshot, e Envelope, now time.Time) Snapshot {
	t.Helper()
	a := approvalFor(t, e, now)
	m, outcome, err := ProposeReceive(s, e.Update.Issuer, e, &a, now)
	if err != nil || outcome != "candidate" {
		t.Fatal("propose", outcome, err)
	}
	return finish(t, s, m, now)
}

func TestInspectionGapsAndDuplicateEvidence(t *testing.T) {
	s, e, key := modelFixture(t)
	before := modelDigest(s)
	if err := Inspect(e, *s.Peers[0].PairContext, s.LocalPeer.Key, testNow()); err != nil {
		t.Fatal(err)
	}
	_, outcome, err := ProposeReceive(s, e.Update.Issuer, e, nil, testNow())
	if err != nil || outcome != "review_required" || modelDigest(s) != before {
		t.Fatal("inspection changed authority", outcome, err)
	}
	s = accept(t, s, signSequence(t, e, key, 3, false), testNow())
	s = accept(t, s, e, testNow())
	state := s.Peers[0].EndpointState
	if state.ReceivedHighwater != "7" || !SavedEligibility(s, state.PairBinding, testNow()) {
		t.Fatal("gap was not accepted")
	}
	before = modelDigest(s)
	_, outcome, err = ProposeReceive(s, e.Update.Issuer, e, nil, testNow().Add(48*time.Hour))
	if err != nil || outcome != "already_applied" || before != modelDigest(s) {
		t.Fatal("expired duplicate was not an inert no-op", outcome, err)
	}
	if _, _, err = ProposeReceive(s, e.Update.Issuer, signSequence(t, e, key, 6, false), nil, testNow()); err != ErrStale {
		t.Fatal("old sequence", err)
	}
	altered := e.Update
	altered.Endpoint = "127.0.0.4:20004"
	conflict, _ := Sign(altered, key)
	if _, _, err = ProposeReceive(s, e.Update.Issuer, conflict, nil, testNow()); err != ErrConflict {
		t.Fatal("conflicting sequence", err)
	}
	if before != modelDigest(s) {
		t.Fatal("rejected input changed snapshot")
	}
}

func TestWithdrawWithoutFollowAndInitialRevoke(t *testing.T) {
	s, e, key := modelFixture(t)
	binding := s.Peers[0].EndpointState.PairBinding
	reduction, err := ProposeReduction(s, binding, "revoke", testNow())
	if err != nil || reduction.Changed || reduction.State != "initial" || reduction.Reason != "no_managed_endpoint_approval" {
		t.Fatal("original pairing mislabeled", reduction, err)
	}
	w := signSequence(t, e, key, 8, true)
	m, outcome, err := ProposeReceive(s, e.Update.Issuer, w, nil, testNow())
	if err != nil || outcome != "candidate" {
		t.Fatal("withdrawal wrongly needs follow", outcome, err)
	}
	fenced := stage(t, s, m, testNow())
	if _, err := FinishPending(fenced, true, testNow(), modelBudget); err != ErrReview {
		t.Fatal("cancelled withdrawal", err)
	}
	s, err = FinishPending(fenced, false, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	state := s.Peers[0].EndpointState
	if state.ReceiveStatus != "withdrawn" || state.ReceivedHighwater != "8" || state.Approval != nil || SavedEligibility(s, binding, testNow()) {
		t.Fatal("withdrawal authority")
	}
	_, outcome, err = ProposeReceive(s, e.Update.Issuer, signSequence(t, e, key, 9, false), nil, testNow())
	if err != nil || outcome != "review_required" {
		t.Fatal("withdrawal fabricated follow", outcome, err)
	}
	if _, err := ProposeReapproval(s, binding, approvalFor(t, e, testNow()), testNow()); err != ErrReview {
		t.Fatal("reapproved withdrawn proof")
	}
}

func TestCancellationReapprovalAndExpiry(t *testing.T) {
	s, e, _ := modelFixture(t)
	binding := s.Peers[0].EndpointState.PairBinding
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	if s.Peers[0].EndpointState.ReceivedHighwater != "0" {
		t.Fatal("proposal mutated state")
	}
	s, err = FinishPending(stage(t, s, m, testNow()), true, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if s.Peers[0].EndpointState.ReceiveStatus != "cancelled" || s.Peers[0].EndpointState.ReceivedHighwater != "7" || SavedEligibility(s, binding, testNow()) {
		t.Fatal("cancelled state lost evidence")
	}
	_, outcome, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil || outcome != "already_applied" {
		t.Fatal("duplicate used exact approval", outcome, err)
	}
	m, err = ProposeReapproval(s, binding, a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, m, testNow())
	if !SavedEligibility(s, binding, testNow()) {
		t.Fatal("local current-proof review failed")
	}
	red, err := ProposeReduction(s, binding, "revoke", testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, red.Mutation, testNow())
	if s.Peers[0].EndpointState.ReceiveStatus != "locally_revoked" || s.Peers[0].EndpointState.ReceivedHighwater != "7" {
		t.Fatal("revoke reset proof")
	}
	if _, err := ProposeReapproval(s, binding, a, testNow().Add(48*time.Hour)); err != ErrReview {
		t.Fatal("reapproved expired proof")
	}
	s, e, _ = modelFixture(t)
	a = approvalFor(t, e, testNow())
	a.Expires = testNow().Add(time.Minute).Format(time.RFC3339Nano)
	m, _, err = ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s, err = FinishPending(stage(t, s, m, testNow()), false, testNow().Add(2*time.Minute), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if s.Peers[0].EndpointState.ReceiveStatus != "expired" || s.Peers[0].EndpointState.ReceivedHighwater != "7" {
		t.Fatal("late save revived authority")
	}
	if s.ValidateAt(testNow()) != ErrReview {
		t.Fatal("observed clock rollback accepted")
	}
}

func TestFollowLifetimeAndDisable(t *testing.T) {
	s, e, _ := modelFixture(t)
	binding := s.Peers[0].EndpointState.PairBinding
	scope, _ := s.LocalScope.Digest()
	f := FollowApproval{ScopeDigest: scope, Revision: "1", Granted: testNow().Format(time.RFC3339Nano), Lifetime: "finite", Expires: testNow().Add(time.Hour).Format(time.RFC3339Nano), Active: true}
	m, err := ProposeFollow(s, binding, f, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, m, testNow())
	m, outcome, err := ProposeReceive(s, e.Update.Issuer, e, nil, testNow())
	if err != nil || outcome != "candidate" {
		t.Fatal("follow", outcome, err)
	}
	s = finish(t, s, m, testNow())
	if s.Peers[0].EndpointState.Follow.Expires != f.Expires || !SavedEligibility(s, binding, testNow()) {
		t.Fatal("follow deadline changed")
	}
	red, err := ProposeReduction(s, binding, "disable-follow", testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, red.Mutation, testNow())
	if s.Peers[0].EndpointState.Follow.Active || s.Peers[0].EndpointState.Approval != nil || s.Peers[0].EndpointState.ReceiveStatus != "locally_revoked" {
		t.Fatal("disable manufactured exact approval")
	}
	if _, outcome, err = ProposeReceive(s, e.Update.Issuer, e, nil, testNow()); err != nil || outcome != "already_applied" {
		t.Fatal("duplicate follow reset", err)
	}
}

func TestSyntheticMigrationAndPrivateDTOSplit(t *testing.T) {
	s, _, _ := modelFixture(t)
	old := LegacySnapshot{2, s.LocalPeer, s.LocalScope, []PeerWire{s.Peers[0].Peer}}
	next, err := MigrateLegacy(old, testNow())
	if err != nil {
		t.Fatal(err)
	}
	if next.LocalPeer != old.LocalPeer || next.Peers[0].Peer != old.Peers[0] || next.Peers[0].EndpointState != nil || next.Peers[0].PairContext != nil || next.Peers[0].ContextConfirmed {
		t.Fatal("migration changed identity or fabricated authority")
	}
	old.LocalScope.Prefixes[0] = "10.0.0.0/8"
	if next.LocalScope.Prefixes[0] != "127.0.0.0/8" {
		t.Fatal("migration retained mutable alias")
	}
	public, err := Encode(next.Peers[0].Peer)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"proof", "pair_context", "signature", "follow", "nonce"} {
		if bytes.Contains(public, []byte(word)) {
			t.Fatal("private state in public DTO")
		}
	}
	b, err := EncodeSnapshot(next, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSnapshot(bytes.Replace(b, []byte(`"version":3`), []byte(`"version":2`), 1), modelBudget); err == nil {
		t.Fatal("accepted downgrade")
	}
	if _, err := ParseSnapshot(append(b, '\n'), modelBudget); err == nil {
		t.Fatal("accepted alternate saved encoding")
	}
}

func TestFenceBindingAndTemporaryStoreConsistency(t *testing.T) {
	s, e, _ := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	review, err := PreviewMutation(s, m)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, err := Fence(s, m, strings.Repeat("0", 64), id, testNow(), modelBudget); err != ErrReview {
		t.Fatal("stale review", err)
	}
	if _, err := Fence(s, m, review, id, testNow(), 10); err != ErrCapacity {
		t.Fatal("budget ignored", err)
	}
	fenced := stage(t, s, m, testNow())
	if SavedEligibility(fenced, m.PairBinding, testNow()) {
		t.Fatal("pending model eligible")
	}
	if _, _, err := ProposeReceive(fenced, e.Update.Issuer, e, &a, testNow()); err != ErrRecovery {
		t.Fatal("writer ignored fence")
	}
	path := filepath.Join(t.TempDir(), "model.json")
	b, err := EncodeSnapshot(fenced, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveTemporaryModel(path, b); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := ParseSnapshot(b, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.PendingChange == nil || modelDigest(reopened) != modelDigest(fenced) {
		t.Fatal("reopen erased fence")
	}
	final, err := FinishPending(reopened, false, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	b, err = EncodeSnapshot(final, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveTemporaryModel(path, b); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err = ParseSnapshot(b, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.PendingChange != nil || reopened.Peers[0].EndpointState.ReceivedHighwater != "7" {
		t.Fatal("final snapshot lost proof")
	}
	bad := cloneSnapshot(fenced)
	bad.PendingChange.BaseRevision = "2"
	if bad.Validate() == nil {
		t.Fatal("corrupt fence accepted")
	}
	bad = cloneSnapshot(final)
	bad.Peers[0].EndpointState.ReceivedHighwater = "8"
	if bad.Validate() == nil {
		t.Fatal("proof/highwater mismatch accepted")
	}
	for _, published := range []bool{false, true} {
		out := ResolveSave(s, fenced, published, errors.New("synthetic save failure"))
		if !out.Recovery || out.Durable || out.Published != published {
			t.Fatal("save outcome")
		}
		want := s
		if published {
			want = fenced
		}
		if modelDigest(out.Snapshot) != modelDigest(want) {
			t.Fatal("publication outcome misrepresented")
		}
	}
}

func TestExportSavesBeforeReturningAndNeverLeaksOnError(t *testing.T) {
	receiver, e, key := modelFixture(t)
	p := *receiver.Peers[0].PairContext
	state, _ := InitialState(p, p.HostKey)
	s := Snapshot{Version: 3, Revision: "1", LocalPeer: receiver.Peers[0].Peer, LocalScope: p.HostScope, PreviousLocalEndpoint: p.HostEndpoint, ObservedAt: testNow().Format(time.RFC3339Nano), Peers: []PeerRecord{{Peer: receiver.LocalPeer, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state}}}
	u := e.Update
	u.Endpoint = s.LocalPeer.Endpoint
	u.PriorEndpoint = s.PreviousLocalEndpoint
	u.Sequence = "1"
	for _, published := range []bool{false, true} {
		out, b, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, u.Recipient, u, key, testNow(), modelBudget, func([]byte) (bool, error) { return published, errors.New("synthetic failure") })
		if err != ErrRecovery || len(b) != 0 || !out.Recovery || out.Durable || out.Published != published {
			t.Fatal("failed export returned bytes", err)
		}
		if _, err := ReexportModel(out, u.Recipient); err != ErrRecovery {
			t.Fatal("recovery latch bypassed")
		}
	}
	path := filepath.Join(t.TempDir(), "export.json")
	called := false
	out, wire, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, u.Recipient, u, key, testNow(), modelBudget, func(b []byte) (bool, error) { called = true; return saveTemporaryModel(path, b) })
	if err != nil || !called || len(wire) == 0 || !out.Durable || !out.Published || out.Recovery {
		t.Fatal("export", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := ParseSnapshot(b, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Peers[0].EndpointState.IssuedHighwater != "1" {
		t.Fatal("sequence not saved")
	}
	repeated, err := ReexportModel(SaveResolution{Snapshot: saved, Durable: true}, u.Recipient)
	if err != nil || !bytes.Equal(wire, repeated) {
		t.Fatal("not exact re-export", err)
	}
	if _, err := nextCounter("18446744073709551615"); err != ErrCapacity {
		t.Fatal("wrapped counter")
	}
}

func TestExactAndFollowExpiryAreIndependent(t *testing.T) {
	for _, exactFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(exactFirst), func(t *testing.T) {
			s, e, key := modelFixture(t)
			binding := s.Peers[0].EndpointState.PairBinding
			scope, _ := s.LocalScope.Digest()
			followDeadline, exactDeadline := testNow().Add(time.Hour), testNow().Add(time.Minute)
			if !exactFirst {
				followDeadline, exactDeadline = exactDeadline, followDeadline
			}
			f := FollowApproval{ScopeDigest: scope, Revision: "1", Granted: testNow().Format(time.RFC3339Nano), Lifetime: "finite", Expires: followDeadline.Format(time.RFC3339Nano), Active: true}
			m, err := ProposeFollow(s, binding, f, testNow())
			if err != nil {
				t.Fatal(err)
			}
			s = finish(t, s, m, testNow())
			a := approvalFor(t, e, testNow())
			a.Expires = exactDeadline.Format(time.RFC3339Nano)
			m, _, err = ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
			if err != nil {
				t.Fatal(err)
			}
			s = finish(t, s, m, testNow())
			now := testNow().Add(2 * time.Minute)
			red, err := ProposeReduction(s, binding, "expire", now)
			if err != nil || !red.Changed {
				t.Fatal("expiry not proposed", err)
			}
			s = finish(t, s, red.Mutation, now)
			state := s.Peers[0].EndpointState
			if state.ReceivedHighwater != "7" || state.Follow.Expires != f.Expires || state.Follow.Active != exactFirst {
				t.Fatal("expiry changed independent consent or evidence")
			}
			if exactFirst {
				if state.Approval != nil || state.ReceiveStatus != "expired" {
					t.Fatal("expired exact approval remains eligible")
				}
				next := signSequence(t, e, key, 8, false)
				_, outcome, err := ProposeReceive(s, next.Update.Issuer, next, nil, now)
				if err != nil || outcome != "candidate" {
					t.Fatal("independent follow consent was lost", outcome, err)
				}
			} else if state.Approval == nil || *state.Approval != a || !SavedEligibility(s, binding, now) {
				t.Fatal("follow expiry changed exact approval")
			}
		})
	}
}

func TestWithdrawalRetainsIndependentFollowConsent(t *testing.T) {
	s, e, key := modelFixture(t)
	binding := s.Peers[0].EndpointState.PairBinding
	scope, _ := s.LocalScope.Digest()
	f := FollowApproval{ScopeDigest: scope, Revision: "1", Granted: testNow().Format(time.RFC3339Nano), Lifetime: "finite", Expires: testNow().Add(time.Hour).Format(time.RFC3339Nano), Active: true}
	m, err := ProposeFollow(s, binding, f, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, m, testNow())
	w := signSequence(t, e, key, 8, true)
	m, _, err = ProposeReceive(s, w.Update.Issuer, w, nil, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s = finish(t, s, m, testNow())
	if *s.Peers[0].EndpointState.Follow != f {
		t.Fatal("withdrawal changed independent follow consent")
	}
	newSet := signSequence(t, e, key, 9, false)
	m, outcome, err := ProposeReceive(s, newSet.Update.Issuer, newSet, nil, testNow())
	if err != nil || outcome != "candidate" {
		t.Fatal("fresh set with current consent", outcome, err)
	}
	s = finish(t, s, m, testNow())
	if *s.Peers[0].EndpointState.Follow != f || !SavedEligibility(s, binding, testNow()) {
		t.Fatal("fresh set changed follow deadline")
	}
}

func TestFenceRechecksReducerAndClock(t *testing.T) {
	s, e, _ := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	s.Peers[0].ContextConfirmed = false
	review, err := PreviewMutation(s, m)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, err := Fence(s, m, review, id, testNow(), modelBudget); err != ErrReview {
		t.Fatal("constructed mutation bypassed context confirmation", err)
	}
	s.Peers[0].ContextConfirmed = true
	fenced := stage(t, s, m, testNow().Add(time.Minute))
	if fenced.ValidateAt(testNow()) != ErrReview {
		t.Fatal("clock rollback before fence was accepted")
	}
	m.State.ReceivedHighwater = "99"
	if fenced.Validate() != nil || fenced.PendingChange.Mutation.State.ReceivedHighwater != "7" {
		t.Fatal("caller mutation aliases a fenced snapshot")
	}
	s = accept(t, s, e, testNow())
	red, err := ProposeReduction(s, s.Peers[0].EndpointState.PairBinding, "expire", testNow().Add(48*time.Hour))
	if err != nil || !red.Changed {
		t.Fatal("expiry proposal", err)
	}
	review, err = PreviewMutation(s, red.Mutation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fence(s, red.Mutation, review, id, testNow(), modelBudget); err != ErrReview {
		t.Fatal("constructed expiry bypassed current-time check", err)
	}
	bad := cloneSnapshot(s)
	bad.Peers[0].EndpointState.AuthorityRevision = "0"
	if bad.Validate() == nil {
		t.Fatal("managed evidence accepted with initial authority revision")
	}
}

func TestSnapshotBudgetPreflightAndExactSize(t *testing.T) {
	s, e, _ := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	fenced := stage(t, s, m, testNow())
	final, err := FinishPending(fenced, false, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []Snapshot{s, fenced, final} {
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		w := wireSizer{left: modelBudget}
		if !snapshot.measure(&w) || modelBudget-w.left != len(raw) {
			t.Fatal("snapshot size differs from encoding")
		}
		if _, err := EncodeSnapshot(snapshot, len(raw)); err != nil {
			t.Fatal("exact model budget", err)
		}
		if _, err := EncodeSnapshot(snapshot, len(raw)-1); err != ErrCapacity {
			t.Fatal("over model budget", err)
		}
	}
	s.Peers = make([]PeerRecord, modelBudget+1)
	if allocs := testing.AllocsPerRun(20, func() {
		if _, err := EncodeSnapshot(s, modelBudget); err != ErrCapacity {
			t.Fatal("oversized model", err)
		}
	}); allocs != 0 {
		t.Fatal("oversized model allocated before budget rejection", allocs)
	}
}
