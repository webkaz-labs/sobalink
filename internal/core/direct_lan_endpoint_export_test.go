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
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// This fixture constructs only synthetic metadata and a temporary private file.
// It starts no Core lifecycle, backend, listener, goroutine or network operation.
func localEndpointExportFixture(t *testing.T, change func(*directLANState)) (*Core, *directLANStore, string) {
	t.Helper()
	local := directlan.Identity{Seed: strings.Repeat("01", 32)}
	remote := directlan.Identity{Seed: strings.Repeat("02", 32)}
	localScope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
	remoteScope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}
	pair := endpointmeta.PairContext{Version: 1, HostKey: local.PublicKey(), JoinerKey: remote.PublicKey(),
		HostTunnelKey: local.TunnelKey(), JoinerTunnelKey: remote.TunnelKey(),
		HostNonce:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
		JoinerNonce:  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)),
		HostEndpoint: "127.0.0.1:22001", JoinerEndpoint: "127.0.0.2:22002", HostScope: localScope, JoinerScope: remoteScope}
	endpoint, err := endpointmeta.InitialState(pair, local.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	peer := directlan.Peer{Key: remote.PublicKey(), Name: "synthetic-peer", Endpoint: netip.MustParseAddrPort(pair.JoinerEndpoint), TunnelKey: remote.TunnelKey()}
	m := endpointmeta.Snapshot{Version: 3, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: local.PublicKey(), Endpoint: pair.HostEndpoint, TunnelKey: local.TunnelKey()},
		LocalScope: localScope, PreviousLocalEndpoint: pair.HostEndpoint, ObservedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		Peers: []endpointmeta.PeerRecord{{Peer: directLANPeerWire(peer), Revision: "1", PairContext: &pair, ContextConfirmed: true, EndpointState: &endpoint}}}
	state := directLANState{Version: directLANMetadataStateVersion, Identity: local, Selection: DirectLANSelection{Listen: pair.HostEndpoint, Prefixes: localScope.Prefixes}, Peers: []directlan.Peer{peer}, Metadata: &m}
	if change != nil {
		change(&state)
	}
	if err := validateDirectLANState(state); err != nil {
		t.Fatal("invalid synthetic state", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "direct-lan.json")
	if err := config.AtomicWritePrivate(path, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	store, err := readDirectLANStore(path, 1<<20, 16)
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{ctx: context.Background(), lanStartNonce: "synthetic-export-process", directLAN: store}
	return c, store, peer.Key
}

func localEndpointExportCommand(t *testing.T, c *Core, id, name string, input any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return c.Command(context.Background(), webui.Command{RequestID: id, Name: name, Payload: raw})
}

func localEndpointExportReview(t *testing.T, c *Core, input directLANEndpointExportInput, reexport bool) directLANEndpointExportInput {
	t.Helper()
	name := "direct-lan.endpoint.export.preview"
	if reexport {
		name = "direct-lan.endpoint.reexport.preview"
	}
	value, err := localEndpointExportCommand(t, c, "same-preview", name, input)
	if err != nil {
		t.Fatal(err)
	}
	review := value.(directLANEndpointExportReview)
	if review.EndpointUpdatesEnabled || review.Revision == "" {
		t.Fatal("preview claimed runtime authority or omitted revision")
	}
	input.ExpectedRevision = review.Revision
	return input
}

func TestLocalEndpointExportSavesBeforeReleaseAndReexportRepublishes(t *testing.T) {
	c, store, peer := localEndpointExportFixture(t, nil)
	writes := 0
	store.write = func(path string, data []byte) error {
		writes++
		var candidate directLANState
		if err := json.Unmarshal(data, &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.Metadata.Peers[0].EndpointState.IssuedProof == nil || candidate.Metadata.Peers[0].EndpointState.IssuedHighwater != "1" {
			t.Fatal("publisher did not receive the whole issued proof")
		}
		return config.AtomicWritePrivate(path, data)
	}
	input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}, false)
	if writes != 0 {
		t.Fatal("preview wrote state")
	}
	value, err := localEndpointExportCommand(t, c, "issue", "direct-lan.endpoint.export", input)
	if err != nil {
		t.Fatal(err)
	}
	issued := value.(directLANEndpointExportResult)
	if !issued.Saved || issued.Reexport || issued.EndpointUpdatesEnabled || writes != 1 {
		t.Fatal("issued bytes without confirmed publication")
	}
	envelope, err := endpointmeta.ParseUpdateText(issued.Update)
	if err != nil || endpointmeta.Verify(envelope, *store.state.Metadata.Peers[0].PairContext, peer) != nil {
		t.Fatal("exported proof did not match saved context", err)
	}
	before := store.copy()
	beforeFile, _ := os.ReadFile(store.path)
	beforeReview := store.reviewRevision
	replay := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer}, true)
	value, err = localEndpointExportCommand(t, c, "reexport", "direct-lan.endpoint.reexport", replay)
	if err != nil {
		t.Fatal(err)
	}
	afterFile, _ := os.ReadFile(store.path)
	if value.(directLANEndpointExportResult).Update != issued.Update || writes != 2 || store.reviewRevision != beforeReview+1 ||
		!reflect.DeepEqual(before, store.copy()) || !bytes.Equal(beforeFile, afterFile) {
		t.Fatal("re-export did not freshly publish identical saved authority")
	}
	if value, err := localEndpointExportCommand(t, c, "reexport", "direct-lan.endpoint.reexport", replay); err == nil || value != nil || writes != 2 {
		t.Fatal("request cache replayed proof bytes or old review issued again")
	}
	if value, err := localEndpointExportCommand(t, c, "issue", "direct-lan.endpoint.export", input); err == nil || value != nil || writes != 2 {
		t.Fatal("request cache bypassed current issuance review")
	}
	if len(c.requests) != 0 || len(c.inflightRequests) != 0 {
		t.Fatal("endpoint proofs entered request history")
	}
	if _, err := store.runtimeConfig(); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" || c.node != nil {
		t.Fatal("export bypassed managed activation guard", err)
	}
}

