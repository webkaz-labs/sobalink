package core

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These tests inspect synthetic values and read-only fixture files. No Node
// constructor, lifecycle, network, preparer or actual publisher is invoked.
func activeV4MixedFixture(t *testing.T) (directLANState, time.Time) {
	t.Helper()
	original, now := pairRecordFixture(t)
	state := pairRecordV4(t, original, now, true)
	active := cloneDirectLANMetadata(original.Metadata).Peers[0]
	remote := directlan.Identity{Seed: strings.Repeat("05", 32)}
	active.Peer.Key, active.Peer.TunnelKey, active.Peer.Endpoint = remote.PublicKey(), remote.TunnelKey(), "127.0.0.3:22003"
	active.PairContext.JoinerKey, active.PairContext.JoinerTunnelKey, active.PairContext.JoinerEndpoint = active.Peer.Key, active.Peer.TunnelKey, active.Peer.Endpoint
	initial, err := endpointmeta.InitialState(*active.PairContext, state.Metadata.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	active.EndpointState = &initial
	dto, err := directLANPeerDTO(active.Peer)
	if err != nil {
		t.Fatal(err)
	}
	state.Metadata.Peers = append(state.Metadata.Peers, active)
	state.Peers = append(state.Peers, dto)
	legacyID := directlan.Identity{Seed: strings.Repeat("06", 32)}
	legacy := endpointmeta.PeerRecord{Peer: endpointmeta.PeerWire{Key: legacyID.PublicKey(), TunnelKey: legacyID.TunnelKey(), Endpoint: "127.0.0.4:22004", Name: "synthetic-legacy"}, Revision: "1"}
	legacyDTO, err := directLANPeerDTO(legacy.Peer)
	if err != nil {
		t.Fatal(err)
	}
	state.Metadata.Peers = append(state.Metadata.Peers, legacy)
	state.Peers = append(state.Peers, legacyDTO)
	if err := validateDirectLANState(state); err != nil {
		t.Fatal(err)
	}
	return state, now
}

func TestActiveV4ProjectionRetainsEvidenceAndNegativeMembership(t *testing.T) {
	for _, kind := range []string{"active", "mixed", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			state, now := pairRecordFixture(t)
			if kind == "mixed" {
				state, now = activeV4MixedFixture(t)
			} else {
				state = pairRecordV4(t, state, now, kind == "terminal")
			}
			before := cloneDirectLANState(state)
			projected, err := projectManagedFixedEndpoint(state)
			if err != nil {
				t.Fatal(err)
			}
			active, contexts, denied := 1, 1, 0
			if kind == "mixed" {
				active, denied = 2, 1
			}
			if kind == "terminal" {
				active, contexts, denied = 0, 0, 1
			}
			if len(projected.Peers) != active || len(projected.PairContexts) != contexts || len(projected.DeniedPeerKeys) != denied {
				t.Fatal("incorrect classification")
			}
			if denied != 0 {
				projected.DeniedPeerKeys[0] = "changed"
			}
			if active != 0 {
				key := projected.Peers[0].Key
				projected.Peers[0].Name = "changed"
				pair := projected.PairContexts[key]
				pair.HostScope.Prefixes[0] = "changed"
				delete(projected.PairContexts, key)
			}
			if !reflect.DeepEqual(state, before) {
				t.Fatal("projection aliases retained evidence")
			}
			_, store := pairRecordStoreFixture(t, state)
			cfg, digest, err := store.contextConfigLocked(now, 8)
			if err != nil || len(cfg.Peers) != active || digest != contextConfigurationDigest(state) {
				t.Fatal("context active projection", err)
			}
			if _, err := store.runtimeConfig(); err == nil {
				t.Fatal("public runtime opened")
			}
			if _, err := store.prepareLegacyEditLocked(cloneDirectLANState(state), now); err == nil {
				t.Fatal("legacy mutation opened")
			}
		})
	}
	state, now := activeV4MixedFixture(t)
	// Expired, unconfirmed terminal preparation remains evidence, never authority.
	r := &state.Metadata.Peers[0]
	r.ContextConfirmed = false
	pair := *r.PairContext
	if pair.HostKey >= pair.JoinerKey {
		swapManagedProjectionSides(&pair)
	}
	r.PairContext = &pair
	initial, err := endpointmeta.InitialState(pair, state.Metadata.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	r.EndpointState = &initial
	r.PairRevocation.PairBinding = initial.PairBinding
	nonce := pair.HostNonce
	if state.Metadata.LocalPeer.Key == pair.JoinerKey {
		nonce = pair.JoinerNonce
	}
	r.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: r.Peer, PeerRevision: r.Revision, OwnNonce: nonce, PrepareDeadline: now.Add(-time.Hour).Format(time.RFC3339Nano), Context: &pair}
	if _, err := projectManagedFixedEndpoint(state); err != nil {
		t.Fatal("terminal historical phase vetoed active projection", err)
	}
}

