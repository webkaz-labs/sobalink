package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Synthetic source fixtures only. No lifecycle, Node, listener or network is
// constructed. Publication tests inject a memory-only writer; fixture files
// serve read-only admission checks, not AtomicWrite/durability evidence.
func pairRecordFixture(t *testing.T) (directLANState, time.Time) {
	t.Helper()
	now := time.Date(2030, 1, 2, 3, 4, 5, 123456789, time.UTC)
	local := directlan.Identity{Seed: strings.Repeat("01", 32)}
	remote := directlan.Identity{Seed: strings.Repeat("02", 32)}
	scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
	pair := endpointmeta.PairContext{Version: 1, HostKey: local.PublicKey(), JoinerKey: remote.PublicKey(), HostTunnelKey: local.TunnelKey(), JoinerTunnelKey: remote.TunnelKey(), HostNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)), JoinerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)), HostEndpoint: "127.0.0.1:22001", JoinerEndpoint: "127.0.0.2:22002", HostScope: scope, JoinerScope: scope}
	endpoint, err := endpointmeta.InitialState(pair, local.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	peer := directlan.Peer{Key: remote.PublicKey(), Name: "synthetic-peer", Endpoint: netip.MustParseAddrPort(pair.JoinerEndpoint), TunnelKey: remote.TunnelKey()}
	m := endpointmeta.Snapshot{Version: 3, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: local.PublicKey(), Endpoint: pair.HostEndpoint, TunnelKey: local.TunnelKey()}, LocalScope: scope, PreviousLocalEndpoint: pair.HostEndpoint, ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{{Peer: directLANPeerWire(peer), Revision: "1", PairContext: &pair, ContextConfirmed: true, EndpointState: &endpoint}}}
	state := directLANState{Version: 3, Identity: local, Selection: DirectLANSelection{Listen: pair.HostEndpoint, Prefixes: scope.Prefixes}, Peers: []directlan.Peer{peer}, Metadata: &m}
	if err := validateDirectLANState(state); err != nil {
		t.Fatal(err)
	}
	return state, now
}

func pairRecordV4(t *testing.T, state directLANState, now time.Time, revoke bool) directLANState {
	t.Helper()
	m, err := endpointmeta.MigrateManagedV4(*state.Metadata, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if revoke {
		m, _, err = endpointmeta.RevokeManagedPairV4(m, m.Peers[0], now, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
	}
	state = cloneDirectLANState(state)
	state.Version, state.Metadata = 4, &m
	return state
}

func pairRecordStoreFixture(t *testing.T, state directLANState) (*Core, *directLANStore) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(t.TempDir(), "synthetic-state.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s := &directLANStore{path: path, state: cloneDirectLANState(state), fileDigest: directLANFileDigest(data), bytes: 1 << 20, peers: 16}
	c := &Core{ctx: context.Background(), lanStartNonce: "synthetic-process", directLAN: s}
	return c, s
}

func TestPairRecordSchemaAndClosedEntryMatrix(t *testing.T) {
	original, now := pairRecordFixture(t)
	for _, kind := range []string{"empty", "legacy", "managed", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			before := cloneDirectLANState(original)
			if kind == "empty" {
				before.Peers = []directlan.Peer{}
				before.Metadata.Peers = []endpointmeta.PeerRecord{}
			}
			if kind == "legacy" {
				r := &before.Metadata.Peers[0]
				r.PairContext = nil
				r.EndpointState = nil
				r.ContextConfirmed = false
			}
			state := pairRecordV4(t, before, now, kind == "terminal")
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			var reopened directLANState
			if err = json.Unmarshal(data, &reopened); err != nil || !reflect.DeepEqual(state, reopened) {
				t.Fatal("lossy v4 read", err)
			}
			if _, err = projectManagedFixedEndpointContexts(state); err == nil {
				t.Fatal("v4 projection")
			}
			c, s := pairRecordStoreFixture(t, state)
			s.write = func(string, []byte) error { t.Fatal("unexpected publisher"); return nil }
			if _, err = s.runtimeConfig(); err == nil {
				t.Fatal("v4 runtime")
			}
			if _, err = s.prepareLegacyEditLocked(cloneDirectLANState(state), now); err == nil {
				t.Fatal("v4 persist")
			}
			if _, err = s.migrationCandidateLocked(now); err == nil {
				t.Fatal("v4 downgraded")
			}
			if _, err = s.migrationReviewLocked(c.lanStartNonce); err == nil {
				t.Fatal("v4 public review")
			}
			if _, err = c.directLANMigrationCommand(context.Background(), "direct-lan.migration.review", json.RawMessage(`{}`)); err == nil {
				t.Fatal("v4 public migration")
			}
			if err = c.revokeDirectLANPeer("synthetic-target"); err == nil {
				t.Fatal("v4 public revoke")
			}
			if !reflect.DeepEqual(state, s.state) || s.contextPublication != nil {
				t.Fatal("guard side effect")
			}
			for _, version := range []int{3, 4} {
				mixed := cloneDirectLANState(state)
				mixed.Version = version
				mixed.Metadata.Version = 7 - version
				if _, err = json.Marshal(mixed); err == nil {
					t.Fatal("mixed marshal")
				}
				if err = validateDirectLANState(mixed); err == nil {
					t.Fatal("mixed validation")
				}
			}
		})
	}
	// Genuine old formats still roundtrip and project; public v2 migration remains v3.
	for _, version := range []int{2, 3} {
		state := cloneDirectLANState(original)
		state.Version = version
		if version == 2 {
			state.Metadata = nil
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var got directLANState
		if err = json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		again, err := json.Marshal(got)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatal("legacy bytes", err)
		}
		if _, err = projectManagedFixedEndpointContexts(got); err != nil {
			t.Fatal("old projection", err)
		}
		if version == 2 {
			s := &directLANStore{state: state}
			migrated, err := s.migrationCandidateLocked(now)
			if err != nil || migrated.Version != 3 {
				t.Fatal("legacy migration", err)
			}
		}
	}
}

