package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Only inert Core/store values, synthetic identities, pure metadata and private
// temporary files are used here. The existing localEndpointExportFixture starts
// no Core lifecycle, constructor, preparer, listener or network work.
func observeManagedProjection(t *testing.T, c *Core, s *directLANStore, now time.Time) (map[string]endpointmeta.PairContext, error) {
	t.Helper()
	before := s.copy()
	file := offlineEndpointRead(t, s.path)
	digest, revision := s.fileDigest, s.reviewRevision
	publication, epoch := s.contextPublication, s.contextEpoch
	s.write = func(string, []byte) error {
		t.Fatal("projection attempted publication")
		return errors.New("publication forbidden")
	}
	c.op.Lock()
	s.mu.Lock()
	contexts, err := s.managedFixedEndpointContextsLocked(now)
	s.mu.Unlock()
	c.op.Unlock()
	if !reflect.DeepEqual(before, s.copy()) || !bytes.Equal(file, offlineEndpointRead(t, s.path)) ||
		digest != s.fileDigest || revision != s.reviewRevision || publication != s.contextPublication || epoch != s.contextEpoch {
		t.Fatal("projection changed saved state or publication evidence")
	}
	return contexts, err
}

func swapManagedProjectionSides(pair *endpointmeta.PairContext) {
	pair.HostKey, pair.JoinerKey = pair.JoinerKey, pair.HostKey
	pair.HostTunnelKey, pair.JoinerTunnelKey = pair.JoinerTunnelKey, pair.HostTunnelKey
	pair.HostEndpoint, pair.JoinerEndpoint = pair.JoinerEndpoint, pair.HostEndpoint
	pair.HostNonce, pair.JoinerNonce = pair.JoinerNonce, pair.HostNonce
	pair.HostScope, pair.JoinerScope = pair.JoinerScope, pair.HostScope
}

func TestManagedFixedEndpointProjectionCopiesBothSidesAndKeepsRuntimeClosed(t *testing.T) {
	for _, role := range []string{"host", "joiner"} {
		t.Run(role, func(t *testing.T) {
			c, s, key := localEndpointExportFixture(t, func(state *directLANState) {
				if role == "joiner" {
					r := &state.Metadata.Peers[0]
					swapManagedProjectionSides(r.PairContext)
					initial, err := endpointmeta.InitialState(*r.PairContext, state.Identity.PublicKey())
					if err != nil {
						t.Fatal(err)
					}
					r.EndpointState = &initial
				}
				other := directlan.Identity{Seed: strings.Repeat("05", 32)}
				legacy := directlan.Peer{Key: other.PublicKey(), TunnelKey: other.TunnelKey(), Name: "synthetic-legacy", Endpoint: netip.MustParseAddrPort("127.0.0.3:22003")}
				state.Peers = append(state.Peers, legacy)
				state.Metadata.Peers = append(state.Metadata.Peers, endpointmeta.PeerRecord{Peer: directLANPeerWire(legacy), Revision: "1"})
			})
			now := time.Now()
			want := *cloneDirectLANMetadata(s.state.Metadata).Peers[0].PairContext
			got, err := observeManagedProjection(t, c, s, now)
			if err != nil || len(got) != 1 || !reflect.DeepEqual(got[key], want) {
				t.Fatal("confirmed initial projection changed classification", err)
			}
			// Mutate both nested slices, the returned value and the map itself.
			pair := got[key]
			pair.HostScope.Prefixes[0] = "127.0.0.0/16"
			pair.JoinerScope.Prefixes[0] = "127.0.0.0/16"
			pair.HostEndpoint = "127.0.0.9:22009"
			got[key] = pair
			delete(got, key)
			again, err := observeManagedProjection(t, c, s, now)
			if err != nil || len(again) != 1 || !reflect.DeepEqual(again[key], want) {
				t.Fatal("caller mutation changed the store projection", err)
			}
			// Also exercise the pure validator's own copy boundary; the observed
			// wrapper's model clone must not mask a shallow structural projection.
			pure, err := projectManagedFixedEndpointContexts(s.state)
			if err != nil {
				t.Fatal(err)
			}
			copy := pure[key]
			copy.HostScope.Prefixes[0], copy.JoinerScope.Prefixes[0] = "127.0.0.0/16", "127.0.0.0/16"
			if !reflect.DeepEqual(*s.state.Metadata.Peers[0].PairContext, want) {
				t.Fatal("structural projection retained scope aliases")
			}
			if !s.endpointObservedAt.Equal(now.UTC()) || s.contextPublication != nil {
				t.Fatal("projection skipped time observation or invented publication")
			}
			if _, err := s.runtimeConfig(); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" {
				t.Fatal("projection opened the managed runtime guard", err)
			}
		})
	}
}

