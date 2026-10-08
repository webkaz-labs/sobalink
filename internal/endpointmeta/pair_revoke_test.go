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

// Pure fabricated model fixtures only: no files, publishers, transport or runtime.
func pairModelFixture(t *testing.T) (Snapshot, Envelope, ed25519.PrivateKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, ed25519.SeedSize))
	local := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	p := PairContext{Version: 1,
		HostKey: hex.EncodeToString(key.Public().(ed25519.PublicKey)), JoinerKey: hex.EncodeToString(local.Public().(ed25519.PublicKey)),
		HostTunnelKey: strings.Repeat("11", 32), JoinerTunnelKey: strings.Repeat("22", 32),
		HostNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)), JoinerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)),
		HostEndpoint: "127.0.0.1:20001", JoinerEndpoint: "127.0.0.2:20002",
		HostScope: Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}, JoinerScope: Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}}
	state, err := InitialState(p, p.JoinerKey)
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Version: SnapshotVersionV3, Revision: "1", LocalPeer: PeerWire{Key: p.JoinerKey, Endpoint: p.JoinerEndpoint, TunnelKey: p.JoinerTunnelKey}, LocalScope: p.JoinerScope, PreviousLocalEndpoint: p.JoinerEndpoint, ObservedAt: testNow().Format(time.RFC3339Nano),
		Peers: []PeerRecord{{Peer: PeerWire{Key: p.HostKey, Endpoint: p.HostEndpoint, TunnelKey: p.HostTunnelKey}, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state}}}
	binding, _ := p.Binding()
	scope, _ := p.JoinerScope.Digest()
	e, err := Sign(UpdateBody{Version: 1, Domain: UpdateDomain, PairBinding: binding, Issuer: p.HostKey, Recipient: p.JoinerKey,
		IssuerTunnelKey: p.HostTunnelKey, RecipientTunnelKey: p.JoinerTunnelKey, Sequence: "7", PriorEndpoint: p.HostEndpoint,
		Operation: "set", Endpoint: "127.0.0.3:20003", ScopeDigest: scope, Issued: testNow().Format(time.RFC3339Nano), Lifetime: "finite", Expires: testNow().Add(time.Hour).Format(time.RFC3339Nano)}, key)
	if err != nil || s.Validate() != nil {
		t.Fatal("invalid fabricated fixture", err)
	}
	return s, e, key
}

func pairContextFixture(t *testing.T) contextFixture {
	t.Helper()
	s, _, remoteKey := pairModelFixture(t)
	localKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
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
		SenderNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)), SenderEndpoint: peer.Endpoint, RecipientEndpoint: s.LocalPeer.Endpoint,
		SenderScope: Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}}
	return contextFixture{s, prepared.Snapshot, request, PreparationWindow{peer.Key, nonce, deadline}, localKey, remoteKey}
}