func TestPairRecordIndependentDelta(t *testing.T) {
	before, now := pairRecordFixture(t)
	next := pairRecordV4(t, before, now, false)
	in := pairRecordInputs{operation: pairRecordMigrate}
	if err := validatePairRecordDelta(before, next, in, true, now); err != nil {
		t.Fatal(err)
	}
	edits := map[string]func(*directLANState){
		"identity":      func(s *directLANState) { s.Identity.Seed = strings.Repeat("09", 32) },
		"selection":     func(s *directLANState) { s.Selection.Listen = "127.0.0.1:22009" },
		"dto":           func(s *directLANState) { s.Peers[0].Name = "changed" },
		"peer revision": func(s *directLANState) { s.Metadata.Peers[0].Revision = "2" },
		"confirmation":  func(s *directLANState) { s.Metadata.Peers[0].ContextConfirmed = false },
		"endpoint":      func(s *directLANState) { s.Metadata.Peers[0].EndpointState.IssuedHighwater = "1" },
		"nonce":         func(s *directLANState) { s.Metadata.Peers[0].PairContext.HostNonce = "changed" },
		"observed":      func(s *directLANState) { s.Metadata.ObservedAt = now.Add(time.Second).Format(time.RFC3339Nano) },
		"local":         func(s *directLANState) { s.Metadata.PreviousLocalEndpoint = "127.0.0.1:22009" },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			bad := cloneDirectLANState(next)
			edit(&bad)
			if validatePairRecordDelta(before, bad, in, true, now) == nil {
				t.Fatal("unauthorized delta")
			}
		})
	}
	revoked := pairRecordV4(t, before, now, true)
	in = pairRecordInputs{operation: pairRecordRevoke, peer: next.Peers[0].Key}
	if err := validatePairRecordDelta(next, revoked, in, true, now); err != nil {
		t.Fatal(err)
	}
	if err := validatePairRecordDelta(revoked, cloneDirectLANState(revoked), in, false, now.Add(time.Hour)); err != nil {
		t.Fatal("no-op", err)
	}
	bad := cloneDirectLANState(revoked)
	bad.Metadata.Peers[0].EndpointState = nil
	if validatePairRecordDelta(next, bad, in, true, now) == nil {
		t.Fatal("lost evidence")
	}
}