func TestActiveV4CoreTerminalBypassMatrix(t *testing.T) {
	state, now := activeV4MixedFixture(t)
	terminal, active := state.Peers[0].Key, state.Peers[1].Key
	_, s := pairRecordStoreFixture(t, state)
	s.write = func(string, []byte) error { t.Fatal("denied target reached publisher"); return nil }
	for _, op := range []contextOperation{contextPrepare, contextResume, contextRecordInbound, contextRecordOutbound, contextCommit, contextConfirmCommit, contextConfirmStatus, contextConfirmUpdate, contextRepublish, contextRepublishPrepared, contextStatusInbound} {
		in := contextInputs{Operation: op, PeerKey: terminal}
		if _, err := s.captureContextAdmissionLocked("synthetic-process", in, now); err == nil {
			t.Fatalf("terminal admission %d", op)
		}
		if _, _, err := s.stateWithContextMetadataLocked(in, contextTranscript{}, now); err == nil {
			t.Fatalf("terminal reduction %d", op)
		}
		if err := validateContextProjection(*state.Metadata, endpointmeta.ContextTransition{Snapshot: *state.Metadata}, in, now); err == nil {
			t.Fatalf("terminal no-op projection %d", op)
		}
	}
	if _, err := contextPeer(*state.Metadata, terminal); err == nil {
		t.Fatal("terminal context lookup")
	}
	if _, err := endpointRecord(*state.Metadata, terminal); err == nil {
		t.Fatal("terminal endpoint lookup")
	}
	if _, err := contextObservation(*state.Metadata, state.Metadata.Peers[0], 1<<20); err == nil {
		t.Fatal("terminal observation")
	}
	if _, err := contextPrepareRequest(*state.Metadata, state.Metadata.Peers[0]); err == nil {
		t.Fatal("terminal prepare request")
	}
	owner := &contextControlOwner{store: s, process: "synthetic-process"}
	s.contextPublication = &contextPublicationReceipt{store: s, process: owner.process, path: s.path, file: s.fileDigest, state: privateRevision(s.state)}
	if err := s.publishContextBeforeArmLocked(context.Background(), owner, terminal, now); err == nil {
		t.Fatal("terminal receipt fast path")
	}
	if _, err := s.contextReplyLocked(owner.process, contextInputs{Operation: contextStatusInbound, PeerKey: terminal}, contextTranscript{}, now); err == nil {
		t.Fatal("terminal reply")
	}

	if _, err := s.captureContextAdmissionLocked("synthetic-process", contextInputs{Operation: contextRepublish, PeerKey: active}, now); err != nil {
		t.Fatal("active v4 republish", err)
	}
	activeOnly := cloneDirectLANState(state)
	activeOnly.Metadata.Peers[0].PairRevocation = nil
	if contextConfigurationDigest(activeOnly) == contextConfigurationDigest(state) {
		t.Fatal("marker-only change missing from digest")
	}
}