func migratePairFixture(t *testing.T, s Snapshot) Snapshot {
	t.Helper()
	next, err := MigrateManagedV4(s, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestManagedV4MigrationIsLosslessAndSeparate(t *testing.T) {
	f := pairContextFixture(t)
	confirmed, proof, _ := pairModelFixture(t)
	withProof := accept(t, confirmed, proof, testNow())
	for _, before := range []Snapshot{f.legacy, f.reviewed, recordedContext(t, f), committedContext(t, f), confirmed, withProof} {
		original := cloneSnapshot(before)
		next := migratePairFixture(t, before)
		want := cloneSnapshot(before)
		want.Version = SnapshotVersionV4
		want.Revision, _ = nextCounter(before.Revision)
		want.ObservedAt = testNow().Format(time.RFC3339Nano)
		if !reflect.DeepEqual(next, want) || !reflect.DeepEqual(before, original) {
			t.Fatal("migration changed historical evidence")
		}
		next.LocalScope.Prefixes[0] = "192.0.2.0/24"
		if !reflect.DeepEqual(before, original) {
			t.Fatal("migration output aliases input")
		}
		if _, err := MigrateManagedV4(want, testNow(), modelBudget); err == nil {
			t.Fatal("implicit repeated migration accepted")
		}
	}
	legacy, err := MigrateLegacy(LegacySnapshot{Version: 2, LocalPeer: f.legacy.LocalPeer, LocalScope: f.legacy.LocalScope, Peers: []PeerWire{f.legacy.Peers[0].Peer}}, testNow())
	if err != nil || legacy.Version != SnapshotVersionV3 {
		t.Fatal("v2 migration changed", err)
	}
	wire, err := EncodeSnapshot(legacy, modelBudget)
	if err != nil || bytes.Contains(wire, []byte("pair_revocation")) {
		t.Fatal("v3 omitted wire shape changed", err)
	}
	parsed, err := ParseSnapshot(wire, modelBudget)
	if err != nil || !reflect.DeepEqual(parsed, legacy) {
		t.Fatal("v3 round trip changed", err)
	}
}

func TestPairRemovalPreservesEvidenceAndExactRetry(t *testing.T) {
	f := pairContextFixture(t)
	confirmed, proof, _ := pairModelFixture(t)
	for _, old := range []Snapshot{confirmed, committedContext(t, f), accept(t, confirmed, proof, testNow())} {
		before := migratePairFixture(t, old)
		original := cloneSnapshot(before)
		now := testNow().Add(24 * time.Hour) // expired proofs remain historical evidence
		next, changed, err := RevokeManagedPairV4(before, before.Peers[0], now, modelBudget)
		if err != nil || !changed {
			t.Fatal("terminal reduction", changed, err)
		}
		want := cloneSnapshot(before)
		want.Revision, _ = nextCounter(before.Revision)
		want.ObservedAt = now.Format(time.RFC3339Nano)
		binding, _ := before.Peers[0].PairContext.Binding()
		want.Peers[0].PairRevocation = &PairRevocation{PairBinding: binding, Revision: want.Revision, RevokedAt: want.ObservedAt}
		if !reflect.DeepEqual(next, want) || !reflect.DeepEqual(before, original) {
			t.Fatal("terminal reduction changed retained evidence")
		}
		again, changed, err := RevokeManagedPairV4(next, next.Peers[0], now.Add(time.Hour), modelBudget)
		if err != nil || changed || !reflect.DeepEqual(again, next) {
			t.Fatal("exact retry refreshed time or revision", err)
		}
		if _, _, err := RevokeManagedPairV4(next, before.Peers[0], now, modelBudget); err == nil {
			t.Fatal("stale whole-record review accepted")
		}
		again.Peers[0].PairRevocation.PairBinding = "changed"
		again.Peers[0].PairContext.HostScope.Prefixes[0] = "192.0.2.0/24"
		again.Peers[0].EndpointState.LastEndpoint = "192.0.2.1:1"
		if !reflect.DeepEqual(next, want) || !reflect.DeepEqual(before, original) {
			t.Fatal("output aliases historical evidence")
		}
	}
	for _, old := range []Snapshot{f.legacy, f.reviewed} {
		s := migratePairFixture(t, old)
		next, changed, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), modelBudget)
		if err != nil || !changed || next.Peers[0].PairRevocation == nil || next.Peers[0].PairRevocation.Kind != "local-record" {
			t.Fatal("local review was not terminally denied", err)
		}
	}
}

