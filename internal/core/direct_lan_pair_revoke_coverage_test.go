package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Independent expected state, not the production reducer/candidate builder.
// The only permitted fresh delta is version/revision/time and, for removal,
// the target's derived terminal marker. All retained values come from a clone.
func pairRecordExpectedFresh(t *testing.T, before directLANState, in pairRecordInputs, now time.Time) directLANState {
	t.Helper()
	next := cloneDirectLANState(before)
	revision, err := strconv.ParseUint(before.Metadata.Revision, 10, 64)
	if err != nil || revision == ^uint64(0) {
		t.Fatal("invalid fixture revision", err)
	}
	next.Version = 4
	next.Metadata.Version = 4
	next.Metadata.Revision = strconv.FormatUint(revision+1, 10)
	next.Metadata.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	switch in.operation {
	case pairRecordMigrate:
		if before.Version != 3 || in.peer != "" {
			t.Fatal("invalid migration fixture")
		}
	case pairRecordRevoke:
		found := false
		for i := range next.Metadata.Peers {
			r := &next.Metadata.Peers[i]
			if r.Peer.Key != in.peer {
				continue
			}
			if before.Version != 4 || r.PairContext == nil || r.PairRevocation != nil {
				t.Fatal("invalid removal fixture")
			}
			binding, err := r.PairContext.Binding()
			if err != nil {
				t.Fatal(err)
			}
			r.PairRevocation = &endpointmeta.PairRevocation{PairBinding: binding, Revision: next.Metadata.Revision, RevokedAt: next.Metadata.ObservedAt}
			found = true
		}
		if !found {
			t.Fatal("missing fixture peer")
		}
	default:
		t.Fatal("unsupported fixture operation")
	}
	if err := validateDirectLANState(next); err != nil {
		t.Fatal("invalid independently expected state", err)
	}
	return next
}