func TestLocalEndpointExportFailureReleasesNoProof(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "uncertain"}[published], func(t *testing.T) {
			c, store, peer := localEndpointExportFixture(t, nil)
			input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}, false)
			writes := 0
			store.write = func(path string, data []byte) error {
				writes++
				if published {
					if err := config.AtomicWritePrivate(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted // synthetic durability uncertainty
				}
				return errors.New("synthetic publication failure")
			}
			value, err := localEndpointExportCommand(t, c, "failed-issue", "direct-lan.endpoint.export", input)
			if err == nil || value != nil || !store.needsRecovery() || writes != 1 || errors.Is(err, config.ErrAtomicCommitted) != published {
				t.Fatal("failed export released proof or lost publication outcome", err)
			}
			want := "0"
			if published {
				want = "1"
			}
			if store.state.Metadata.Peers[0].EndpointState.IssuedHighwater != want {
				t.Fatal("wrong in-memory publication adoption")
			}
			if value, err := localEndpointExportCommand(t, c, "failed-issue", "direct-lan.endpoint.export", input); err == nil || value != nil || writes != 1 {
				t.Fatal("retry bypassed recovery or leaked request-history bytes")
			}
		})
	}
}

func TestLocalEndpointReexportAfterReopenRequiresFreshDurability(t *testing.T) {
	c, store, peer := localEndpointExportFixture(t, nil)
	input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "withdraw", Lifetime: "until-revoked"}, false)
	value, err := localEndpointExportCommand(t, c, "initial", "direct-lan.endpoint.export", input)
	if err != nil {
		t.Fatal(err)
	}
	want := value.(directLANEndpointExportResult).Update
	replay := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer}, true)
	store.write = func(path string, data []byte) error {
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	if value, err := localEndpointExportCommand(t, c, "uncertain", "direct-lan.endpoint.reexport", replay); value != nil || !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatal("uncertain re-export released bytes", err)
	}
	for _, fail := range []bool{true, false} {
		fresh, err := readDirectLANStore(store.path, 1<<20, 16)
		if err != nil {
			t.Fatal(err)
		}
		owner := &Core{ctx: context.Background(), lanStartNonce: "synthetic-reopened-owner", directLAN: fresh}
		before := fresh.copy()
		writes := 0
		fresh.write = func(path string, data []byte) error {
			writes++
			if fail {
				return errors.New("synthetic republish failure")
			}
			return config.AtomicWritePrivate(path, data)
		}
		reviewed := localEndpointExportReview(t, owner, directLANEndpointExportInput{PeerID: peer}, true)
		value, err := localEndpointExportCommand(t, owner, "same-reexport", "direct-lan.endpoint.reexport", reviewed)
		if writes != 1 || !reflect.DeepEqual(before, fresh.copy()) {
			t.Fatal("reopened re-export skipped publication or renewed saved state")
		}
		if fail {
			if err == nil || value != nil || !fresh.needsRecovery() {
				t.Fatal("reread was treated as durability")
			}
		} else if err != nil || value.(directLANEndpointExportResult).Update != want {
			t.Fatal("confirmed re-export changed the saved proof", err)
		}
	}
}