func TestPairRemovalCanonicalSizeAndMarkerValidation(t *testing.T) {
	old, _, _ := pairModelFixture(t)
	s := migratePairFixture(t, old)
	next, _, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Snapshot{s, next} {
		wire, err := EncodeSnapshot(candidate, modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		w := wireSizer{left: len(wire)}
		if !candidate.measure(&w) || w.left != 0 {
			t.Fatal("wire size differs from exact encoded size")
		}
		if _, err := EncodeSnapshot(candidate, len(wire)-1); err == nil {
			t.Fatal("undersized budget accepted")
		}
		got, err := ParseSnapshot(wire, len(wire))
		if err != nil || !reflect.DeepEqual(got, candidate) {
			t.Fatal("v4 canonical round trip", err)
		}
		// An old exact-v3 header reader rejects marker-free v4 as well.
		var header struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(wire, &header) != nil || header.Version == SnapshotVersionV3 {
			t.Fatal("new schema masquerades as v3")
		}
	}
	wire, _ := EncodeSnapshot(next, modelBudget)
	if _, changed, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), len(wire)); err != nil || !changed {
		t.Fatal("exact changed budget rejected", err)
	}
	if _, _, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), len(wire)-1); err == nil {
		t.Fatal("changed output exceeded budget")
	}
	if _, _, err := RevokeManagedPairV4(next, next.Peers[0], testNow(), len(wire)-1); err == nil {
		t.Fatal("no-op output bypassed budget")
	}
	marker, _ := json.Marshal(next.Peers[0].PairRevocation)
	for _, bad := range []string{
		strings.Replace(string(wire), string(marker), "null", 1),
		strings.Replace(string(wire), string(marker), string(marker)+`,"pair_revocation":`+string(marker), 1),
		strings.Replace(string(wire), `"revoked_at":`, `"unknown":true,"revoked_at":`, 1),
		strings.Replace(string(wire), `"revoked_at":`, `"revision":"3","revoked_at":`, 1),
	} {
		if _, err := ParseSnapshot([]byte(bad), modelBudget); err == nil {
			t.Fatal("noncanonical marker accepted")
		}
	}
	for _, alter := range []func(*Snapshot){
		func(x *Snapshot) { x.Version = SnapshotVersionV3 },
		func(x *Snapshot) { x.Version = 5 },
		func(x *Snapshot) { x.Peers[0].PairRevocation.PairBinding = "wrong" },
		func(x *Snapshot) { x.Peers[0].PairRevocation.Revision = "0" },
		func(x *Snapshot) { x.Peers[0].PairRevocation.Revision = "01" },
		func(x *Snapshot) { x.Peers[0].PairRevocation.Revision = "99" },
		func(x *Snapshot) {
			x.Peers[0].PairRevocation.RevokedAt = testNow().Add(time.Second).Format(time.RFC3339Nano)
		},
		func(x *Snapshot) { x.Peers[0].PairRevocation.RevokedAt = "2030-01-01T00:00:00+00:00" },
		func(x *Snapshot) { x.Peers[0].PairContext = nil },
		func(x *Snapshot) { x.Peers[0].EndpointState = nil },
		func(x *Snapshot) { x.PendingChange = &PendingChange{} },
		func(x *Snapshot) { x.Peers[0].EndpointState.ReceivedHighwater = "99" },
	} {
		bad := cloneSnapshot(next)
		alter(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid marker or historical evidence accepted")
		}
	}
}