func TestPairRecordCaptureSaveWriterOutcomes(t *testing.T) {
	for _, operation := range []pairRecordOperation{pairRecordMigrate, pairRecordRevoke} {
		name := "migration"
		if operation == pairRecordRevoke {
			name = "removal"
		}
		t.Run(name, func(t *testing.T) {
			for _, outcome := range []string{"success", "unpublished", "uncertain", "late caller cancel", "late Core cancel"} {
				t.Run(outcome, func(t *testing.T) {
					before, now := pairRecordFixture(t)
					in := pairRecordInputs{operation: operation}
					if operation == pairRecordRevoke {
						before = pairRecordV4(t, before, now, false)
						in.peer = before.Peers[0].Key
					}
					expected := pairRecordExpectedFresh(t, before, in, now)
					expectedBytes, err := json.MarshalIndent(expected, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					expectedBytes = append(expectedBytes, '\n')
					c, s := pairRecordStoreFixture(t, before)
					originalDigest := s.fileDigest
					caller, cancel := context.WithCancel(context.Background())
					defer cancel()
					coreCtx, coreCancel := context.WithCancel(context.Background())
					defer coreCancel()
					c.ctx = coreCtx
					s.reviewRevision = 7
					s.contextEpoch = &directlan.ContextEpoch{} // inert pointer only; no live epoch claim
					s.contextPublication = &contextPublicationReceipt{store: s}
					admission, err := c.capturePairRecordAdmissionLocked(caller, s, c.lanStartNonce, in, now)
					if err != nil {
						t.Fatal("fresh capture", err)
					}
					calls := 0
					s.write = func(path string, data []byte) error {
						calls++
						if path != s.path || !bytes.Equal(data, expectedBytes) {
							t.Fatal("unexpected full-file publication candidate")
						}
						switch outcome {
						case "unpublished":
							return errors.New("synthetic unpublished failure")
						case "uncertain":
							return config.ErrAtomicCommitted
						case "late caller cancel":
							cancel()
						case "late Core cancel":
							coreCancel()
						}
						return nil // memory-only writer; not a disk durability simulation
					}
					got, err := c.savePairRecordLocked(caller, s, c.lanStartNonce, admission, now)
					published := outcome != "unpublished"
					durable := published && outcome != "uncertain"
					if calls != 1 || got != (pairRecordSaveResult{changed: published, published: published, durable: durable}) {
						t.Fatalf("unexpected result %+v calls=%d error=%v", got, calls, err)
					}
					switch outcome {
					case "success":
						if err != nil {
							t.Fatal(err)
						}
					case "unpublished":
						if !errors.Is(err, directlan.ErrRecovery) || errors.Is(err, config.ErrAtomicCommitted) {
							t.Fatal("wrong unpublished error", err)
						}
					case "uncertain":
						if !errors.Is(err, config.ErrAtomicCommitted) || !errors.Is(err, directlan.ErrRecovery) {
							t.Fatal("lost uncertainty", err)
						}
					default:
						if !errors.Is(err, context.Canceled) {
							t.Fatal("lost late cancellation", err)
						}
					}
					wantState, wantDigest := before, originalDigest
					if published {
						wantState, wantDigest = expected, directLANFileDigest(expectedBytes)
					}
					if !reflect.DeepEqual(s.state, wantState) || s.fileDigest != wantDigest {
						t.Fatal("incorrect state/digest adoption")
					}
					if s.recovery != (outcome == "unpublished" || outcome == "uncertain") {
						t.Fatal("incorrect recovery latch")
					}
					if s.reviewRevision != 8 || s.contextEpoch != nil || s.contextPublication != nil {
						t.Fatal("publication did not invalidate prior authority")
					}
					// Exactly one attempt per fixture. Fake writes do not update the read-only
					// fixture file, and cannot support a subsequent admission or reopen claim.
				})
			}
		})
	}
}

func TestPairRecordCaptureSaveRejectsChangedAdmission(t *testing.T) {
	for _, kind := range []string{"file bytes", "memory state", "write revision", "byte limit", "peer limit", "caller cancellation", "Core cancellation"} {
		t.Run(kind, func(t *testing.T) {
			before, now := pairRecordFixture(t)
			before = pairRecordV4(t, before, now, false)
			c, s := pairRecordStoreFixture(t, before)
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			coreCtx, coreCancel := context.WithCancel(context.Background())
			defer coreCancel()
			c.ctx = coreCtx
			in := pairRecordInputs{operation: pairRecordRevoke, peer: before.Peers[0].Key}
			admission, err := c.capturePairRecordAdmissionLocked(caller, s, c.lanStartNonce, in, now)
			if err != nil {
				t.Fatal("positive capture", err)
			}
			if err = c.matchPairRecordAdmissionLocked(caller, s, c.lanStartNonce, admission, now); err != nil {
				t.Fatal("positive admission", err)
			}
			switch kind {
			case "file bytes":
				data, err := os.ReadFile(s.path)
				if err != nil {
					t.Fatal(err)
				}
				// Legal trailing whitespace changes the exact file digest without inventing
				// malformed metadata. This is disposable fixture setup, not a publisher.
				if err = os.WriteFile(s.path, append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "memory state":
				s.state.Metadata.ObservedAt = now.Add(time.Nanosecond).Format(time.RFC3339Nano)
			case "write revision":
				s.reviewRevision++
			case "byte limit":
				s.bytes--
			case "peer limit":
				s.peers--
			case "caller cancellation":
				cancel()
			case "Core cancellation":
				coreCancel()
			}
			stateAtSave := cloneDirectLANState(s.state)
			digestAtSave := s.fileDigest
			revisionAtSave := s.reviewRevision
			receipt := &contextPublicationReceipt{store: s}
			epoch := &directlan.ContextEpoch{}
			s.contextPublication, s.contextEpoch = receipt, epoch
			calls := 0
			s.write = func(string, []byte) error { calls++; return nil }
			got, err := c.savePairRecordLocked(caller, s, c.lanStartNonce, admission, now)
			if err == nil || calls != 0 || got != (pairRecordSaveResult{}) {
				t.Fatal("changed admission reached publisher", got, calls, err)
			}
			if kind == "caller cancellation" || kind == "Core cancellation" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("wrong cancellation error", err)
				}
			}
			if !reflect.DeepEqual(s.state, stateAtSave) || s.fileDigest != digestAtSave || s.reviewRevision != revisionAtSave {
				t.Fatal("rejected save adopted state or revision")
			}
			if s.recovery != (kind == "file bytes") {
				t.Fatal("incorrect preflight recovery latch")
			}
			if s.contextPublication != receipt || s.contextEpoch != epoch {
				t.Fatal("preflight reached publication invalidation")
			}
		})
	}
}

