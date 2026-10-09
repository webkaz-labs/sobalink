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

// Fabricated signed metadata only; no transport owner or live movement runs.
func currentCoreProjectionSignedState(t *testing.T, state *directLANState) {
	t.Helper()
	proof := managedProjectionProof(t, state, false, "set")
	proof.Update.Endpoint = "127.0.0.3:22003"
	seed, err := hex.DecodeString(strings.Repeat("02", 32))
	if err != nil {
		t.Fatal(err)
	}
	proof, err = endpointmeta.Sign(proof.Update, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := proof.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r := &state.Metadata.Peers[0]
	e := r.EndpointState
	e.ReceivedVersion, e.ReceivedHighwater, e.ReceivedProof, e.AuthorityRevision = 1, "1", &proof, "1"
	e.LastEndpoint, e.ReceiveStatus = proof.Update.Endpoint, "eligible"
	e.Approval = &endpointmeta.Approval{Kind: "exact", ProofDigest: digest, Endpoint: proof.Update.Endpoint, Granted: proof.Update.Issued, Lifetime: "finite", Expires: proof.Update.Expires}
	r.Peer.Endpoint = proof.Update.Endpoint
	state.Peers[0].Endpoint = netip.MustParseAddrPort(proof.Update.Endpoint)
}

func observeCurrentCoreProjection(t *testing.T, c *Core, s *directLANStore, now time.Time) (directlan.Config, error) {
	t.Helper()
	before, file := s.copy(), offlineEndpointRead(t, s.path)
	publication, epoch, digest, revision := s.contextPublication, s.contextEpoch, s.fileDigest, s.reviewRevision
	s.write = func(string, []byte) error {
		t.Fatal("projection attempted publication")
		return errors.New("forbidden")
	}
	c.op.Lock()
	s.mu.Lock()
	cfg, err := s.managedCurrentEndpointProjectionLocked(now)
	s.mu.Unlock()
	c.op.Unlock()
	if !reflect.DeepEqual(before, s.copy()) || !bytes.Equal(file, offlineEndpointRead(t, s.path)) || publication != s.contextPublication || epoch != s.contextEpoch || digest != s.fileDigest || revision != s.reviewRevision {
		t.Fatal("projection changed evidence or receipt")
	}
	return cfg, err
}

func TestManagedCurrentEndpointProjectionKeepsFixedActivationSeparate(t *testing.T) {
	for _, version := range []int{3, 4} {
		c, s, key := localEndpointExportFixture(t, func(state *directLANState) {
			state.Version, state.Metadata.Version = version, version
			currentCoreProjectionSignedState(t, state)
		})
		now := time.Now()
		cfg, err := observeCurrentCoreProjection(t, c, s, now)
		if err != nil || len(cfg.Peers) != 1 || cfg.Peers[0].Endpoint.String() != "127.0.0.3:22003" || !reflect.DeepEqual(cfg.PairContexts[key], *s.state.Metadata.Peers[0].PairContext) {
			t.Fatal("current projection", err)
		}
		if _, err := projectManagedFixedEndpoint(s.copy()); err == nil {
			t.Fatal("initial fixed contract broadened")
		}
		if node, err := directlan.NewNode(cfg); node != nil || !errors.Is(err, directlan.ErrUnavailable) {
			t.Fatal("inert projection opened constructor", err)
		}
		deadline, _ := time.Parse(time.RFC3339Nano, s.state.Metadata.Peers[0].EndpointState.Approval.Expires)
		expired, err := observeCurrentCoreProjection(t, c, s, deadline)
		if err != nil || len(expired.Peers) != 0 || len(expired.PairContexts) != 0 || !reflect.DeepEqual(expired.InactiveEndpointPeerKeys(), []string{key}) || len(expired.DeniedPeerKeys) != 0 {
			t.Fatal("expired endpoint authority fell back", err)
		}
	}
}

func TestManagedCurrentEndpointProjectionKeepsStoreAndMonotonicGuards(t *testing.T) {
	for _, guard := range []string{"recovery", "zero-clock", "clock-rollback", "changed-file", "changed-memory", "file-budget", "monotonic-proof", "monotonic-approval"} {
		t.Run(guard, func(t *testing.T) {
			c, s, _ := localEndpointExportFixture(t, func(state *directLANState) { currentCoreProjectionSignedState(t, state) })
			now := time.Now()
			switch guard {
			case "recovery":
				s.recovery = true
			case "zero-clock":
				now = time.Time{}
			case "clock-rollback":
				if _, err := observeCurrentCoreProjection(t, c, s, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "changed-file":
				if err := config.AtomicWritePrivate(s.path, append(offlineEndpointRead(t, s.path), '\n')); err != nil {
					t.Fatal(err)
				}
			case "changed-memory":
				s.state.Peers[0].Name = "changed-synthetic"
			case "file-budget":
				info, err := os.Stat(s.path)
				if err != nil {
					t.Fatal(err)
				}
				limits := *s.currentCapacity()
				limits.bytes = info.Size() - 1
				s.limits.Store(&limits)
				if s.currentCapacity().bytes != info.Size()-1 {
					t.Fatal("fixture did not select the below-file byte budget")
				}
			default:
				if _, err := observeCurrentCoreProjection(t, c, s, now); err != nil {
					t.Fatal(err)
				}
				kind := strings.TrimPrefix(guard, "monotonic-")
				for key, deadline := range s.endpointDeadlines {
					if key.kind == kind {
						deadline.expired, deadline.monotonic = true, now.Add(-time.Second)
						s.endpointDeadlines[key] = deadline
					}
				}
			}
			cfg, err := observeCurrentCoreProjection(t, c, s, now)
			if err == nil || len(cfg.Peers) != 0 || len(cfg.PairContexts) != 0 {
				t.Fatal("store guard leaked authority", err)
			}
			if _, err := observeCurrentCoreProjection(t, c, s, now.Add(time.Minute)); err == nil {
				t.Fatal("later observation escaped guard")
			}
		})
	}
}

func TestManagedCurrentEndpointProjectionLegacyAndWholeModelGuards(t *testing.T) {
	_, store, _ := localEndpointExportFixture(t, nil)
	legacy := store.copy()
	legacy.Version, legacy.Metadata = directLANStateVersion, nil
	cfg, err := projectManagedCurrentEndpoint(legacy, time.Now())
	want, wantErr := directLANConfig(legacy)
	if err != nil || wantErr != nil || !reflect.DeepEqual(cfg, want) {
		t.Fatal("legacy projection changed", err)
	}
	beforeLegacy := cloneDirectLANState(legacy)
	cfg.Peers[0].Name = "changed-returned-copy"
	cfg.Peers[0].Endpoint = netip.MustParseAddrPort("127.0.0.9:22009")
	if !reflect.DeepEqual(legacy, beforeLegacy) {
		t.Fatal("legacy projection aliases retained peer evidence")
	}
	for _, guard := range []string{"unconfirmed", "pending", "future", "malformed"} {
		t.Run(guard, func(t *testing.T) {
			state := store.copy()
			switch guard {
			case "unconfirmed":
				state.Metadata.Peers[0].ContextConfirmed = false
			case "pending":
				state.Metadata.PendingChange = &endpointmeta.PendingChange{}
			case "future":
				state.Metadata.ObservedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			case "malformed":
				state.Metadata.Peers[0].EndpointState.PairBinding = "invalid"
			}
			before := cloneDirectLANState(state)
			cfg, err := projectManagedCurrentEndpoint(state, time.Now())
			if err == nil || len(cfg.Peers) != 0 || !reflect.DeepEqual(before, state) {
				t.Fatal("bad whole model projected or changed", err)
			}
		})
	}
}
