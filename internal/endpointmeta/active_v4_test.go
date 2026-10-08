package endpointmeta

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These tests use only fabricated model values and in-memory callbacks. They do
// not construct transports, touch files, or establish production activation.
func sameActiveSnapshot(t *testing.T, v3, v4 Snapshot) {
	t.Helper()
	if v3.Version != SnapshotVersionV3 || v4.Version != SnapshotVersionV4 {
		t.Fatal("operation changed the input schema")
	}
	v4.Version = SnapshotVersionV3
	if !reflect.DeepEqual(v3, v4) {
		t.Fatal("active v4 operation differs from its v3 counterpart")
	}
}

func TestActiveV4EndpointReducerParity(t *testing.T) {
	initial, proof, _ := pairModelFixture(t)
	v4 := cloneSnapshot(initial)
	v4.Version = SnapshotVersionV4
	accepted := []Snapshot{accept(t, initial, proof, testNow()), accept(t, v4, proof, testNow())}
	sameActiveSnapshot(t, accepted[0], accepted[1])
	binding := initial.Peers[0].EndpointState.PairBinding
	for _, s := range accepted {
		if !SavedEligibility(s, binding, testNow()) {
			t.Fatal("active received proof lost eligibility")
		}
		if _, outcome, err := ProposeReceive(s, proof.Update.Issuer, proof, nil, testNow().Add(2*time.Hour)); err != nil || outcome != "already_applied" {
			t.Fatal("duplicate evidence renewed or rejected", outcome, err)
		}
	}
	for _, kind := range []string{"revoke", "expire", "disable-follow"} {
		var results [2]Snapshot
		for i, s := range accepted {
			now := testNow()
			if kind == "expire" {
				now = now.Add(2 * time.Hour)
			}
			r, err := ProposeReduction(s, binding, kind, now)
			if err != nil {
				t.Fatal(kind, err)
			}
			results[i] = s
			if r.Changed {
				results[i] = finish(t, s, r.Mutation, now)
			}
		}
		sameActiveSnapshot(t, results[0], results[1])
	}
	var reapproved, followed [2]Snapshot
	for i, s := range accepted {
		m, err := ProposeReapproval(s, binding, approvalFor(t, proof, testNow()), testNow())
		if err != nil {
			t.Fatal(err)
		}
		reapproved[i] = finish(t, s, m, testNow())
		revision, _ := nextCounter(s.Peers[0].EndpointState.AuthorityRevision)
		scope, _ := s.LocalScope.Digest()
		m, err = ProposeFollow(s, binding, FollowApproval{ScopeDigest: scope, Revision: revision, Granted: s.ObservedAt, Lifetime: "until-revoked", Active: true}, testNow())
		if err != nil {
			t.Fatal(err)
		}
		followed[i] = finish(t, s, m, testNow())
	}
	sameActiveSnapshot(t, reapproved[0], reapproved[1])
	sameActiveSnapshot(t, followed[0], followed[1])
}