// A committed-but-unconfirmed saved context with complete retained preparation
// evidence, plus an unrelated valid legacy record. No preparer API is called.
func pairRecordRetainedEvidenceFixture(t *testing.T) (directLANState, time.Time) {
	t.Helper()
	state, now := pairRecordFixture(t)
	r := &state.Metadata.Peers[0]
	p := r.PairContext
	if p.HostKey > p.JoinerKey {
		p.HostKey, p.JoinerKey = p.JoinerKey, p.HostKey
		p.HostTunnelKey, p.JoinerTunnelKey = p.JoinerTunnelKey, p.HostTunnelKey
		p.HostEndpoint, p.JoinerEndpoint = p.JoinerEndpoint, p.HostEndpoint
		p.HostNonce, p.JoinerNonce = p.JoinerNonce, p.HostNonce
		p.HostScope, p.JoinerScope = p.JoinerScope, p.HostScope
	}
	initial, err := endpointmeta.InitialState(*p, state.Metadata.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	r.EndpointState = &initial
	r.ContextConfirmed = false
	ownNonce := p.HostNonce
	if state.Metadata.LocalPeer.Key == p.JoinerKey {
		ownNonce = p.JoinerNonce
	}
	r.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: r.Peer, PeerRevision: r.Revision, OwnNonce: ownNonce, PrepareDeadline: now.Add(time.Hour).Format(time.RFC3339Nano), Context: p}
	unrelated := directlan.Identity{Seed: strings.Repeat("05", 32)}
	dto := directlan.Peer{Key: unrelated.PublicKey(), Name: "synthetic-unrelated", TunnelKey: unrelated.TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.3:22003")}
	state.Peers = append(state.Peers, dto)
	state.Metadata.Peers = append(state.Metadata.Peers, endpointmeta.PeerRecord{Peer: directLANPeerWire(dto), Revision: "1"})
	if err := validateDirectLANState(state); err != nil {
		t.Fatal("invalid retained evidence fixture", err)
	}
	return pairRecordV4(t, state, now, false), now
}

func TestPairRecordRemovalDeltaRejectsEvidenceChanges(t *testing.T) {
	edits := map[string]func(*directLANState, time.Time){
		"unrelated record": func(s *directLANState, _ time.Time) {
			s.Peers[1].Name = "different synthetic name"
			s.Metadata.Peers[1].Peer.Name = s.Peers[1].Name
		},
		"selected peer revision": func(s *directLANState, _ time.Time) {
			s.Metadata.Peers[0].Revision = "2"
			s.Metadata.Peers[0].UpgradePending.PeerRevision = "2"
		},
		"retained preparation": func(s *directLANState, now time.Time) {
			s.Metadata.Peers[0].UpgradePending.PrepareDeadline = now.Add(2 * time.Hour).Format(time.RFC3339Nano)
		},
		"removed preparation":   func(s *directLANState, _ time.Time) { s.Metadata.Peers[0].UpgradePending = nil },
		"old context nonce":     func(s *directLANState, _ time.Time) { s.Metadata.Peers[0].PairContext.HostNonce = "changed" },
		"old endpoint evidence": func(s *directLANState, _ time.Time) { s.Metadata.Peers[0].EndpointState.AuthorityRevision = "2" },
		"marker binding": func(s *directLANState, _ time.Time) {
			s.Metadata.Peers[0].PairRevocation.PairBinding = strings.Repeat("0", 64)
		},
		"marker time": func(s *directLANState, now time.Time) {
			s.Metadata.Peers[0].PairRevocation.RevokedAt = now.Add(-time.Second).Format(time.RFC3339Nano)
		},
		"marker revision": func(s *directLANState, _ time.Time) { s.Metadata.Peers[0].PairRevocation.Revision = "2" },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			before, now := pairRecordRetainedEvidenceFixture(t)
			in := pairRecordInputs{operation: pairRecordRevoke, peer: before.Peers[0].Key}
			expected := pairRecordExpectedFresh(t, before, in, now)
			if err := validatePairRecordDelta(before, expected, in, true, now); err != nil {
				t.Fatal("independent positive delta", err)
			}
			// Compare a real reducer result with the independently assembled expectation
			// before testing mutation rejection; otherwise all-error fixtures can pass.
			actual, changed, err := endpointmeta.RevokeManagedPairV4(*before.Metadata, before.Metadata.Peers[0], now, 1<<20)
			if err != nil || !changed || !reflect.DeepEqual(actual, *expected.Metadata) {
				t.Fatal("positive reducer control", changed, err)
			}
			bad := cloneDirectLANState(expected)
			edit(&bad, now)
			if reflect.DeepEqual(bad, expected) {
				t.Fatal("ineffective tamper fixture")
			}
			if err := validatePairRecordDelta(before, bad, in, true, now); err == nil {
				t.Fatal("unauthorized evidence delta")
			}
		})
	}
	t.Run("replace existing terminal marker", func(t *testing.T) {
		before, now := pairRecordRetainedEvidenceFixture(t)
		in := pairRecordInputs{operation: pairRecordRevoke, peer: before.Peers[0].Key}
		terminal := pairRecordExpectedFresh(t, before, in, now)
		if err := validatePairRecordDelta(before, terminal, in, true, now); err != nil {
			t.Fatal("positive removal", err)
		}
		unchanged := cloneDirectLANState(terminal)
		if err := validatePairRecordDelta(terminal, unchanged, in, false, now.Add(time.Hour)); err != nil {
			t.Fatal("positive no-op", err)
		}
		bad := cloneDirectLANState(terminal)
		bad.Metadata.Peers[0].PairRevocation.RevokedAt = now.Add(-time.Second).Format(time.RFC3339Nano)
		if err := validateDirectLANState(bad); err != nil {
			t.Fatal("replacement fixture should remain structurally valid", err)
		}
		if err := validatePairRecordDelta(terminal, bad, in, false, now.Add(time.Hour)); err == nil {
			t.Fatal("existing marker replacement accepted as no-op")
		}
		if err := validatePairRecordDelta(terminal, bad, in, true, now.Add(time.Hour)); err == nil {
			t.Fatal("existing marker replacement accepted as fresh removal")
		}
	})
}