func TestActiveV4EndpointProjectionRejectsArbitraryDelta(t *testing.T) {
	state, now := activeV4MixedFixture(t)
	s := &directLANStore{state: state}
	before := *state.Metadata
	mutation := endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.1:22009"}
	review, err := endpointmeta.PreviewMutation(before, mutation)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := endpointmeta.Fence(before, mutation, review, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.stateWithEndpointMetadataLocked(fence); err != nil {
		t.Fatal("active v4 fence", err)
	}
	final, err := endpointmeta.FinishPending(fence, false, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fenced := cloneDirectLANState(state)
	fenced.Metadata = &fence
	s.state = fenced
	if _, err := s.stateWithEndpointMetadataLocked(final); err != nil {
		t.Fatal("active v4 finish", err)
	}
	if !reflect.DeepEqual(before.Peers[0], final.Peers[0]) {
		t.Fatal("terminal record rewritten")
	}
	for _, kind := range []string{"schema", "terminal", "active-name", "revision", "local-history"} {
		bad := *cloneDirectLANMetadata(&final)
		switch kind {
		case "schema":
			bad.Version = 3
		case "terminal":
			bad.Peers[0].Revision = "99"
		case "active-name":
			bad.Peers[1].Peer.Name = "changed"
		case "revision":
			bad.Revision = "99"
		case "local-history":
			bad.PreviousLocalEndpoint = "127.0.0.1:22008"
		}
		if _, err := s.stateWithEndpointMetadataLocked(bad); err == nil {
			t.Fatal("arbitrary delta", kind)
		}
	}
}

func TestActiveV4ContextOwnerRefreshOnlyOwnDurableTransition(t *testing.T) {
	for _, kind := range []string{"success", "uncertain", "cancelled", "owner-replaced", "external", "marker", "transport", "wrong-operation"} {
		t.Run(kind, func(t *testing.T) {
			before, now := pairRecordFixture(t)
			before = pairRecordV4(t, before, now, false)
			before.Metadata.Peers[0].ContextConfirmed = false
			next := cloneDirectLANState(before)
			next.Metadata.Revision = "3"
			next.Metadata.ObservedAt = now.Format(time.RFC3339Nano)
			next.Metadata.Peers[0].Revision = "3"
			next.Metadata.Peers[0].ContextConfirmed = true
			s := &directLANStore{state: next, path: "synthetic-state", fileDigest: "synthetic-file"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			owner := &contextControlOwner{node: &directlan.Node{}, store: s, process: "synthetic-process", ctx: ctx, configuration: contextConfigurationDigest(before)}
			c := &Core{ctx: context.Background(), directLAN: s, lanStartNonce: owner.process, contextControl: owner}
			s.contextPublication = &contextPublicationReceipt{store: s, process: owner.process, path: s.path, file: s.fileDigest, state: privateRevision(s.state)}
			result := contextSaveResult{changed: true, published: true, durable: true}
			in := contextInputs{Operation: contextConfirmCommit, PeerKey: before.Peers[0].Key}
			switch kind {
			case "uncertain":
				result.durable = false
			case "cancelled":
				cancel()
			case "owner-replaced":
				c.contextControl = &contextControlOwner{}
			case "external":
				owner.configuration = "stale"
			case "marker":
				m, _, err := endpointmeta.RevokeManagedPairV4(*next.Metadata, next.Metadata.Peers[0], now, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				s.state.Metadata = &m
				s.contextPublication.state = privateRevision(s.state)
			case "transport":
				s.state.Selection.Listen = "127.0.0.1:22009"
				s.contextPublication.state = privateRevision(s.state)
			case "wrong-operation":
				in.Operation = contextRepublish
			}
			frozen := owner.configuration
			err := c.refreshContextConfigurationLocked(owner, before, in, result, &contextSaveLiveness{ctx: ctx, owner: owner})
			if kind == "success" {
				if err != nil || owner.configuration != contextConfigurationDigest(s.state) {
					t.Fatal("own durable transition", err)
				}
			} else if err == nil || owner.configuration != frozen {
				t.Fatal("invalid refresh", kind, err)
			}
		})
	}
}