func TestActiveV4ContextReducerParity(t *testing.T) {
	f := pairContextFixture(t)
	var confirmed [2]Snapshot
	for i, version := range []int{SnapshotVersionV3, SnapshotVersionV4} {
		s := cloneSnapshot(f.legacy)
		s.Version = version
		peer := s.Peers[0].Peer.Key
		r, err := PrepareContextUpgrade(s, peer, "1", f.window.OwnNonce, f.window.Deadline.Format(time.RFC3339Nano), testNow(), modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		s = r.Snapshot
		review, err := ReviewContextResume(s, peer, f.window.Deadline.Format(time.RFC3339Nano), testNow())
		if err != nil {
			t.Fatal(err)
		}
		r, err = ResumePreparedContext(s, review, testNow(), modelBudget)
		if err != nil || r.Changed || !reflect.DeepEqual(r.Snapshot, s) {
			t.Fatal("exact resume changed evidence", err)
		}
		r, err = RecordInboundContextPrepare(s, peer, f.request, f.window, testNow(), modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		s = r.Snapshot
		pair := *s.Peers[0].UpgradePending.Context
		binding, _ := pair.Binding()
		request := expectedPrepare(s, s.Peers[0], true)
		reply := PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairBinding: binding, PairContext: pair}
		r, err = RecordOutboundContextPrepare(s, peer, request, reply, f.window, testNow(), modelBudget)
		if err != nil || r.Changed || !reflect.DeepEqual(s, r.Snapshot) {
			t.Fatal("outbound duplicate changed context", err)
		}
		r, err = CommitPreparedContext(s, peer, binding, f.window, testNow(), modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		requestCommit, replyCommit := contextCommitEvidence(r.Snapshot)
		r, err = ConfirmContextCommit(r.Snapshot, peer, requestCommit, replyCommit, testNow(), modelBudget)
		if err != nil || !r.Snapshot.Peers[0].ContextConfirmed {
			t.Fatal("confirmation failed", err)
		}
		confirmed[i] = r.Snapshot
	}
	sameActiveSnapshot(t, confirmed[0], confirmed[1])
	initial, proof, _ := pairModelFixture(t)
	for _, version := range []int{SnapshotVersionV3, SnapshotVersionV4} {
		initial.Version = version
		r, err := ConfirmContextFromUpdate(initial, proof.Update.Issuer, proof, testNow(), modelBudget)
		if err != nil || r.Changed || !reflect.DeepEqual(r.Snapshot, initial) {
			t.Fatal("confirmed update duplicate changed evidence", err)
		}
	}
}

func TestActiveV4ExportParityAndTerminalDenial(t *testing.T) {
	initial, _, _ := pairModelFixture(t)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	var saved [2]Snapshot
	var wires [2][]byte
	for i, version := range []int{SnapshotVersionV3, SnapshotVersionV4} {
		s := cloneSnapshot(initial)
		s.Version = version
		peer := s.Peers[0].Peer.Key
		body, err := PrepareExport(s, peer, ExportOptions{Operation: "withdraw", Lifetime: "until-revoked"}, testNow(), modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := Sign(body, key)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := ProposeIssued(s, peer, envelope, testNow(), modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		result, wire, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, peer, body, key, testNow(), modelBudget, func(b []byte) (bool, error) {
			calls++
			parsed, err := ParseSnapshot(b, modelBudget)
			if err != nil || !reflect.DeepEqual(parsed, candidate) {
				t.Fatal("save callback candidate mismatch", err)
			}
			return true, nil
		})
		if err != nil || calls != 1 || !result.Durable {
			t.Fatal("export did not use the save boundary", err)
		}
		again, err := ReexportModel(result, peer)
		if err != nil || !bytes.Equal(again, wire) {
			t.Fatal("retained export mismatch", err)
		}
		saved[i], wires[i] = result.Snapshot, wire
	}
	sameActiveSnapshot(t, saved[0], saved[1])
	if !bytes.Equal(wires[0], wires[1]) {
		t.Fatal("schema altered signed endpoint bytes")
	}
	terminal, _, err := RevokeManagedPairV4(saved[1], saved[1].Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if wire, err := ReexportModel(SaveResolution{Snapshot: terminal, Durable: true}, terminal.Peers[0].Peer.Key); err == nil || len(wire) != 0 {
		t.Fatal("terminal history reexported retained issued bytes")
	}
}

func TestV4GlobalFencePreservesTerminalPreparation(t *testing.T) {
	f := pairContextFixture(t)
	s := migratePairFixture(t, committedContext(t, f))
	s, _, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	allTerminal := stage(t, s, Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.8:30008"}, testNow())
	if len(allTerminal.PendingChange.PairBindings) != 0 {
		t.Fatal("all-terminal fence gained an affected binding")
	}
	allTerminal, err = FinishPending(allTerminal, false, testNow(), modelBudget)
	if err != nil || !reflect.DeepEqual(allTerminal.Peers, s.Peers) {
		t.Fatal("all-terminal reconciliation changed retained evidence", err)
	}
	// Add an unrelated active context with distinct fabricated identities. Its
	// full record must remain intact while the terminal transcript is retained.
	otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{43}, ed25519.SeedSize))
	p := *s.Peers[0].PairContext
	if p.HostKey == s.LocalPeer.Key {
		p.JoinerKey = hex.EncodeToString(otherKey.Public().(ed25519.PublicKey))
		p.JoinerTunnelKey, p.JoinerEndpoint = strings.Repeat("33", 32), "127.0.0.4:20004"
	} else {
		p.HostKey = hex.EncodeToString(otherKey.Public().(ed25519.PublicKey))
		p.HostTunnelKey, p.HostEndpoint = strings.Repeat("33", 32), "127.0.0.4:20004"
	}
	state, err := InitialState(p, s.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	remote, _, _ := pairSides(p, s.LocalPeer.Key)
	s.Peers = append(s.Peers, PeerRecord{Peer: remote, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state})
	before := cloneSnapshot(s)
	scopeDigest, _ := s.LocalScope.Digest()
	follow, err := ProposeFollow(s, state.PairBinding, FollowApproval{ScopeDigest: scopeDigest, Revision: "1", Granted: s.ObservedAt, Lifetime: "until-revoked", Active: true}, testNow())
	if err != nil {
		t.Fatal(err)
	}
	changed := finish(t, s, follow, testNow())
	if !reflect.DeepEqual(changed.Peers[0], s.Peers[0]) || changed.Version != SnapshotVersionV4 {
		t.Fatal("targeted active mutation changed unrelated terminal record")
	}
	m := Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.9:30009"}
	fenced := stage(t, s, m, testNow())
	if !reflect.DeepEqual(fenced.PendingChange.PairBindings, []string{state.PairBinding}) {
		t.Fatal("fence includes terminal binding or omits active binding")
	}
	finished, err := FinishPending(fenced, false, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	terminalBefore, _ := json.Marshal(before.Peers[0])
	terminalAfter, _ := json.Marshal(finished.Peers[0])
	if finished.Version != SnapshotVersionV4 || !bytes.Equal(terminalBefore, terminalAfter) || !reflect.DeepEqual(before, s) || !reflect.DeepEqual(before.Peers[1], finished.Peers[1]) {
		t.Fatal("global endpoint mutation changed retained record values")
	}
	wire, err := EncodeSnapshot(finished, modelBudget)
	if err != nil || !fitsFinish(fenced, false, testNow(), len(wire)) || fitsFinish(fenced, false, testNow(), len(wire)-1) {
		t.Fatal("finish budget omits retained terminal preparation", err)
	}
	fenceWire, _ := EncodeSnapshot(fenced, modelBudget)
	if !fitsFence(s, m, fenced.PendingChange.ReviewDigest, fenced.PendingChange.TransactionID, testNow(), len(fenceWire)) || fitsFence(s, m, fenced.PendingChange.ReviewDigest, fenced.PendingChange.TransactionID, testNow(), len(fenceWire)-1) {
		t.Fatal("fence budget differs from active binding subset")
	}
	parsed, err := ParseSnapshot(wire, modelBudget)
	if err != nil || !reflect.DeepEqual(parsed, finished) {
		t.Fatal("terminal historical preparation no longer round trips", err)
	}
	bad := cloneSnapshot(fenced)
	bad.PendingChange.PairBindings = append(bad.PendingChange.PairBindings, s.Peers[0].EndpointState.PairBinding)
	if bad.Validate() == nil {
		t.Fatal("fence accepted a terminal affected binding")
	}
	finished.Peers[0].UpgradePending.Context.HostScope.Prefixes[0] = "192.0.2.0/24"
	if !reflect.DeepEqual(before, s) {
		t.Fatal("output aliases retained terminal preparation")
	}
}

func TestV4RejectsStaleMutationAndTerminalContextDuplicates(t *testing.T) {
	initial, proof, _ := pairModelFixture(t)
	s := migratePairFixture(t, initial)
	approval := approvalFor(t, proof, testNow())
	m, _, err := ProposeReceive(s, proof.Update.Issuer, proof, &approval, testNow())
	if err != nil {
		t.Fatal(err)
	}
	review, err := PreviewMutation(s, m)
	if err != nil {
		t.Fatal(err)
	}
	terminal, _, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if validateMutation(terminal, m) == nil {
		t.Fatal("caller-supplied mutation bypassed terminal denial")
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, err := Fence(terminal, m, review, id, testNow(), modelBudget); err == nil {
		t.Fatal("stale mutation fenced terminal target")
	}
	// Even a newly fabricated digest cannot bless a terminal target fence.
	bad := cloneSnapshot(terminal)
	bad.Revision, _ = nextCounter(terminal.Revision)
	bad.PendingChange = &PendingChange{TransactionID: id, FencedAt: terminal.ObservedAt, PairBindings: []string{m.PairBinding}, BaseRevision: terminal.Revision, BaseDigest: modelDigest(terminal), ReviewDigest: modelDigest(struct {
		Snapshot Snapshot
		Mutation Mutation
	}{terminal, m}), Mutation: m}
	if bad.Validate() == nil {
		t.Fatal("structural pending validation accepted terminal target")
	}
	if _, err := FinishPending(bad, false, testNow(), modelBudget); err == nil {
		t.Fatal("finish accepted terminal target")
	}
	f := pairContextFixture(t)
	committed := migratePairFixture(t, committedContext(t, f))
	committed, _, err = RevokeManagedPairV4(committed, committed.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	r := committed.Peers[0]
	binding := r.EndpointState.PairBinding
	if _, err := PrepareContextUpgrade(committed, r.Peer.Key, r.Revision, r.UpgradePending.OwnNonce, r.UpgradePending.PrepareDeadline, testNow(), modelBudget); err == nil {
		t.Fatal("terminal exact preparation retry accepted")
	}
	if _, err := RecordInboundContextPrepare(committed, r.Peer.Key, f.request, f.window, testNow(), modelBudget); err == nil {
		t.Fatal("terminal exact prepared transcript accepted")
	}
	if _, err := CommitPreparedContext(committed, r.Peer.Key, binding, f.window, testNow(), modelBudget); err == nil {
		t.Fatal("terminal duplicate commit accepted")
	}
	request, reply := contextCommitEvidence(committed)
	if _, err := ConfirmContextCommit(committed, r.Peer.Key, request, reply, testNow(), modelBudget); err == nil {
		t.Fatal("terminal retained commitment became confirmed")
	}
}

func TestActiveModelRejectsUnsupportedSchema(t *testing.T) {
	s, proof, key := pairModelFixture(t)
	for _, version := range []int{0, 2, 5} {
		s.Version = version
		peer, binding := s.Peers[0].Peer.Key, s.Peers[0].EndpointState.PairBinding
		if _, err := contextRecord(s, peer, testNow()); err == nil {
			t.Fatal("unsupported context schema accepted", version)
		}
		if _, _, err := ProposeReceive(s, peer, proof, nil, testNow()); err == nil {
			t.Fatal("unsupported receive schema accepted", version)
		}
		if SavedEligibility(s, binding, testNow()) {
			t.Fatal("unsupported schema gained eligibility", version)
		}
		if _, err := ProposeReduction(s, binding, "revoke", testNow()); err == nil {
			t.Fatal("unsupported schema reduction accepted", version)
		}
		if _, err := PreviewMutation(s, Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.9:30009"}); err == nil {
			t.Fatal("unsupported global schema accepted", version)
		}
		if _, err := PrepareExport(s, peer, ExportOptions{Operation: "withdraw", Lifetime: "until-revoked"}, testNow(), modelBudget); err == nil {
			t.Fatal("unsupported export schema accepted", version)
		}
		if _, wire, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, peer, proof.Update, key, testNow(), modelBudget, func([]byte) (bool, error) { t.Fatal("unsupported schema invoked save"); return true, nil }); err == nil || len(wire) != 0 {
			t.Fatal("unsupported schema exported bytes", version)
		}
		if wire, err := ReexportModel(SaveResolution{Snapshot: s, Durable: true}, peer); err == nil || len(wire) != 0 {
			t.Fatal("unsupported schema reexported bytes", version)
		}
	}
}