func TestManagedFixedEndpointProjectionPreservesLegacyFormats(t *testing.T) {
	for _, version := range []int{directLANStateVersion, directLANMetadataStateVersion} {
		t.Run(map[int]string{2: "legacy-v2", 3: "legacy-v3"}[version], func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
				state.Version = version
				if version == directLANStateVersion {
					state.Metadata = nil
				} else {
					r := &state.Metadata.Peers[0]
					r.PairContext, r.EndpointState, r.ContextConfirmed = nil, nil, false
				}
			})
			got, err := observeManagedProjection(t, c, s, time.Now())
			if err != nil || got == nil || len(got) != 0 {
				t.Fatal("genuine legacy state was changed or classified as managed", err)
			}
			want, err := directLANConfig(s.copy())
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := s.runtimeConfig()
			if err != nil || !reflect.DeepEqual(cfg, want) {
				t.Fatal("legacy runtime configuration changed", err)
			}
		})
	}
}

func TestManagedFixedEndpointProjectionRejectsEveryUpgradePhase(t *testing.T) {
	for _, phase := range []string{"reviewed", "prepared", "committed", "confirmed-with-upgrade"} {
		t.Run(phase, func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
				r := &state.Metadata.Peers[0]
				pair := *r.PairContext
				if pair.HostKey > pair.JoinerKey {
					swapManagedProjectionSides(&pair)
				}
				nonce := pair.HostNonce
				if state.Identity.PublicKey() == pair.JoinerKey {
					nonce = pair.JoinerNonce
				}
				r.ContextConfirmed = phase == "confirmed-with-upgrade"
				if phase == "committed" {
					return
				}
				r.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: r.Peer, PeerRevision: r.Revision, OwnNonce: nonce, PrepareDeadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
				if phase == "reviewed" || phase == "prepared" {
					r.PairContext, r.EndpointState = nil, nil
				}
				if phase == "prepared" {
					r.UpgradePending.Context = &pair
				}
			})
			got, err := observeManagedProjection(t, c, s, time.Now())
			if got != nil || networkErrorCode(err) != "direct_lan_endpoint_integration_pending" {
				t.Fatal("upgrade evidence became a context or legacy fallback", err)
			}
		})
	}
}

// This signs only fabricated metadata at the unchanged synthetic endpoints.
// There is no endpoint exchange, movement, application traffic or resumption.
func managedProjectionProof(t *testing.T, state *directLANState, issued bool, operation string) endpointmeta.Envelope {
	t.Helper()
	m, r := state.Metadata, state.Metadata.Peers[0]
	issuer, recipient, scope := r.Peer, m.LocalPeer, r.PairContext.HostScope
	identity := directlan.Identity{Seed: strings.Repeat("02", 32)}
	if issued {
		issuer, recipient, scope, identity = m.LocalPeer, r.Peer, r.PairContext.JoinerScope, state.Identity
	}
	digest, err := scope.Digest()
	if err != nil {
		t.Fatal(err)
	}
	when, err := time.Parse(time.RFC3339Nano, m.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	u := endpointmeta.UpdateBody{Version: 1, Domain: endpointmeta.UpdateDomain, PairBinding: r.EndpointState.PairBinding,
		Issuer: issuer.Key, Recipient: recipient.Key, IssuerTunnelKey: issuer.TunnelKey, RecipientTunnelKey: recipient.TunnelKey,
		Sequence: "1", PriorEndpoint: issuer.Endpoint, Operation: operation, Endpoint: issuer.Endpoint, ScopeDigest: digest,
		Issued: m.ObservedAt, Lifetime: "finite", Expires: when.Add(time.Hour).Format(time.RFC3339Nano)}
	if operation == "withdraw" {
		u.Endpoint = ""
	}
	seed, err := hex.DecodeString(identity.Seed)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := endpointmeta.Sign(u, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestManagedFixedEndpointProjectionRejectsValidNoninitialHistory(t *testing.T) {
	for _, history := range []string{"authority-revision", "follow-active", "follow-inactive", "issued-set", "issued-withdraw", "received-approved", "received-revoked", "received-expired", "received-cancelled", "received-withdraw", "previous-local", "selected-local"} {
		t.Run(history, func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
				m, e := state.Metadata, state.Metadata.Peers[0].EndpointState
				switch history {
				case "authority-revision":
					e.AuthorityRevision = "1"
				case "follow-active", "follow-inactive":
					digest, err := m.LocalScope.Digest()
					if err != nil {
						t.Fatal(err)
					}
					e.AuthorityRevision = "1"
					e.Follow = &endpointmeta.FollowApproval{ScopeDigest: digest, Revision: "1", Granted: m.ObservedAt, Lifetime: "until-revoked", Active: history == "follow-active"}
				case "previous-local":
					m.PreviousLocalEndpoint = "127.0.0.4:22004"
				case "selected-local":
					state.Selection.Listen = "127.0.0.4:22004"
					m.LocalPeer.Endpoint, m.PreviousLocalEndpoint = state.Selection.Listen, state.Selection.Listen
				default:
					issued, operation := strings.HasPrefix(history, "issued-"), "set"
					if strings.HasSuffix(history, "withdraw") {
						operation = "withdraw"
					}
					proof := managedProjectionProof(t, state, issued, operation)
					if issued {
						e.IssuedVersion, e.IssuedHighwater, e.IssuedProof = 1, "1", &proof
						return
					}
					e.ReceivedVersion, e.ReceivedHighwater, e.ReceivedProof, e.AuthorityRevision = 1, "1", &proof, "1"
					e.ReceiveStatus = map[string]string{"received-approved": "eligible", "received-revoked": "locally_revoked", "received-expired": "expired", "received-cancelled": "cancelled", "received-withdraw": "withdrawn"}[history]
					if history == "received-approved" {
						digest, err := proof.Digest()
						if err != nil {
							t.Fatal(err)
						}
						e.Approval = &endpointmeta.Approval{Kind: "exact", ProofDigest: digest, Endpoint: proof.Update.Endpoint, Granted: proof.Update.Issued, Lifetime: "finite", Expires: proof.Update.Expires}
					}
				}
			})
			got, err := observeManagedProjection(t, c, s, time.Now())
			if got != nil || err == nil {
				t.Fatal("noninitial state projected at an unchanged endpoint")
			}
			if history == "received-approved" && len(s.endpointDeadlines) != 2 {
				t.Fatal("rejection bypassed existing proof/approval observations")
			}
		})
	}
}