func TestPairRecordFrozenAdmissionAndOfflineGuards(t *testing.T) {
	state, now := pairRecordFixture(t)
	state = pairRecordV4(t, state, now, false)
	c, s := pairRecordStoreFixture(t, state)
	in := pairRecordInputs{operation: pairRecordRevoke, peer: state.Peers[0].Key}
	a, err := c.capturePairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.matchPairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, a, now); err != nil {
		t.Fatal(err)
	}
	edits := map[string]func(*pairRecordAdmission){
		"store":           func(a *pairRecordAdmission) { a.store = &directLANStore{} },
		"core":            func(a *pairRecordAdmission) { a.core = &Core{} },
		"process":         func(a *pairRecordAdmission) { a.process = "other" },
		"path":            func(a *pairRecordAdmission) { a.path = "other" },
		"file":            func(a *pairRecordAdmission) { a.file = "other" },
		"state":           func(a *pairRecordAdmission) { a.state = "other" },
		"write revision":  func(a *pairRecordAdmission) { a.writeRevision++ },
		"bytes":           func(a *pairRecordAdmission) { a.capacity.bytes-- },
		"peers":           func(a *pairRecordAdmission) { a.capacity.peers-- },
		"operation":       func(a *pairRecordAdmission) { a.inputs.operation = pairRecordMigrate },
		"republish":       func(a *pairRecordAdmission) { a.inputs.republish = true },
		"peer":            func(a *pairRecordAdmission) { a.inputs.peer = "other" },
		"target revision": func(a *pairRecordAdmission) { a.target.Revision = "9" },
		"binding":         func(a *pairRecordAdmission) { a.binding = "other" },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			bad := a
			edit(&bad)
			if c.matchPairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, bad, now) == nil {
				t.Fatal("stale admission")
			}
		})
	}
	c.node = &directLANBackend{}
	if c.pairRecordOwnerLocked(context.Background(), s, c.lanStartNonce) == nil {
		t.Fatal("ordinary owner")
	}
	c.node = nil
	c.contextControl = &contextControlOwner{}
	if c.pairRecordOwnerLocked(context.Background(), s, c.lanStartNonce) == nil {
		t.Fatal("context owner")
	}
	c.contextControl = nil
	c.attemptedNetwork = "synthetic"
	if c.pairRecordOwnerLocked(context.Background(), s, c.lanStartNonce) == nil {
		t.Fatal("network attempted")
	}
	c.attemptedNetwork = ""
	c.closing = true
	if c.pairRecordOwnerLocked(context.Background(), s, c.lanStartNonce) == nil {
		t.Fatal("closing")
	}
	c.closing = false
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if c.pairRecordOwnerLocked(cancelled, s, c.lanStartNonce) == nil {
		t.Fatal("caller cancellation")
	}
	c.ctx = cancelled
	if c.pairRecordOwnerLocked(context.Background(), s, c.lanStartNonce) == nil {
		t.Fatal("Core cancellation")
	}
	c.ctx = context.Background()
	s.recovery = true
	if _, err = s.pairRecordModelLocked(now); err == nil {
		t.Fatal("recovery")
	}
}

