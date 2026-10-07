package endpointmeta

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These are inert model fixtures. Fabricated request/reply values below do not
// authenticate a peer and exercise no Core, file publisher, TLS or runtime path.
type contextFixture struct {
	legacy, reviewed Snapshot
	request          PrepareRequest
	window           PreparationWindow
	localKey         ed25519.PrivateKey
	remoteKey        ed25519.PrivateKey
}

func newContextFixture(t *testing.T) contextFixture {
	t.Helper()
	s, _, remoteKey := modelFixture(t)
	localKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{23}, ed25519.SeedSize))
	s.LocalPeer.Key = hex.EncodeToString(localKey.Public().(ed25519.PublicKey))
	s.Peers[0].PairContext, s.Peers[0].EndpointState = nil, nil
	s.Peers[0].ContextConfirmed = false
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	deadline := testNow().Add(time.Minute)
	prepared, err := PrepareContextUpgrade(s, s.Peers[0].Peer.Key, "1", nonce, deadline.Format(time.RFC3339Nano), testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	peer := s.Peers[0].Peer
	request := PrepareRequest{Version: 2, Operation: "pair-context-prepare", Sender: peer.Key, Recipient: s.LocalPeer.Key,
		SenderTunnelKey: peer.TunnelKey, RecipientTunnelKey: s.LocalPeer.TunnelKey,
		SenderNonce:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		SenderEndpoint: peer.Endpoint, RecipientEndpoint: s.LocalPeer.Endpoint,
		SenderScope: Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}}
	return contextFixture{s, prepared.Snapshot, request, PreparationWindow{peer.Key, nonce, deadline}, localKey, remoteKey}
}