func TestManagedFixedEndpointProjectionRejectsMismatchedStructure(t *testing.T) {
	_, store, _ := localEndpointExportFixture(t, nil)
	other := directlan.Identity{Seed: strings.Repeat("05", 32)}
	for _, field := range []string{"local-signing-key", "remote-signing-key", "local-tunnel-key", "remote-tunnel-key", "local-endpoint", "remote-endpoint", "local-scope", "remote-scope", "noncanonical-scope", "peer-dto", "local-selection", "missing-context", "missing-endpoint-state", "unknown-version"} {
		t.Run(field, func(t *testing.T) {
			state := store.copy()
			r := &state.Metadata.Peers[0]
			switch field {
			case "local-signing-key":
				r.PairContext.HostKey = other.PublicKey()
			case "remote-signing-key":
				r.PairContext.JoinerKey = other.PublicKey()
			case "local-tunnel-key":
				r.PairContext.HostTunnelKey = other.TunnelKey()
			case "remote-tunnel-key":
				r.PairContext.JoinerTunnelKey = other.TunnelKey()
			case "local-endpoint":
				r.PairContext.HostEndpoint = "127.0.0.4:22004"
			case "remote-endpoint":
				r.PairContext.JoinerEndpoint = "127.0.0.4:22004"
			case "local-scope":
				r.PairContext.HostScope.Prefixes = []string{"127.0.0.0/16"}
			case "remote-scope":
				r.PairContext.JoinerScope.Prefixes = []string{"127.0.0.0/16"}
			case "noncanonical-scope":
				r.PairContext.JoinerScope.Prefixes = []string{"127.0.0.0/24", "127.0.0.0/16"}
			case "peer-dto":
				state.Peers[0].Endpoint = netip.MustParseAddrPort("127.0.0.4:22004")
			case "local-selection":
				state.Selection.Listen = "127.0.0.4:22004"
			case "missing-context":
				r.PairContext = nil
			case "missing-endpoint-state":
				r.EndpointState = nil
			case "unknown-version":
				state.Version = 4
			}
			before := cloneDirectLANState(state)
			got, err := projectManagedFixedEndpointContexts(state)
			if got != nil || err == nil || !reflect.DeepEqual(before, state) {
				t.Fatal("mismatched structural input was accepted or changed", err)
			}
		})
	}
}