func TestPairRecordPublisherOutcomesAndLateCancellation(t *testing.T) {
	before, now := pairRecordFixture(t)
	before = pairRecordV4(t, before, now, false)
	next := cloneDirectLANState(before)
	m, _, err := endpointmeta.RevokeManagedPairV4(*before.Metadata, before.Metadata.Peers[0], now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	next.Metadata = &m
	for _, kind := range []string{"success", "unpublished", "uncertain", "late caller cancel", "late Core cancel", "before admission"} {
		t.Run(kind, func(t *testing.T) {
			_, s := pairRecordStoreFixture(t, before)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			coreCtx, coreCancel := context.WithCancel(context.Background())
			defer coreCancel()
			calls := 0
			s.contextPublication = &contextPublicationReceipt{store: s}
			s.contextEpoch = &directlan.ContextEpoch{} // inert zero value, no runtime constructor
			s.write = func(_ string, data []byte) error {
				calls++
				if len(data) == 0 || data[len(data)-1] != '\n' {
					t.Fatal("whole file newline")
				}
				switch kind {
				case "unpublished":
					return errors.New("synthetic failure")
				case "uncertain":
					return config.ErrAtomicCommitted
				case "late caller cancel":
					cancel()
				case "late Core cancel":
					coreCancel()
				}
				return nil
			}
			if kind == "before admission" {
				cancel()
			}
			live := &contextSaveLiveness{ctx: pairRecordContext{Context: ctx, core: coreCtx}}
			got, err := s.publishPairRecordLocked(next, true, live)
			published := kind != "unpublished" && kind != "before admission"
			durable := published && kind != "uncertain"
			if got.published != published || got.durable != durable || got.changed != published {
				t.Fatalf("outcome: %+v %v", got, err)
			}
			if (kind == "success") != (err == nil) {
				t.Fatal("error", err)
			}
			if (kind == "before admission" && calls != 0) || (kind != "before admission" && calls != 1) {
				t.Fatal("writer count", calls)
			}
			want := before
			if published {
				want = next
			}
			if !reflect.DeepEqual(s.state, want) {
				t.Fatal("adoption")
			}
			if s.recovery != (kind == "unpublished" || kind == "uncertain") {
				t.Fatal("recovery")
			}
			if s.contextPublication != nil || s.contextEpoch != nil || s.reviewRevision != 1 {
				t.Fatal("old authority survived")
			}
		})
	}
}

func TestPairRecordBudgetRevisionAndExactRepublish(t *testing.T) {
	before, now := pairRecordFixture(t)
	before = pairRecordV4(t, before, now, true)
	c, s := pairRecordStoreFixture(t, before)
	in := pairRecordInputs{operation: pairRecordRevoke, peer: before.Peers[0].Key}
	a, err := c.capturePairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.write = func(string, []byte) error { calls++; return nil }
	got, err := c.savePairRecordLocked(context.Background(), s, c.lanStartNonce, a, now)
	if err != nil || got != (pairRecordSaveResult{}) || calls != 0 {
		t.Fatal("no-op is not publication", got, err)
	}
	in.republish = true
	a, err = c.capturePairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, in, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.savePairRecordLocked(context.Background(), s, c.lanStartNonce, a, now)
	if err != nil || got != (pairRecordSaveResult{published: true, durable: true}) || calls != 1 || !reflect.DeepEqual(before, s.state) {
		t.Fatal("exact republish", got, err)
	}
	s.reviewRevision = ^uint64(0)
	if _, _, err = s.pairRecordCandidateLocked(a, now); err == nil {
		t.Fatal("publication revision overflow")
	}
	s.reviewRevision = 0
	compact, err := endpointmeta.EncodeSnapshot(*before.Metadata, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s.bytes = int64(len(compact))
	if _, _, err = s.pairRecordCandidateLocked(a, now); err == nil {
		t.Fatal("compact-only budget admitted whole file")
	}
}

func TestPairRecordPassiveObservationsPreserveLatches(t *testing.T) {
	state, now := pairRecordFixture(t)
	e := state.Metadata.Peers[0].EndpointState
	scopeDigest, err := state.Metadata.LocalScope.Digest()
	if err != nil {
		t.Fatal(err)
	}
	e.AuthorityRevision = "1"
	e.Follow = &endpointmeta.FollowApproval{ScopeDigest: scopeDigest, Revision: "1", Granted: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), Lifetime: "finite", Expires: now.Add(time.Hour).Format(time.RFC3339Nano), Active: true}
	state = pairRecordV4(t, state, now, false)
	c, s := pairRecordStoreFixture(t, state)
	key := directLANEndpointDeadlineKey{binding: e.PairBinding, kind: "follow", revision: "1"}
	prior := directLANEndpointDeadline{abs: e.Follow.Expires, monotonic: now.Add(-time.Second), expired: true}
	s.endpointDeadlines = map[directLANEndpointDeadlineKey]directLANEndpointDeadline{key: prior}
	window := contextPreparationWindow{pair: "synthetic-pair", nonce: "synthetic-nonce", cutoff: now.Add(-time.Hour)}
	s.contextWindows = map[string]contextPreparationWindow{state.Peers[0].Key: window}
	a, err := c.capturePairRecordAdmissionLocked(context.Background(), s, c.lanStartNonce, pairRecordInputs{operation: pairRecordRevoke, peer: state.Peers[0].Key}, now)
	if err != nil {
		t.Fatal("expired authority vetoed terminal reduction", err)
	}
	if _, changed, err := s.pairRecordCandidateLocked(a, now); err != nil || !changed {
		t.Fatal("reducer", err)
	}
	if got := s.endpointDeadlines[key]; got != prior {
		t.Fatal("deadline reset", got)
	}
	if s.contextWindows[state.Peers[0].Key] != window {
		t.Fatal("preparation cutoff changed")
	}
	if _, err := s.endpointModelLocked(now, false); err == nil {
		t.Fatal("active observation widened")
	}
	if _, err := s.pairRecordModelLocked(now.Add(-time.Second)); err == nil || !s.recovery {
		t.Fatal("wall rollback not latched")
	}
}