func recordedContext(t *testing.T, f contextFixture) Snapshot {
	t.Helper()
	result, err := RecordInboundContextPrepare(f.reviewed, f.request.Sender, f.request, f.window, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return result.Snapshot
}

func committedContext(t *testing.T, f contextFixture) Snapshot {
	t.Helper()
	s := recordedContext(t, f)
	binding, _ := s.Peers[0].UpgradePending.Context.Binding()
	result, err := CommitPreparedContext(s, f.request.Sender, binding, f.window, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return result.Snapshot
}

func contextCommitEvidence(s Snapshot) (BoundRequest, ContextReply) {
	binding, _ := s.Peers[0].PairContext.Binding()
	return BoundRequest{Version: 2, Operation: "pair-context-status", PairBinding: binding},
		ContextReply{Version: 2, Operation: "pair-context-status", OK: true, PairBinding: binding, State: "committed"}
}

func TestContextUpgradePhasesAndOppositeTranscriptAgreement(t *testing.T) {
	f := newContextFixture(t)
	if f.reviewed.Revision != "2" || f.reviewed.Peers[0].Revision != "1" || f.reviewed.Peers[0].UpgradePending.PeerRevision != "1" ||
		f.reviewed.Peers[0].PairContext != nil || f.reviewed.Peers[0].EndpointState != nil {
		t.Fatal("preparation changed peer authority or captured revision")
	}
	prepared := recordedContext(t, f)
	pair := *prepared.Peers[0].UpgradePending.Context
	if prepared.Revision != "3" || prepared.Peers[0].Revision != "1" || pair.HostKey >= pair.JoinerKey {
		t.Fatal("recording did not retain deterministic roles and frozen peer revision")
	}
	// The other side's outbound request is the first side's inbound request.
	remote := Snapshot{Version: 3, Revision: "1", LocalPeer: f.legacy.Peers[0].Peer, LocalScope: f.request.SenderScope,
		PreviousLocalEndpoint: f.request.SenderEndpoint, ObservedAt: f.legacy.ObservedAt,
		Peers: []PeerRecord{{Peer: f.legacy.LocalPeer, Revision: "1"}}}
	r, err := PrepareContextUpgrade(remote, remote.Peers[0].Peer.Key, "1", f.request.SenderNonce, f.window.Deadline.Format(time.RFC3339Nano), testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := pair.Binding()
	reply := PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairContext: pair, PairBinding: binding}
	window := PreparationWindow{remote.Peers[0].Peer.Key, f.request.SenderNonce, f.window.Deadline}
	outbound, err := RecordOutboundContextPrepare(r.Snapshot, remote.Peers[0].Peer.Key, f.request, reply, window, testNow(), modelBudget)
	if err != nil || modelDigest(outbound.Snapshot.Peers[0].UpgradePending.Context) != modelDigest(&pair) {
		t.Fatal("opposite prepare directions did not agree on the exact context", err)
	}
	committed := committedContext(t, f)
	state := committed.Peers[0].EndpointState
	if committed.Revision != "4" || committed.Peers[0].Revision != "1" || committed.Peers[0].ContextConfirmed ||
		state.IssuedHighwater != "0" || state.ReceivedHighwater != "0" || state.AuthorityRevision != "0" || state.Approval != nil || state.Follow != nil {
		t.Fatal("first local commit manufactured authority or confirmed the peer")
	}
	request, committedReply := contextCommitEvidence(committed)
	confirmed, err := ConfirmContextCommit(committed, f.request.Sender, request, committedReply, f.window.Deadline.Add(time.Hour), modelBudget)
	if err != nil || !confirmed.Changed || confirmed.Phase != "confirmed" || confirmed.Snapshot.Revision != "5" ||
		confirmed.Snapshot.Peers[0].Revision != "5" || confirmed.Snapshot.Peers[0].UpgradePending != nil {
		t.Fatal("fresh confirmation of durable context failed after preparation expiry", err)
	}
	duplicate, err := ConfirmContextCommit(confirmed.Snapshot, f.request.Sender, request, committedReply, f.window.Deadline.Add(time.Hour), modelBudget)
	if err != nil || duplicate.Changed || modelDigest(duplicate.Snapshot) != modelDigest(confirmed.Snapshot) {
		t.Fatal("duplicate confirmation recreated pending state or advanced revisions", err)
	}
}

func TestContextPrepareRetriesAndCopiedTranscriptSlices(t *testing.T) {
	f := newContextFixture(t)
	original := modelDigest(f.reviewed)
	prepared := recordedContext(t, f)
	expected := modelDigest(prepared)
	again, err := RecordInboundContextPrepare(prepared, f.request.Sender, f.request, PreparationWindow{}, f.window.Deadline.Add(time.Second), modelBudget)
	if err != nil || again.Changed || modelDigest(again.Snapshot) != expected {
		t.Fatal("exact observational prepare retry renewed its phase/deadline", err)
	}
	duplicate, err := PrepareContextUpgrade(f.reviewed, f.request.Sender, "1", f.window.OwnNonce, f.window.Deadline.Format(time.RFC3339Nano), f.window.Deadline.Add(time.Second), modelBudget)
	if err != nil || duplicate.Changed || modelDigest(duplicate.Snapshot) != original {
		t.Fatal("exact local preparation retry changed evidence", err)
	}
	if _, err := PrepareContextUpgrade(f.reviewed, f.request.Sender, "1", f.window.OwnNonce, f.window.Deadline.Add(time.Hour).Format(time.RFC3339Nano), testNow(), modelBudget); err != ErrReview {
		t.Fatal("retry silently renewed the preparation deadline", err)
	}
	f.request.SenderScope.Prefixes[0] = "127.0.0.0/16"
	if modelDigest(prepared) != expected || modelDigest(f.reviewed) != original {
		t.Fatal("request scope mutation changed a snapshot")
	}
	again.Snapshot.Peers[0].UpgradePending.Context.HostScope.Prefixes[0] = "127.0.0.0/16"
	if modelDigest(prepared) != expected {
		t.Fatal("output context scope aliases input state")
	}
}

func TestContextPrepareRejectsMismatchedPinnedAndSavedTranscript(t *testing.T) {
	f := newContextFixture(t)
	prepared := recordedContext(t, f)
	for name, change := range map[string]func(*PrepareRequest){
		"sender":    func(r *PrepareRequest) { r.Sender = strings.Repeat("a", 64) },
		"recipient": func(r *PrepareRequest) { r.Recipient = strings.Repeat("b", 64) },
		"endpoint":  func(r *PrepareRequest) { r.SenderEndpoint = "127.0.0.9:20009" },
		"nonce replacement": func(r *PrepareRequest) {
			r.SenderNonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
		},
		"scope replacement": func(r *PrepareRequest) { r.SenderScope = Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/16"}} },
	} {
		t.Run(name, func(t *testing.T) {
			request := f.request
			change(&request)
			if _, err := RecordInboundContextPrepare(prepared, f.request.Sender, request, f.window, testNow(), modelBudget); err == nil {
				t.Fatal("mismatched prepared transcript accepted")
			}
		})
	}
	if _, err := RecordInboundContextPrepare(f.legacy, f.request.Sender, f.request, f.window, testNow(), modelBudget); err != ErrReview {
		t.Fatal("incoming prepare created local review", err)
	}
	badWindow := f.window
	badWindow.OwnNonce = f.request.SenderNonce
	if _, err := RecordInboundContextPrepare(f.reviewed, f.request.Sender, f.request, badWindow, testNow(), modelBudget); err != ErrReview {
		t.Fatal("preparation bound was not associated with the saved nonce", err)
	}
	if _, err := RecordInboundContextPrepare(f.reviewed, f.request.Sender, f.request, f.window, testNow().Add(-time.Second), modelBudget); err != ErrReview {
		t.Fatal("saved-time regression accepted", err)
	}
}

func TestContextDeadlineAndExplicitResumePreserveProposal(t *testing.T) {
	f := newContextFixture(t)
	if _, err := RecordInboundContextPrepare(f.reviewed, f.request.Sender, f.request, f.window, f.window.Deadline, modelBudget); err != ErrExpired {
		t.Fatal("expired first transcript recording accepted", err)
	}
	prepared := recordedContext(t, f)
	binding, _ := prepared.Peers[0].UpgradePending.Context.Binding()
	if _, err := CommitPreparedContext(prepared, f.request.Sender, binding, f.window, f.window.Deadline, modelBudget); err != ErrExpired {
		t.Fatal("expired first local commit accepted", err)
	}
	now := f.window.Deadline.Add(time.Second)
	deadline := now.Add(time.Hour)
	review, err := ReviewContextResume(prepared, f.request.Sender, deadline.Format(time.RFC3339Nano), now)
	if err != nil {
		t.Fatal(err)
	}
	changed := cloneSnapshot(prepared)
	changed.Revision = "4"
	if _, err := ResumePreparedContext(changed, review, now, modelBudget); err != ErrReview {
		t.Fatal("old resume review attached to a later snapshot", err)
	}
	resumed, err := ResumePreparedContext(prepared, review, now, modelBudget)
	if err != nil || !resumed.Changed || resumed.Snapshot.Peers[0].Revision != "1" {
		t.Fatal("explicit deadline resume", err)
	}
	before, after := *prepared.Peers[0].UpgradePending, *resumed.Snapshot.Peers[0].UpgradePending
	after.PrepareDeadline = before.PrepareDeadline
	if !reflect.DeepEqual(before, after) || resumed.Snapshot.Peers[0].EndpointState != nil {
		t.Fatal("resume replaced nonce/context or manufactured authority")
	}
	if _, err := CommitPreparedContext(resumed.Snapshot, f.request.Sender, binding, f.window, now, modelBudget); err != ErrReview {
		t.Fatal("renewed proposal reused the old process bound", err)
	}
	newWindow := PreparationWindow{f.request.Sender, f.window.OwnNonce, deadline}
	committed, err := CommitPreparedContext(resumed.Snapshot, f.request.Sender, binding, newWindow, now, modelBudget)
	if err != nil || committed.Phase != "committed" {
		t.Fatal("reviewed renewed preparation could not commit", err)
	}
	duplicate, err := CommitPreparedContext(committed.Snapshot, f.request.Sender, binding, PreparationWindow{}, deadline.Add(time.Hour), modelBudget)
	if err != nil || duplicate.Changed || modelDigest(duplicate.Snapshot) != modelDigest(committed.Snapshot) {
		t.Fatal("already committed context was reset after deadline", err)
	}
}

func contextHistory(t *testing.T, f contextFixture) (Snapshot, Envelope) {
	t.Helper()
	s := committedContext(t, f)
	r := &s.Peers[0]
	p := r.PairContext
	state := r.EndpointState
	localScope, _ := s.LocalScope.Digest()
	remoteScope := p.HostScope
	if p.HostKey == s.LocalPeer.Key {
		remoteScope = p.JoinerScope
	}
	remoteDigest, _ := remoteScope.Digest()
	incoming := UpdateBody{Version: 1, Domain: UpdateDomain, PairBinding: state.PairBinding, Issuer: r.Peer.Key,
		Recipient: s.LocalPeer.Key, IssuerTunnelKey: r.Peer.TunnelKey, RecipientTunnelKey: s.LocalPeer.TunnelKey,
		Sequence: "7", PriorEndpoint: r.Peer.Endpoint, Operation: "set", Endpoint: r.Peer.Endpoint,
		ScopeDigest: localScope, Issued: testNow().Add(-time.Minute).Format(time.RFC3339Nano), Lifetime: "finite", Expires: testNow().Add(time.Hour).Format(time.RFC3339Nano)}
	proof, err := Sign(incoming, f.remoteKey)
	if err != nil {
		t.Fatal(err)
	}
	state.ReceivedVersion, state.ReceivedHighwater, state.ReceivedProof = 1, "7", &proof
	state.ReceiveStatus, state.AuthorityRevision = "eligible", "2"
	state.Follow = &FollowApproval{ScopeDigest: localScope, Revision: "1", Granted: testNow().Format(time.RFC3339Nano), Lifetime: "until-revoked", Active: true}
	digest, _ := proof.Digest()
	state.Approval = &Approval{Kind: "follow", ProofDigest: digest, Endpoint: r.Peer.Endpoint, FollowRevision: "1", Granted: state.Follow.Granted, Lifetime: "until-revoked"}
	outgoing := incoming
	outgoing.Issuer, outgoing.Recipient = s.LocalPeer.Key, r.Peer.Key
	outgoing.IssuerTunnelKey, outgoing.RecipientTunnelKey = s.LocalPeer.TunnelKey, r.Peer.TunnelKey
	outgoing.Sequence, outgoing.PriorEndpoint, outgoing.Endpoint, outgoing.ScopeDigest = "3", s.LocalPeer.Endpoint, s.LocalPeer.Endpoint, remoteDigest
	issued, err := Sign(outgoing, f.localKey)
	if err != nil {
		t.Fatal(err)
	}
	state.IssuedVersion, state.IssuedHighwater, state.IssuedProof = 1, "3", &issued
	if err := s.ValidateAt(testNow()); err != nil {
		t.Fatal("invalid synthetic history", err)
	}
	incoming.Sequence = "8"
	possession, err := Sign(incoming, f.remoteKey)
	if err != nil {
		t.Fatal(err)
	}
	return s, possession
}

func TestContextConfirmationPreservesHistoryAndChecksEvidenceShape(t *testing.T) {
	f := newContextFixture(t)
	s, possession := contextHistory(t, f)
	before := cloneState(*s.Peers[0].EndpointState)
	request, reply := contextCommitEvidence(s)
	confirmed, err := ConfirmContextCommit(s, f.request.Sender, request, reply, testNow(), modelBudget)
	if err != nil || !reflect.DeepEqual(before, *confirmed.Snapshot.Peers[0].EndpointState) {
		t.Fatal("committed-response confirmation reset endpoint history", err)
	}
	signed, err := ConfirmContextFromUpdate(s, f.request.Sender, possession, testNow(), modelBudget)
	if err != nil || !reflect.DeepEqual(before, *signed.Snapshot.Peers[0].EndpointState) || signed.Snapshot.Peers[0].EndpointState.ReceivedHighwater != "7" {
		t.Fatal("signed possession consumed endpoint sequence or created approval", err)
	}
	prepared := recordedContext(t, f)
	if _, err := ConfirmContextCommit(prepared, f.request.Sender, request, reply, testNow(), modelBudget); err != ErrReview {
		t.Fatal("confirmation bypassed local context commit", err)
	}
	for _, variant := range []string{"prepared", "mismatched operation", "session", "binding"} {
		req, rep := request, reply
		switch variant {
		case "prepared":
			rep.State = "prepared"
		case "mismatched operation":
			rep.Operation = "pair-context-commit"
		case "session":
			req.Operation = "session"
		case "binding":
			req.PairBinding, rep.PairBinding = strings.Repeat("0", 64), strings.Repeat("0", 64)
		}
		if _, err := ConfirmContextCommit(s, f.request.Sender, req, rep, testNow(), modelBudget); err == nil {
			t.Fatal("invalid confirmation variant accepted", variant)
		}
	}
	bad := possession.Update
	bad.PairBinding = strings.Repeat("0", 64)
	wrongPair, _ := Sign(bad, f.remoteKey)
	if _, err := ConfirmContextFromUpdate(s, f.request.Sender, wrongPair, testNow(), modelBudget); err == nil {
		t.Fatal("signed proof from another context confirmed this one")
	}
	bad = possession.Update
	bad.Expires = testNow().Add(-time.Second).Format(time.RFC3339Nano)
	expired, _ := Sign(bad, f.remoteKey)
	if _, err := ConfirmContextFromUpdate(s, f.request.Sender, expired, testNow(), modelBudget); err != ErrExpired {
		t.Fatal("expired signed possession accepted", err)
	}
}

func TestContextModelCapacityClockAndPendingFence(t *testing.T) {
	f := newContextFixture(t)
	encoded, err := EncodeSnapshot(f.reviewed, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareContextUpgrade(f.legacy, f.request.Sender, "1", f.window.OwnNonce, f.window.Deadline.Format(time.RFC3339Nano), testNow(), len(encoded)-1); err != ErrCapacity {
		t.Fatal("context preparation exceeded exact model budget", err)
	}
	exact, err := PrepareContextUpgrade(f.legacy, f.request.Sender, "1", f.window.OwnNonce, f.window.Deadline.Format(time.RFC3339Nano), testNow(), len(encoded))
	if err != nil || modelDigest(exact.Snapshot) != modelDigest(f.reviewed) {
		t.Fatal("exact model budget rejected", err)
	}
	exhausted := cloneSnapshot(f.legacy)
	exhausted.Revision = "18446744073709551615"
	if _, err := PrepareContextUpgrade(exhausted, f.request.Sender, "1", f.window.OwnNonce, f.window.Deadline.Format(time.RFC3339Nano), testNow(), modelBudget); err != ErrCapacity {
		t.Fatal("context revision wrapped", err)
	}
	committed := committedContext(t, f)
	request, reply := contextCommitEvidence(committed)
	pending := stage(t, committed, Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.9:20009"}, testNow())
	if _, err := ConfirmContextCommit(pending, f.request.Sender, request, reply, testNow(), modelBudget); err != ErrRecovery {
		t.Fatal("context confirmation bypassed endpoint transaction fence", err)
	}
}