func TestManagedFixedEndpointProjectionRejectsWholeMixedProjection(t *testing.T) {
	c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
		other := directlan.Identity{Seed: strings.Repeat("05", 32)}
		r := cloneDirectLANMetadata(state.Metadata).Peers[0]
		r.Peer.Key, r.Peer.TunnelKey, r.Peer.Endpoint = other.PublicKey(), other.TunnelKey(), "127.0.0.3:22003"
		r.PairContext.JoinerKey, r.PairContext.JoinerTunnelKey, r.PairContext.JoinerEndpoint = r.Peer.Key, r.Peer.TunnelKey, r.Peer.Endpoint
		initial, err := endpointmeta.InitialState(*r.PairContext, state.Identity.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		r.EndpointState, r.ContextConfirmed = &initial, false
		dto, err := directLANPeerDTO(r.Peer)
		if err != nil {
			t.Fatal(err)
		}
		state.Peers = append(state.Peers, dto)
		state.Metadata.Peers = append(state.Metadata.Peers, r)
	})
	if got, err := observeManagedProjection(t, c, s, time.Now()); err == nil || got != nil {
		t.Fatal("unsupported second pair leaked a partial first-pair projection", err)
	}
}

func TestManagedFixedEndpointProjectionKeepsStoreGuards(t *testing.T) {
	for _, version := range []int{directLANStateVersion, directLANMetadataStateVersion} {
		for _, guard := range []string{"recovery", "zero-clock", "clock-rollback", "changed-file", "changed-memory", "file-budget"} {
			t.Run(map[int]string{2: "v2/", 3: "v3/"}[version]+guard, func(t *testing.T) {
				c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
					if version == directLANStateVersion {
						state.Version, state.Metadata = version, nil
					}
				})
				now := time.Now()
				switch guard {
				case "recovery":
					s.recovery = true
				case "zero-clock":
					now = time.Time{}
				case "clock-rollback":
					if _, err := observeManagedProjection(t, c, s, now.Add(time.Minute)); err != nil {
						t.Fatal(err)
					}
				case "changed-file":
					if err := config.AtomicWritePrivate(s.path, append(offlineEndpointRead(t, s.path), '\n')); err != nil {
						t.Fatal(err)
					}
				case "changed-memory":
					s.state.Peers[0].Name = "synthetic-changed-peer"
				case "file-budget":
					info, err := os.Stat(s.path)
					if err != nil {
						t.Fatal(err)
					}
					s.bytes = info.Size() - 1
				}
				got, err := observeManagedProjection(t, c, s, now)
				if err == nil || got != nil || !s.recovery {
					t.Fatal("store guard did not reject and retain recovery", err)
				}
				if got, err := observeManagedProjection(t, c, s, now.Add(2*time.Minute)); err == nil || got != nil || !s.recovery {
					t.Fatal("later observation escaped recovery", err)
				}
			})
		}
	}
}

func TestManagedFixedEndpointProjectionRejectsSavedFutureAndFence(t *testing.T) {
	for _, guard := range []string{"saved-future", "pending-fence"} {
		t.Run(guard, func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) {
				now := time.Now().UTC()
				if guard == "saved-future" {
					state.Metadata.ObservedAt = now.Add(time.Hour).Format(time.RFC3339Nano)
					return
				}
				m := *state.Metadata
				digest, err := m.LocalScope.Digest()
				if err != nil {
					t.Fatal(err)
				}
				follow := endpointmeta.FollowApproval{ScopeDigest: digest, Revision: "1", Granted: now.Format(time.RFC3339Nano), Lifetime: "until-revoked", Active: true}
				mutation, err := endpointmeta.ProposeFollow(m, m.Peers[0].EndpointState.PairBinding, follow, now)
				if err != nil {
					t.Fatal(err)
				}
				review, err := endpointmeta.PreviewMutation(m, mutation)
				if err != nil {
					t.Fatal(err)
				}
				fenced, err := endpointmeta.Fence(m, mutation, review, m.Peers[0].PairContext.HostNonce, now, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				state.Metadata = &fenced
			})
			// File loading already latches these conditions. The observation
			// wrapper must keep them closed without reconciling their metadata.
			if !s.recovery {
				t.Fatal("fixture did not reopen with recovery")
			}
			if got, err := observeManagedProjection(t, c, s, time.Now()); got != nil || !errors.Is(err, directlan.ErrRecovery) || !s.recovery {
				t.Fatal("saved guard escaped recovery", err)
			}
			if guard == "pending-fence" {
				if got, err := projectManagedFixedEndpointContexts(s.state); got != nil || !errors.Is(err, directlan.ErrRecovery) {
					t.Fatal("structural projection ignored a saved fence", err)
				}
			}
		})
	}
}