func TestLocalEndpointExportRejectsPendingContextAndAuthorityPayload(t *testing.T) {
	c, store, peer := localEndpointExportFixture(t, func(s *directLANState) {
		r := &s.Metadata.Peers[0]
		r.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: r.Peer, PeerRevision: r.Revision,
			OwnNonce: r.PairContext.HostNonce, PrepareDeadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	})
	writes := 0
	store.write = func(string, []byte) error { writes++; return nil }
	if value, err := localEndpointExportCommand(t, c, "pending", "direct-lan.endpoint.export.preview", directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}); err == nil || value != nil || writes != 0 {
		t.Fatal("export accepted a pending context transcript")
	}
	c, store, peer = localEndpointExportFixture(t, nil)
	store.write = func(string, []byte) error { writes++; return nil }
	if value, err := localEndpointExportCommand(t, c, "injected", "direct-lan.endpoint.export.preview", map[string]any{"peerId": peer, "operation": "set", "lifetime": "until-revoked", "sequence": "99"}); err == nil || value != nil || writes != 0 {
		t.Fatal("caller supplied signed authority fields")
	}
	input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}, false)
	c.attemptedNetwork = "direct-lan"
	if value, err := localEndpointExportCommand(t, c, "not-offline", "direct-lan.endpoint.export", input); err == nil || value != nil || writes != 0 {
		t.Fatal("export bypassed the stopped-only gate")
	}
}

func TestLocalEndpointExportFiniteDeadlineBeforeAndDuringPublication(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, store, peer := localEndpointExportFixture(t, nil)
				expires := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
				input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "finite", Expires: expires}, false)
				writes := 0
				store.write = func(path string, data []byte) error {
					writes++
					if err := config.AtomicWritePrivate(path, data); err != nil {
						return err
					}
					time.Sleep(2 * time.Second) // synthetic clock only; no real timer wait
					return nil
				}
				if !during {
					time.Sleep(2 * time.Second)
				}
				value, err := localEndpointExportCommand(t, c, "finite", "direct-lan.endpoint.export", input)
				wantCode := "direct_lan_endpoint_expired"
				if !during {
					// A new body's derived Issued is already at/past the chosen
					// deadline, so structural validity rejects it before signing.
					wantCode = "direct_lan_endpoint_invalid"
				}
				if err == nil || value != nil || networkErrorCode(err) != wantCode {
					t.Fatal("expired new issuance returned bytes", err)
				}
				if !during {
					if writes != 0 || store.state.Metadata.Peers[0].EndpointState.IssuedHighwater != "0" {
						t.Fatal("expired preview consumed a sequence")
					}
					return
				}
				if writes != 1 || store.state.Metadata.Peers[0].EndpointState.IssuedHighwater != "1" {
					t.Fatal("post-publication expiry rolled back issued evidence")
				}
				proof := *store.state.Metadata.Peers[0].EndpointState.IssuedProof
				replay := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer}, true)
				value, err = localEndpointExportCommand(t, c, "expired-replay", "direct-lan.endpoint.reexport", replay)
				if err != nil || writes != 2 {
					t.Fatal("expired evidence could not be republished", err)
				}
				got, err := endpointmeta.ParseUpdateText(value.(directLANEndpointExportResult).Update)
				if err != nil || got != proof || got.Update.Expires != expires {
					t.Fatal("re-export renewed expired evidence", err)
				}
			})
		})
	}
}

func TestLocalEndpointExportPreservesReceivedFollowAuthority(t *testing.T) {
	c, store, peer := localEndpointExportFixture(t, func(s *directLANState) {
		r := &s.Metadata.Peers[0]
		state := r.EndpointState
		scope, _ := s.Metadata.LocalScope.Digest()
		issued := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
		u := endpointmeta.UpdateBody{Version: 1, Domain: endpointmeta.UpdateDomain, PairBinding: state.PairBinding,
			Issuer: r.Peer.Key, Recipient: s.Metadata.LocalPeer.Key, IssuerTunnelKey: r.Peer.TunnelKey,
			RecipientTunnelKey: s.Metadata.LocalPeer.TunnelKey, Sequence: "7", PriorEndpoint: r.Peer.Endpoint,
			Operation: "set", Endpoint: r.Peer.Endpoint, ScopeDigest: scope, Issued: issued, Lifetime: "until-revoked"}
		proof, err := (directlan.Identity{Seed: strings.Repeat("02", 32)}).SignEndpointUpdate(u)
		if err != nil {
			t.Fatal(err)
		}
		digest, _ := proof.Digest()
		state.ReceivedVersion, state.ReceivedHighwater, state.ReceivedProof = 1, "7", &proof
		state.ReceiveStatus, state.AuthorityRevision = "eligible", "2"
		state.Follow = &endpointmeta.FollowApproval{ScopeDigest: scope, Revision: "1", Granted: issued, Lifetime: "until-revoked", Active: true}
		state.Approval = &endpointmeta.Approval{Kind: "follow", ProofDigest: digest, Endpoint: r.Peer.Endpoint, FollowRevision: "1", Granted: issued, Lifetime: "until-revoked"}
	})
	before := *cloneDirectLANMetadata(store.state.Metadata).Peers[0].EndpointState
	input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "withdraw", Lifetime: "finite", Expires: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}, false)
	if _, err := localEndpointExportCommand(t, c, "preserve", "direct-lan.endpoint.export", input); err != nil {
		t.Fatal(err)
	}
	after := *store.state.Metadata.Peers[0].EndpointState
	after.IssuedVersion, after.IssuedHighwater, after.IssuedProof = before.IssuedVersion, before.IssuedHighwater, before.IssuedProof
	if !reflect.DeepEqual(before, after) {
		t.Fatal("outgoing withdrawal changed incoming proof/follow authority")
	}
}

