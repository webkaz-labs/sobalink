package endpointmeta

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func senderModel(t *testing.T) (Snapshot, UpdateBody, ed25519.PrivateKey) {
	t.Helper()
	receiver, e, key := modelFixture(t)
	p := *receiver.Peers[0].PairContext
	state, err := InitialState(p, p.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Version: 3, Revision: "1", LocalPeer: receiver.Peers[0].Peer, LocalScope: p.HostScope, PreviousLocalEndpoint: p.HostEndpoint, ObservedAt: testNow().Format(time.RFC3339Nano), Peers: []PeerRecord{{Peer: receiver.LocalPeer, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state}}}
	u := e.Update
	u.Endpoint, u.PriorEndpoint, u.Sequence = s.LocalPeer.Endpoint, s.PreviousLocalEndpoint, "1"
	return s, u, key
}

func encodedModelSize(t *testing.T, s Snapshot) int {
	t.Helper()
	b, err := EncodeSnapshot(s, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func TestBudgetedHelpersMeasureBeforeAllocating(t *testing.T) {
	s, e, _ := modelFixture(t)
	s.Revision = "99"
	now := testNow().Add(123456789 * time.Nanosecond)
	a := approvalFor(t, e, now)
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := PreviewMutation(s, m)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	pending := stage(t, s, m, now)
	final, err := FinishPending(pending, false, now, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	sender, u, key := senderModel(t)
	save := func([]byte) (bool, error) { return true, nil }
	exported, _, err := ExportModel(SaveResolution{Snapshot: sender, Durable: true}, u.Recipient, u, key, now, modelBudget, save)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		size int
		run  func(int) error
	}{
		"fence": {encodedModelSize(t, pending), func(budget int) error {
			_, err := Fence(s, m, review, id, now, budget)
			return err
		}},
		"finish": {encodedModelSize(t, final), func(budget int) error {
			_, err := FinishPending(pending, false, now, budget)
			return err
		}},
		"export": {encodedModelSize(t, exported.Snapshot), func(budget int) error {
			_, _, err := ExportModel(SaveResolution{Snapshot: sender, Durable: true}, u.Recipient, u, key, now, budget, save)
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, budget := range []int{10, tc.size - 1} {
				allocs := testing.AllocsPerRun(3, func() {
					if err := tc.run(budget); err != ErrCapacity {
						t.Fatal("budget rejection", err)
					}
				})
				if allocs != 0 {
					t.Fatal("allocated before rejecting prospective snapshot", budget, allocs)
				}
			}
			if err := tc.run(tc.size); err != nil {
				t.Fatal("exact prospective budget", err)
			}
		})
	}
}

func TestShrinkingReconciliationFitsSmallerBudget(t *testing.T) {
	s, e, _ := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	pending := stage(t, s, m, testNow())
	for _, cancel := range []bool{false, true} {
		for _, now := range []time.Time{testNow(), testNow().Add(48 * time.Hour)} {
			final, err := FinishPending(pending, cancel, now, modelBudget)
			if err != nil {
				t.Fatal(err)
			}
			budget := encodedModelSize(t, final)
			if encodedModelSize(t, pending) <= budget {
				t.Fatal("fixture did not shrink")
			}
			got, err := FinishPending(pending, cancel, now, budget)
			if err != nil || modelDigest(got) != modelDigest(final) {
				t.Fatal("shrinking final snapshot rejected", err)
			}
			if _, err := FinishPending(pending, cancel, now, budget-1); err != ErrCapacity {
				t.Fatal("undersized final budget", err)
			}
		}
	}
}

func TestShrinkingExportFitsSmallerBudget(t *testing.T) {
	s, u, key := senderModel(t)
	save := func([]byte) (bool, error) { return true, nil }
	first, _, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, u.Recipient, u, key, testNow(), modelBudget, save)
	if err != nil {
		t.Fatal(err)
	}
	u.Operation, u.Endpoint, u.Sequence = "withdraw", "", "2"
	second, _, err := ExportModel(first, u.Recipient, u, key, testNow(), modelBudget, save)
	if err != nil {
		t.Fatal(err)
	}
	budget := encodedModelSize(t, second.Snapshot)
	if encodedModelSize(t, first.Snapshot) <= budget {
		t.Fatal("fixture did not shrink")
	}
	got, _, err := ExportModel(first, u.Recipient, u, key, testNow(), budget, save)
	if err != nil || modelDigest(got.Snapshot) != modelDigest(second.Snapshot) {
		t.Fatal("shrinking export rejected", err)
	}
	if _, _, err := ExportModel(first, u.Recipient, u, key, testNow(), budget-1, save); err != ErrCapacity {
		t.Fatal("undersized export budget", err)
	}
}

func TestPendingFenceOnlyBlocksAffectedEligibility(t *testing.T) {
	s, e, _ := modelFixture(t)
	p := *s.Peers[0].PairContext
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, 32))
	tunnel, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	p.HostKey = hex.EncodeToString(key.Public().(ed25519.PublicKey))
	p.HostTunnelKey = hex.EncodeToString(tunnel.PublicKey().Bytes())
	p.HostNonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{44}, 32))
	p.HostEndpoint = "127.0.0.8:20008"
	state, err := InitialState(p, s.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	u := e.Update
	u.PairBinding = state.PairBinding
	u.Issuer, u.IssuerTunnelKey = p.HostKey, p.HostTunnelKey
	u.PriorEndpoint, u.Endpoint = p.HostEndpoint, "127.0.0.9:20009"
	second, err := Sign(u, key)
	if err != nil {
		t.Fatal(err)
	}
	s.Peers = append(s.Peers, PeerRecord{Peer: PeerWire{Key: p.HostKey, Endpoint: p.HostEndpoint, TunnelKey: p.HostTunnelKey}, Revision: "1", PairContext: &p, ContextConfirmed: true, EndpointState: &state})
	s = accept(t, s, e, testNow())
	s = accept(t, s, second, testNow())
	firstBinding := s.Peers[0].EndpointState.PairBinding
	secondBinding := s.Peers[1].EndpointState.PairBinding
	red, err := ProposeReduction(s, firstBinding, "revoke", testNow())
	if err != nil {
		t.Fatal(err)
	}
	pending := stage(t, s, red.Mutation, testNow())
	if SavedEligibility(pending, firstBinding, testNow()) || !SavedEligibility(pending, secondBinding, testNow()) {
		t.Fatal("peer fence did not preserve unrelated eligibility")
	}
	bad := cloneSnapshot(pending)
	bad.PendingChange.PairBindings = []string{secondBinding}
	if SavedEligibility(bad, firstBinding, testNow()) || SavedEligibility(bad, secondBinding, testNow()) {
		t.Fatal("unvalidated affected bindings trusted")
	}
	local := stage(t, s, Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.7:20007"}, testNow())
	if SavedEligibility(local, firstBinding, testNow()) || SavedEligibility(local, secondBinding, testNow()) {
		t.Fatal("local endpoint fence did not include all managed pairs")
	}
}

func TestSizingTimeDecoderMatchesCanonicalInstants(t *testing.T) {
	values := []string{"", "0001-01-01T00:00:00Z", "0000-01-01T00:00:00Z", "2030-02-29T00:00:00Z", "2032-02-29T00:00:00Z", "2030-01-01T24:00:00Z", "2030-01-01T00:00:60Z", "2030-01-01T00:00:00.0Z", "2030-01-01T00:00:00.01Z", "2030-01-01T00:00:00.1234567890Z", "2030-01-01T00:00:00+00:00"}
	for _, nanos := range []int{0, 1, 10, 1000, 120000000, 999999999} {
		values = append(values, time.Date(2030, 10, 2, 13, 24, 35, nanos, time.UTC).Format(time.RFC3339Nano))
	}
	for _, text := range values {
		want, err := instant(text)
		got, ok := instantForSize(text)
		if ok != (err == nil) || ok && !got.Equal(want) {
			t.Fatal("sizing decoder disagrees", text, got, want, err)
		}
	}
}

func TestMalformedTransitionTargetRejectsWithoutAllocation(t *testing.T) {
	s, e, key := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	missingBinding := stage(t, s, m, testNow())
	missingBinding.PendingChange.Mutation.PairBinding = strings.Repeat("0", 64)
	missingMutation := stage(t, s, m, testNow())
	missingMutation.PendingChange.Mutation.State = nil
	missingState := cloneSnapshot(s)
	missingState.Peers[0].EndpointState = nil
	missingPeer := strings.Repeat("0", 64)
	noSave := func([]byte) (bool, error) {
		t.Fatal("malformed target reached save")
		return false, nil
	}
	for name, run := range map[string]func() error{
		"finish missing binding": func() error {
			_, err := FinishPending(missingBinding, false, testNow(), 10)
			return err
		},
		"finish missing state": func() error {
			_, err := FinishPending(missingMutation, false, testNow(), 10)
			return err
		},
		"export missing peer": func() error {
			_, _, err := ExportModel(SaveResolution{Snapshot: s, Durable: true}, missingPeer, e.Update, key, testNow(), 10, noSave)
			return err
		},
		"export missing state": func() error {
			_, _, err := ExportModel(SaveResolution{Snapshot: missingState, Durable: true}, s.Peers[0].Peer.Key, e.Update, key, testNow(), 10, noSave)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if allocs := testing.AllocsPerRun(3, func() {
				if err := run(); err == nil {
					t.Fatal("accepted malformed target")
				}
			}); allocs != 0 {
				t.Fatal("malformed target bypassed bounded preflight", allocs)
			}
		})
	}
}