func TestPairMigrationAndRemovalFailureBoundaries(t *testing.T) {
	old, _, _ := pairModelFixture(t)
	v4 := migratePairFixture(t, old)
	wire, _ := EncodeSnapshot(v4, modelBudget)
	if _, err := MigrateManagedV4(old, testNow(), len(wire)); err != nil {
		t.Fatal("exact migration budget rejected", err)
	}
	if _, err := MigrateManagedV4(old, testNow(), len(wire)-1); err == nil {
		t.Fatal("migration budget ignored")
	}
	for _, now := range []time.Time{{}, testNow().Add(-time.Second)} {
		if _, err := MigrateManagedV4(old, now, modelBudget); err == nil {
			t.Fatal("migration clock rejected too late")
		}
		if _, _, err := RevokeManagedPairV4(v4, v4.Peers[0], now, modelBudget); err == nil {
			t.Fatal("removal clock ignored")
		}
	}
	fenced := stage(t, old, Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.3:4444"}, testNow())
	if _, err := MigrateManagedV4(fenced, testNow(), modelBudget); err == nil {
		t.Fatal("migration reinterpreted a v3 fence")
	}
	old.Revision = "18446744073709551615"
	if _, err := MigrateManagedV4(old, testNow(), modelBudget); err == nil {
		t.Fatal("migration counter exhaustion ignored")
	}
	v4.Revision = old.Revision
	if _, _, err := RevokeManagedPairV4(v4, v4.Peers[0], testNow(), modelBudget); err == nil {
		t.Fatal("removal counter exhaustion ignored")
	}
	binding, _ := v4.Peers[0].PairContext.Binding()
	v4.Peers[0].PairRevocation = &PairRevocation{PairBinding: binding, Revision: v4.Revision, RevokedAt: v4.ObservedAt}
	if got, changed, err := RevokeManagedPairV4(v4, v4.Peers[0], testNow(), modelBudget); err != nil || changed || !reflect.DeepEqual(got, v4) {
		t.Fatal("exact no-op unnecessarily consumed revision", err)
	}
}

// Every public target-to-authority route rejects terminal records before
// duplicate/no-op branches. Passive parsing and sizing retain their evidence.
func TestV4TerminalAuthorityEntrypointMatrix(t *testing.T) {
	old, proof, key := pairModelFixture(t)
	managed := migratePairFixture(t, accept(t, old, proof, testNow()))
	terminal, _, err := RevokeManagedPairV4(managed, managed.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Snapshot{terminal} {
		peer := old.Peers[0].Peer.Key
		binding := old.Peers[0].EndpointState.PairBinding
		now := testNow()
		check := func(name string, err error) {
			t.Helper()
			if err == nil {
				t.Fatal(name, "accepted a terminal target")
			}
		}
		_, err := PrepareContextUpgrade(s, peer, "1", "", "", now, modelBudget)
		check("prepare", err)
		_, err = RecordInboundContextPrepare(s, peer, PrepareRequest{}, PreparationWindow{}, now, modelBudget)
		check("inbound", err)
		_, err = RecordOutboundContextPrepare(s, peer, PrepareRequest{}, PrepareReply{}, PreparationWindow{}, now, modelBudget)
		check("outbound", err)
		_, err = CommitPreparedContext(s, peer, binding, PreparationWindow{}, now, modelBudget)
		check("commit", err)
		_, err = ConfirmContextCommit(s, peer, BoundRequest{}, ContextReply{}, now, modelBudget)
		check("confirm", err)
		_, err = ConfirmContextFromUpdate(s, peer, proof, now, modelBudget)
		check("confirm update", err)
		_, err = ReviewContextResume(s, peer, "", now)
		check("review resume", err)
		_, err = ResumePreparedContext(s, ContextResumeReview{PeerKey: peer}, now, modelBudget)
		check("resume", err)
		_, _, err = ProposeReceive(s, peer, proof, nil, now)
		check("receive duplicate", err)
		if SavedEligibility(s, binding, now) {
			t.Fatal("saved eligibility accepted terminal target")
		}
		_, err = ProposeReapproval(s, binding, Approval{}, now)
		check("reapproval", err)
		_, err = ProposeReduction(s, binding, "revoke", now)
		check("reduction", err)
		_, err = ProposeFollow(s, binding, FollowApproval{}, now)
		check("follow", err)
		_, err = PreviewMutation(s, Mutation{Kind: "revoke", PairBinding: binding, State: s.Peers[0].EndpointState})
		check("direct mutation", err)
		_, err = Fence(s, Mutation{}, "", "", now, modelBudget)
		check("fence", err)
		_, err = FinishPending(s, false, now, modelBudget)
		check("finish", err)
		_, err = PrepareExport(s, peer, ExportOptions{Operation: "withdraw", Lifetime: "until-revoked"}, now, modelBudget)
		check("prepare export", err)
		_, err = ProposeIssued(s, peer, proof, now, modelBudget)
		check("issued", err)
		called := false
		_, wire, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, peer, proof.Update, key, now, modelBudget, func([]byte) (bool, error) {
			called = true
			return true, nil
		})
		check("export", err)
		if called || len(wire) != 0 {
			t.Fatal("terminal target exported bytes or invoked publisher")
		}
		wire, err = ReexportModel(SaveResolution{Snapshot: s, Durable: true}, peer)
		check("reexport", err)
		if len(wire) != 0 {
			t.Fatal("terminal target reexported bytes")
		}
	}
}

func TestPairRemovalRetainsUnrelatedRecordsAndRejectsFabricatedTarget(t *testing.T) {
	s, _, _ := pairModelFixture(t)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{43}, ed25519.SeedSize))
	s.Peers = append(s.Peers, PeerRecord{Peer: PeerWire{Key: hex.EncodeToString(other.Public().(ed25519.PublicKey)), TunnelKey: strings.Repeat("33", 32), Endpoint: "127.0.0.4:20004"}, Revision: "1"})
	s = migratePairFixture(t, s)
	before := cloneSnapshot(s)
	for _, alter := range []func(*PeerRecord){
		func(r *PeerRecord) { r.Revision = "2" },
		func(r *PeerRecord) { r.Peer.Endpoint = "127.0.0.9:20009" },
		func(r *PeerRecord) { r.ContextConfirmed = false },
		func(r *PeerRecord) {
			r.PairContext.HostNonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
		},
		func(r *PeerRecord) { r.EndpointState.AuthorityRevision = "1" },
	} {
		expected := cloneSnapshot(s).Peers[0]
		alter(&expected)
		if _, _, err := RevokeManagedPairV4(s, expected, testNow(), modelBudget); err == nil {
			t.Fatal("fabricated or stale target was accepted")
		}
	}
	next, changed, err := RevokeManagedPairV4(s, s.Peers[0], testNow(), modelBudget)
	if err != nil || !changed || !reflect.DeepEqual(next.Peers[1], before.Peers[1]) || !reflect.DeepEqual(s, before) {
		t.Fatal("unrelated record or input changed", err)
	}
	next.Peers[1].Peer.Name = "synthetic mutation"
	if !reflect.DeepEqual(s, before) {
		t.Fatal("unrelated output record aliases input")
	}
}