func TestLocalEndpointExportRejectsStaleIntentProcessAndFileReview(t *testing.T) {
	for _, change := range []string{"intent", "process", "file"} {
		t.Run(change, func(t *testing.T) {
			c, store, peer := localEndpointExportFixture(t, nil)
			input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}, false)
			writes := 0
			store.write = func(string, []byte) error { writes++; return nil }
			switch change {
			case "intent":
				input.Operation = "withdraw"
			case "process":
				c.lanStartNonce = "another-synthetic-owner"
			case "file":
				data, err := os.ReadFile(store.path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(store.path, append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if value, err := localEndpointExportCommand(t, c, "stale", "direct-lan.endpoint.export", input); err == nil || value != nil || writes != 0 {
				t.Fatal("stale review released proof or published state")
			}
		})
	}
}

func TestLocalEndpointExportWholeFileAndCounterLimits(t *testing.T) {
	t.Run("whole Core file", func(t *testing.T) {
		c, store, peer := localEndpointExportFixture(t, nil)
		now := time.Now()
		body, err := endpointmeta.PrepareExport(*store.state.Metadata, peer, endpointmeta.ExportOptions{Operation: "set", Lifetime: "until-revoked"}, now, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := store.state.Identity.SignEndpointUpdate(body)
		if err != nil {
			t.Fatal(err)
		}
		next, err := endpointmeta.ProposeIssued(*store.state.Metadata, peer, proof, now, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		compact, _ := json.Marshal(next)
		whole, err := store.stateWithEndpointMetadataLocked(next)
		if err != nil {
			t.Fatal(err)
		}
		indented, _ := json.MarshalIndent(whole, "", "  ")
		current, _ := os.ReadFile(store.path)
		limits := *store.currentCapacity()
		limits.bytes = int64(max(len(current), len(compact)+64))
		store.limits.Store(&limits)
		if store.currentCapacity().bytes != limits.bytes {
			t.Fatal("fixture did not select the whole-file byte budget")
		}
		if int64(len(indented)+1) <= store.currentCapacity().bytes {
			t.Fatal("fixture does not distinguish model and complete-file budgets")
		}
		writes := 0
		store.write = func(string, []byte) error { writes++; return nil }
		input := localEndpointExportReview(t, c, directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}, false)
		if value, err := localEndpointExportCommand(t, c, "full-file", "direct-lan.endpoint.export", input); value != nil || networkErrorCode(err) != "direct_lan_capacity" || writes != 0 {
			t.Fatal("compact model budget bypassed complete private-file budget", err)
		}
	})
	for _, exhausted := range []string{"snapshot", "issued", "process"} {
		t.Run(exhausted, func(t *testing.T) {
			c, store, peer := localEndpointExportFixture(t, func(s *directLANState) {
				if exhausted == "snapshot" {
					s.Metadata.Revision = "18446744073709551615"
				}
				if exhausted == "issued" {
					now := time.Now()
					u, err := endpointmeta.PrepareExport(*s.Metadata, s.Peers[0].Key, endpointmeta.ExportOptions{Operation: "set", Lifetime: "until-revoked"}, now, 1<<20)
					if err != nil {
						t.Fatal(err)
					}
					u.Sequence = "18446744073709551615"
					proof, err := s.Identity.SignEndpointUpdate(u)
					if err != nil {
						t.Fatal(err)
					}
					s.Metadata.Peers[0].EndpointState.IssuedVersion = 1
					s.Metadata.Peers[0].EndpointState.IssuedHighwater = u.Sequence
					s.Metadata.Peers[0].EndpointState.IssuedProof = &proof
				}
			})
			if exhausted == "process" {
				store.reviewRevision = ^uint64(0)
			}
			writes := 0
			store.write = func(string, []byte) error { writes++; return nil }
			if value, err := localEndpointExportCommand(t, c, "counter", "direct-lan.endpoint.export.preview", directLANEndpointExportInput{PeerID: peer, Operation: "set", Lifetime: "until-revoked"}); value != nil || networkErrorCode(err) != "direct_lan_capacity" || writes != 0 {
				t.Fatal("exhausted counter was not rejected before publication", err)
			}
		})
	}
}
