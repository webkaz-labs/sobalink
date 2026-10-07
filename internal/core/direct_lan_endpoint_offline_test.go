package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// These tests only inspect and transact disposable private metadata. They do
// not establish a pair, construct a Node, prepare an endpoint or open a socket.
// The fixture represents an already confirmed saved context; every variant is
// validated before writing, without changing a production admission guard.
type offlineEndpointFixture struct {
	core     *Core
	store    *directLANStore
	initial  directLANState
	envelope endpointmeta.Envelope
	key      ed25519.PrivateKey
	now      time.Time
	requests int
}

func newOfflineEndpointFixture(t *testing.T, edit func(*directLANState)) *offlineEndpointFixture {
	t.Helper()
	// Public RFC 8032 examples, also used by endpointmeta/testdata/README.md.
	host := directlan.Identity{Seed: "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"}
	local := directlan.Identity{Seed: "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"}
	pairBytes := offlineEndpointRead(t, filepath.Join("..", "endpointmeta", "testdata", "pair.json"))
	pair, err := endpointmeta.ParsePairContext(pairBytes)
	if err != nil {
		t.Fatal(err)
	}
	if pair.HostKey != host.PublicKey() || pair.JoinerKey != local.PublicKey() {
		t.Fatal("public identity vectors changed")
	}
	// Core derives its tunnel identity from the saved seed. Adapt only the
	// synthetic tunnel fields, then re-sign the public update at current time.
	pair.HostTunnelKey, pair.JoinerTunnelKey = host.TunnelKey(), local.TunnelKey()
	initial, err := endpointmeta.InitialState(pair, pair.JoinerKey)
	if err != nil {
		t.Fatal(err)
	}
	f := &offlineEndpointFixture{now: time.Now().UTC().Add(-time.Minute)}
	peer := endpointmeta.PeerWire{Key: pair.HostKey, Name: "synthetic-peer", Endpoint: pair.HostEndpoint, TunnelKey: pair.HostTunnelKey}
	dto, err := directLANPeerDTO(peer)
	if err != nil {
		t.Fatal(err)
	}
	m := endpointmeta.Snapshot{
		Version: 3, Revision: "1", ObservedAt: f.now.Format(time.RFC3339Nano),
		LocalPeer:  endpointmeta.PeerWire{Key: pair.JoinerKey, Endpoint: pair.JoinerEndpoint, TunnelKey: pair.JoinerTunnelKey},
		LocalScope: pair.JoinerScope, PreviousLocalEndpoint: pair.JoinerEndpoint,
		Peers: []endpointmeta.PeerRecord{{Peer: peer, Revision: "1", PairContext: &pair, ContextConfirmed: true, EndpointState: &initial}},
	}
	f.initial = directLANState{Version: 3, Identity: local, Selection: DirectLANSelection{Listen: pair.JoinerEndpoint, Prefixes: pair.JoinerScope.Prefixes}, Peers: []directlan.Peer{dto}, Metadata: &m}
	if edit != nil {
		edit(&f.initial)
	}
	if err := validateDirectLANState(f.initial); err != nil {
		t.Fatal("invalid synthetic saved fixture", err)
	}
	data, err := json.MarshalIndent(f.initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := config.AtomicWrite(filepath.Join(dir, "direct-lan.json"), append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	f.core = openOfflineEndpointCore(t, dir)
	f.store = f.core.directLANStoreCopy()
	if f.store == nil || f.store.needsRecovery() {
		t.Fatal("validated initial fixture did not reopen cleanly")
	}
	seed, err := hex.DecodeString(host.Seed)
	if err != nil {
		t.Fatal(err)
	}
	f.key = ed25519.NewKeyFromSeed(seed)
	f.envelope, err = endpointmeta.ParseEnvelope(offlineEndpointRead(t, filepath.Join("..", "endpointmeta", "testdata", "update.json")))
	if err != nil {
		t.Fatal(err)
	}
	u := f.envelope.Update
	u.PairBinding, err = pair.Binding()
	if err != nil {
		t.Fatal(err)
	}
	u.IssuerTunnelKey, u.RecipientTunnelKey = pair.HostTunnelKey, pair.JoinerTunnelKey
	u.Issued, u.Expires = f.now.Format(time.RFC3339Nano), f.now.Add(24*time.Hour).Format(time.RFC3339Nano)
	f.envelope = f.sign(t, u)
	return f
}

func openOfflineEndpointCore(t *testing.T, dir string) *Core {
	t.Helper()
	c, err := Open(context.Background(), Options{Directory: dir, Version: "offline-metadata-test", SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		t.Fatal("offline endpoint metadata test reached NodeFactory")
		return nil, errors.New("network forbidden")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.nodeCopy() != nil {
			t.Error("offline endpoint metadata created a backend")
		}
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func offlineEndpointRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *offlineEndpointFixture) sign(t *testing.T, u endpointmeta.UpdateBody) endpointmeta.Envelope {
	t.Helper()
	e, err := endpointmeta.Sign(u, f.key)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *offlineEndpointFixture) input(t *testing.T, approve bool) directLANEndpointInput {
	t.Helper()
	text, err := f.envelope.Text()
	if err != nil {
		t.Fatal(err)
	}
	in := directLANEndpointInput{PeerID: f.envelope.Update.Issuer, Update: text}
	if approve {
		digest, err := f.envelope.Digest()
		if err != nil {
			t.Fatal(err)
		}
		in.Approval = &endpointmeta.Approval{Kind: "exact", ProofDigest: digest, Endpoint: f.envelope.Update.Endpoint, Granted: f.now.Format(time.RFC3339Nano), Lifetime: "finite", Expires: f.now.Add(time.Hour).Format(time.RFC3339Nano)}
	}
	return in
}

func (f *offlineEndpointFixture) call(t *testing.T, name string, input directLANEndpointInput) (any, error) {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	f.requests++
	return f.core.Command(context.Background(), webui.Command{RequestID: fmt.Sprintf("offline-endpoint-%d", f.requests), Name: "direct-lan.endpoint." + name, Payload: b})
}

func (f *offlineEndpointFixture) must(t *testing.T, name string, input directLANEndpointInput) any {
	t.Helper()
	value, err := f.call(t, name, input)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func (f *offlineEndpointFixture) reviewed(t *testing.T, input directLANEndpointInput) directLANEndpointInput {
	t.Helper()
	review := f.must(t, "inspect", input).(directLANEndpointReview)
	if review.Revision == "" || review.EndpointUpdatesEnabled {
		t.Fatal("missing offline review boundary")
	}
	input.ExpectedRevision = review.Revision
	return input
}

func (f *offlineEndpointFixture) grantFollow(t *testing.T) {
	t.Helper()
	digest, err := f.initial.Metadata.LocalScope.Digest()
	if err != nil {
		t.Fatal(err)
	}
	in := directLANEndpointInput{PeerID: f.envelope.Update.Issuer, Action: "grant-follow", Follow: &endpointmeta.FollowApproval{ScopeDigest: digest, Revision: "1", Granted: f.now.Format(time.RFC3339Nano), Lifetime: "finite", Expires: f.now.Add(2 * time.Hour).Format(time.RFC3339Nano), Active: true}}
	review := f.must(t, "follow.preview", in).(directLANEndpointReview)
	in.ExpectedRevision = review.Revision
	f.must(t, "follow.apply", in)
	saved := f.store.copy().Metadata.Peers[0].EndpointState
	if !reflect.DeepEqual(saved.Follow, in.Follow) || saved.Approval != nil || saved.ReceivedHighwater != "0" {
		t.Fatal("explicit follow changed endpoint authority or its chosen lifetime")
	}
}

func (f *offlineEndpointFixture) exhaustCutoff(t *testing.T, kind string) {
	t.Helper()
	// Model elapsed monotonic time without changing valid saved UTC metadata.
	// This is a fault in the process projection, not an OS clock simulation.
	f.core.op.Lock()
	f.store.mu.Lock()
	count := 0
	for key, deadline := range f.store.endpointDeadlines {
		if key.kind == kind {
			deadline.monotonic = time.Now().Add(-time.Minute)
			f.store.endpointDeadlines[key] = deadline
			count++
		}
	}
	f.store.mu.Unlock()
	f.core.op.Unlock()
	if count != 1 {
		t.Fatalf("expected one observed %s cutoff, got %d", kind, count)
	}
}

func (f *offlineEndpointFixture) assertUnchanged(t *testing.T, file []byte, state directLANState) {
	t.Helper()
	if !bytes.Equal(file, offlineEndpointRead(t, f.store.path)) || !reflect.DeepEqual(state, f.store.copy()) {
		t.Fatal("observation or rejected operation changed the protected authority")
	}
}

func TestOfflineEndpointInspectIsWriteFreeAndCannotCreateConsent(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	signature := f.envelope.Signature
	before := offlineEndpointRead(t, f.store.path)
	profile := offlineEndpointRead(t, filepath.Join(f.core.dir, "sobalink.json"))
	f.store.write = func(string, []byte) error { t.Fatal("inspection wrote endpoint state"); return os.ErrPermission }
	in := f.input(t, false)
	review := f.must(t, "inspect", in).(directLANEndpointReview)
	if review.Outcome != "review_required" || review.Approval != nil || review.Follow != nil || review.EndpointUpdatesEnabled {
		t.Fatal("incoming proof manufactured consent")
	}
	in.ExpectedRevision = review.Revision
	result := f.must(t, "accept", in).(map[string]any)
	if result["saved"] != false || result["changed"] != false || result["outcome"] != "review_required" {
		t.Fatal("unapproved proof reported acceptance")
	}
	// Even explicitly approved but unapplied previews must not accumulate
	// deadline observations for imported proofs in the saved-state owner.
	for _, sequence := range []string{"8", "9", "10"} {
		u := f.envelope.Update
		u.Sequence = sequence
		f.envelope = f.sign(t, u)
		f.reviewed(t, f.input(t, true))
	}
	if len(f.store.endpointDeadlines) != 0 {
		t.Fatal("uncommitted previews accumulated process deadline entries")
	}
	status := f.must(t, "status", directLANEndpointInput{}).(map[string]any)
	if status["endpointUpdatesEnabled"] != false || status["recoveryRequired"] != false {
		t.Fatal("read advertised activation or recovery")
	}
	b, err := json.Marshal([]any{review, status})
	if err != nil {
		t.Fatal(err)
	}
	pair := f.initial.Metadata.Peers[0].PairContext
	for _, secret := range []string{f.initial.Identity.Seed, signature, f.envelope.Signature, pair.HostNonce, pair.JoinerNonce} {
		if strings.Contains(string(b), secret) {
			t.Fatal("review exposed private proof or context material")
		}
	}
	f.assertUnchanged(t, before, f.initial)
	if !bytes.Equal(profile, offlineEndpointRead(t, filepath.Join(f.core.dir, "sobalink.json"))) || len(f.core.profileCopy().Peers) != 0 {
		t.Fatal("endpoint inspection created application authority")
	}
}

func TestOfflineEndpointMissingContextCannotBeManufactured(t *testing.T) {
	for _, absent := range []bool{true, false} {
		t.Run(fmt.Sprintf("absent_%t", absent), func(t *testing.T) {
			f := newOfflineEndpointFixture(t, func(s *directLANState) {
				r := &s.Metadata.Peers[0]
				r.ContextConfirmed = false
				if absent {
					r.PairContext, r.EndpointState = nil, nil
				}
			})
			before := offlineEndpointRead(t, f.store.path)
			f.store.write = func(string, []byte) error { t.Fatal("missing-context operation wrote"); return os.ErrPermission }
			for _, name := range []string{"inspect", "accept"} {
				_, err := f.call(t, name, f.input(t, true))
				if networkErrorCode(err) != "direct_lan_endpoint_context_required" {
					t.Fatal("missing context accepted", err)
				}
			}
			f.assertUnchanged(t, before, f.initial)
		})
	}
}

func TestOfflineEndpointCommandsRejectAttemptedNetworkStart(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	before := offlineEndpointRead(t, f.store.path)
	f.store.write = func(string, []byte) error { t.Fatal("stopped-only guard reached writer"); return os.ErrPermission }
	// Mark a prior attempt without constructing a backend or starting anything.
	f.core.mu.Lock()
	f.core.attemptedNetwork = "direct-lan"
	f.core.mu.Unlock()
	for _, name := range []string{"status", "inspect", "accept", "reapprove-current", "revoke", "expire", "follow.preview", "follow.apply", "recovery.inspect", "recovery.apply"} {
		if _, err := f.call(t, name, directLANEndpointInput{}); networkErrorCode(err) != "network_restart_required" {
			t.Fatal("endpoint command bypassed stopped-only admission", name, err)
		}
	}
	f.assertUnchanged(t, before, f.initial)
}

func TestOfflineEndpointReviewBindsInputFileProcessAndStoreRevision(t *testing.T) {
	for _, change := range []string{"input", "file-bytes", "process", "store-revision", "missing-review"} {
		t.Run(change, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			in := f.reviewed(t, f.input(t, true))
			// Ordinary polling must not invalidate the unchanged reviewed result.
			f.must(t, "status", directLANEndpointInput{})
			if got := f.reviewed(t, in).ExpectedRevision; got != in.ExpectedRevision {
				t.Fatal("status polling invalidated an unchanged review")
			}
			switch change {
			case "input":
				in.Approval.Expires = f.now.Add(30 * time.Minute).Format(time.RFC3339Nano)
			case "file-bytes":
				b := append(offlineEndpointRead(t, f.store.path), '\n')
				if err := config.AtomicWrite(f.store.path, b); err != nil {
					t.Fatal(err)
				}
			case "process":
				if err := f.core.Close(); err != nil {
					t.Fatal(err)
				}
				f.core = openOfflineEndpointCore(t, f.core.dir)
				f.store = f.core.directLANStoreCopy()
			case "store-revision":
				// A whole-file publication can preserve semantic authority while
				// changing the process write generation. It still invalidates review.
				f.core.op.Lock()
				f.store.mu.Lock()
				err := f.store.writeStateLocked(f.store.state)
				f.store.mu.Unlock()
				f.core.op.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "missing-review":
				in.ExpectedRevision = ""
			}
			before, state := offlineEndpointRead(t, f.store.path), f.store.copy()
			f.store.write = func(string, []byte) error { t.Fatal("stale review reached writer"); return os.ErrPermission }
			_, err := f.call(t, "accept", in)
			if networkErrorCode(err) != "direct_lan_endpoint_review_changed" {
				t.Fatal("changed review accepted", err)
			}
			f.assertUnchanged(t, before, state)
		})
	}
}

func TestOfflineEndpointSavedAcceptanceKeepsReplayAndScope(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	profile := offlineEndpointRead(t, filepath.Join(f.core.dir, "sobalink.json"))
	in := f.reviewed(t, f.input(t, true))
	result := f.must(t, "accept", in).(map[string]any)
	if result["saved"] != true || result["outcome"] != "saved_only" || result["endpointUpdatesEnabled"] != false {
		t.Fatal("saved metadata was not reported as saved-only")
	}
	saved := f.store.copy()
	r := saved.Metadata.Peers[0]
	if saved.Metadata.Revision != "3" || r.Revision != "3" || r.EndpointState.ReceivedHighwater != "7" || *r.EndpointState.ReceivedProof != f.envelope || !reflect.DeepEqual(r.EndpointState.Approval, in.Approval) {
		t.Fatal("two-save transaction lost exact approval or replay evidence")
	}
	if saved.Identity != f.initial.Identity || !reflect.DeepEqual(saved.Selection, f.initial.Selection) || !reflect.DeepEqual(r.PairContext, f.initial.Metadata.Peers[0].PairContext) || !r.ContextConfirmed || r.Peer.Key != f.initial.Peers[0].Key || r.Peer.TunnelKey != f.initial.Peers[0].TunnelKey {
		t.Fatal("endpoint save changed unrelated identity, context or selected scope")
	}
	if !bytes.Equal(profile, offlineEndpointRead(t, filepath.Join(f.core.dir, "sobalink.json"))) {
		t.Fatal("endpoint save changed application profile")
	}
	before := offlineEndpointRead(t, f.store.path)
	f.store.write = func(string, []byte) error { t.Fatal("replayed proof reached writer"); return os.ErrPermission }
	duplicate := f.reviewed(t, f.input(t, false))
	if got := f.must(t, "accept", duplicate).(map[string]any); got["outcome"] != "already_applied" || got["saved"] != false {
		t.Fatal("duplicate proof was not inert")
	}
	for _, stale := range []bool{true, false} {
		u := f.envelope.Update
		want := "direct_lan_endpoint_conflict"
		if stale {
			u.Sequence, want = "6", "direct_lan_endpoint_stale"
		} else {
			u.Endpoint = "127.0.0.4:20004"
		}
		e := f.sign(t, u)
		text, err := e.Text()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.call(t, "inspect", directLANEndpointInput{PeerID: u.Issuer, Update: text}); networkErrorCode(err) != want {
			t.Fatal("replay boundary changed", err)
		}
	}
	f.assertUnchanged(t, before, saved)
}

func TestOfflineEndpointFenceAndFinalSaveOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                string
		fail                int
		published           bool
		writes              int
		pending             bool
		revision, highwater string
	}{
		{"fence-unpublished", 1, false, 1, false, "1", "0"},
		{"fence-uncertain", 1, true, 1, true, "2", "0"},
		{"final-unpublished", 2, false, 2, true, "2", "0"},
		{"final-uncertain", 2, true, 2, false, "3", "7"},
		{"durable", 0, false, 2, false, "3", "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			in := f.reviewed(t, f.input(t, true))
			writes := 0
			f.store.write = func(path string, data []byte) error {
				writes++
				var candidate directLANState
				if err := json.Unmarshal(data, &candidate); err != nil {
					t.Fatal("writer received invalid whole Core file", err)
				}
				if writes == 1 {
					p := candidate.Metadata.PendingChange
					if p == nil || p.Mutation.State.ReceivedHighwater != "7" || candidate.Metadata.Peers[0].EndpointState.ReceivedHighwater != "0" {
						t.Fatal("first save did not preserve old authority under a pending fence")
					}
				} else if writes == 2 && candidate.Metadata.PendingChange != nil {
					t.Fatal("final save retained pending transaction")
				}
				if writes == tc.fail && !tc.published {
					return os.ErrPermission
				}
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				if writes == tc.fail {
					return config.ErrAtomicCommitted
				}
				return nil
			}
			value, err := f.call(t, "accept", in)
			if (err != nil) != (tc.fail != 0) || writes != tc.writes {
				t.Fatal("wrong save outcome or write count", err, writes)
			}
			if tc.fail != 0 && (value != nil || !f.store.needsRecovery() || errors.Is(err, config.ErrAtomicCommitted) != tc.published) {
				t.Fatal("uncertain write returned success or lost publication evidence")
			}
			state := f.store.copy()
			if state.Metadata.Revision != tc.revision || state.Metadata.Peers[0].EndpointState.ReceivedHighwater != tc.highwater || (state.Metadata.PendingChange != nil) != tc.pending {
				t.Fatal("in-memory authority disagrees with publication boundary")
			}
			loaded, err := readDirectLANStore(f.store.path, f.store.currentCapacity().bytes, f.store.currentCapacity().peers)
			if err != nil || !reflect.DeepEqual(loaded.copy(), state) || loaded.needsRecovery() != tc.pending {
				t.Fatal("private-file reload disagrees with published evidence", err)
			}
			// Ordinary reads do not clear a fence or a process uncertainty latch.
			f.must(t, "status", directLANEndpointInput{})
			if writes != tc.writes || f.store.needsRecovery() != (tc.fail != 0) {
				t.Fatal("status silently reconciled a save")
			}
			if tc.fail != 0 && !tc.pending {
				if _, err := f.call(t, "recovery.inspect", directLANEndpointInput{}); networkErrorCode(err) != "direct_lan_recovery_required" || writes != tc.writes {
					t.Fatal("fence-less uncertainty gained a recovery override", err)
				}
			}
		})
	}
}

func (f *offlineEndpointFixture) leavePending(t *testing.T) {
	t.Helper()
	writes := 0
	f.store.write = func(path string, data []byte) error {
		writes++
		if writes == 2 {
			return os.ErrPermission
		}
		return config.AtomicWrite(path, data)
	}
	if _, err := f.call(t, "accept", f.reviewed(t, f.input(t, true))); err == nil || writes != 2 || f.store.copy().Metadata.PendingChange == nil {
		t.Fatal("fixture did not leave a confirmed pending fence", err)
	}
}

func TestOfflineEndpointPendingRecoveryNeedsExactReview(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_%t", cancel), func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			f.leavePending(t)
			before, pending := offlineEndpointRead(t, f.store.path), f.store.copy()
			writes := 0
			f.store.write = func(path string, data []byte) error { writes++; return config.AtomicWrite(path, data) }
			review := f.must(t, "recovery.inspect", directLANEndpointInput{Cancel: cancel}).(map[string]any)
			f.must(t, "status", directLANEndpointInput{})
			f.assertUnchanged(t, before, pending)
			if writes != 0 || review["canCancel"] != true || review["endpointUpdatesEnabled"] != false {
				t.Fatal("pending inspection changed saved evidence")
			}
			in := directLANEndpointInput{TransactionID: review["transactionId"].(string), ExpectedRevision: review["revision"].(string), Cancel: cancel}
			for _, changed := range []directLANEndpointInput{
				{TransactionID: in.TransactionID, Cancel: cancel},
				{TransactionID: strings.Repeat("A", 43), ExpectedRevision: in.ExpectedRevision, Cancel: cancel},
				{TransactionID: in.TransactionID, ExpectedRevision: in.ExpectedRevision, Cancel: !cancel},
			} {
				if _, err := f.call(t, "recovery.apply", changed); networkErrorCode(err) != "direct_lan_endpoint_review_changed" || writes != 0 {
					t.Fatal("changed recovery request wrote state", err)
				}
			}
			value := f.must(t, "recovery.apply", in).(map[string]any)
			state := f.store.copy()
			e := state.Metadata.Peers[0].EndpointState
			want := "eligible"
			if cancel {
				want = "cancelled"
			}
			if writes != 1 || value["saved"] != true || value["outcome"] != "saved_only" || f.store.needsRecovery() || state.Metadata.PendingChange != nil || e.ReceiveStatus != want || e.ReceivedHighwater != "7" || *e.ReceivedProof != f.envelope || (e.Approval == nil) != cancel {
				t.Fatal("reconciliation lost evidence or overstated completion")
			}
			if _, err := f.call(t, "recovery.apply", in); networkErrorCode(err) != "direct_lan_recovery_required" || writes != 1 {
				t.Fatal("completed recovery was replayed", err)
			}
			loaded, err := readDirectLANStore(f.store.path, f.store.currentCapacity().bytes, f.store.currentCapacity().peers)
			if err != nil || !reflect.DeepEqual(state, loaded.copy()) {
				t.Fatal("reconciled file differs from memory", err)
			}
		})
	}
}

func TestOfflineEndpointRecoveryReviewBindsExpiryOutcome(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	f.leavePending(t)
	before, pending := offlineEndpointRead(t, f.store.path), f.store.copy()
	f.store.write = func(string, []byte) error { t.Fatal("stale expiry review reached writer"); return os.ErrPermission }
	f.core.op.Lock()
	f.store.mu.Lock()
	defer f.core.op.Unlock()
	defer f.store.mu.Unlock()
	inspect := func(now time.Time) map[string]any {
		value, err := f.store.endpointRecoveryCommandLocked(context.Background(), f.core.lanStartNonce, "direct-lan.endpoint.recovery.inspect", directLANEndpointInput{}, now)
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]any)
	}
	eligible := inspect(f.now.Add(5 * time.Minute))
	expired := inspect(f.now.Add(2 * time.Hour))
	if eligible["revision"] == expired["revision"] || eligible["after"].([]map[string]any)[0]["state"] != "eligible" || expired["after"].([]map[string]any)[0]["state"] != "expired" {
		t.Fatal("same review token represented different final authority")
	}
	in := directLANEndpointInput{TransactionID: eligible["transactionId"].(string), ExpectedRevision: eligible["revision"].(string)}
	_, err := f.store.endpointRecoveryCommandLocked(context.Background(), f.core.lanStartNonce, "direct-lan.endpoint.recovery.apply", in, f.now.Add(2*time.Hour))
	if networkErrorCode(err) != "direct_lan_endpoint_review_changed" || !reflect.DeepEqual(f.store.state, pending) || !bytes.Equal(before, offlineEndpointRead(t, f.store.path)) {
		t.Fatal("stale expiry outcome changed a pending fence", err)
	}
}

func TestOfflineEndpointObservedClockRollbackStaysProcessLocal(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	f.grantFollow(t)
	before := offlineEndpointRead(t, f.store.path)
	state := f.store.copy()
	f.store.write = func(string, []byte) error { t.Fatal("clock observation wrote state"); return os.ErrPermission }
	f.core.op.Lock()
	f.store.mu.Lock()
	_, first := f.store.endpointModelLocked(f.now.Add(3*time.Hour), false)
	_, rollback := f.store.endpointModelLocked(f.now.Add(time.Hour), false)
	_, caughtUp := f.store.endpointModelLocked(f.now.Add(4*time.Hour), false)
	f.store.mu.Unlock()
	f.core.op.Unlock()
	if first != nil || !errors.Is(rollback, directlan.ErrRecovery) || !errors.Is(caughtUp, directlan.ErrRecovery) {
		t.Fatal("wall-clock rollback did not remain blocked", first, rollback, caughtUp)
	}
	f.assertUnchanged(t, before, state)
	loaded, err := readDirectLANStore(f.store.path, f.store.currentCapacity().bytes, f.store.currentCapacity().peers)
	if err != nil || loaded.needsRecovery() || !loaded.endpointObservedAt.IsZero() {
		t.Fatal("write-free observation was incorrectly treated as durable across restart", err)
	}
}

func TestOfflineEndpointMonotonicProofBoundSurvivesLocalReapproval(t *testing.T) {
	f := newOfflineEndpointFixture(t, nil)
	in := f.reviewed(t, f.input(t, true))
	f.must(t, "accept", in)
	before, state := offlineEndpointRead(t, f.store.path), f.store.copy()
	f.store.write = func(string, []byte) error { t.Fatal("exhausted proof reached writer"); return os.ErrPermission }
	f.exhaustCutoff(t, "proof")
	status := f.must(t, "status", directLANEndpointInput{}).(map[string]any)
	if status["authorityClockReviewRequired"] != true {
		t.Fatal("status hid exhausted process authority")
	}
	fresh := *in.Approval
	fresh.Granted = f.now.Add(30 * time.Second).Format(time.RFC3339Nano)
	fresh.Expires = f.now.Add(2 * time.Hour).Format(time.RFC3339Nano)
	_, err := f.call(t, "inspect", directLANEndpointInput{PeerID: in.PeerID, Action: "reapprove", Approval: &fresh})
	if networkErrorCode(err) != "direct_lan_endpoint_clock_review_required" {
		t.Fatal("local reapproval reset the unchanged remote proof cutoff", err)
	}
	f.assertUnchanged(t, before, state)
}

func TestOfflineEndpointMonotonicConsentBoundsSurvivePolling(t *testing.T) {
	for _, kind := range []string{"approval", "follow"} {
		t.Run(kind, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			if kind == "follow" {
				f.grantFollow(t)
			} else {
				f.must(t, "accept", f.reviewed(t, f.input(t, true)))
			}
			before, state := offlineEndpointRead(t, f.store.path), f.store.copy()
			f.exhaustCutoff(t, kind)
			f.store.write = func(string, []byte) error { t.Fatal("exhausted consent reached writer"); return os.ErrPermission }
			for i := 0; i < 2; i++ {
				if f.must(t, "status", directLANEndpointInput{}).(map[string]any)["authorityClockReviewRequired"] != true {
					t.Fatal("polling reset an unchanged consent cutoff")
				}
			}
			in := f.input(t, false)
			if kind == "approval" {
				in.Update, in.Action, in.Approval = "", "reapprove", state.Metadata.Peers[0].EndpointState.Approval
			}
			if _, err := f.call(t, "inspect", in); networkErrorCode(err) != "direct_lan_endpoint_clock_review_required" {
				t.Fatal("unchanged consent regained authority", err)
			}
			f.assertUnchanged(t, before, state)
		})
	}
}

func TestOfflineEndpointReductionsIgnoreIndependentFollowCutoff(t *testing.T) {
	for _, action := range []string{"revoke", "withdraw", "disable-follow", "cancel-set"} {
		t.Run(action, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			f.grantFollow(t)
			if action == "cancel-set" {
				f.leavePending(t)
			} else {
				f.must(t, "accept", f.reviewed(t, f.input(t, true)))
			}
			f.exhaustCutoff(t, "follow")
			f.store.write = config.AtomicWrite
			wantState, wantSequence := "locally_revoked", "7"
			switch action {
			case "cancel-set":
				review := f.must(t, "recovery.inspect", directLANEndpointInput{Cancel: true}).(map[string]any)
				f.must(t, "recovery.apply", directLANEndpointInput{Cancel: true, TransactionID: review["transactionId"].(string), ExpectedRevision: review["revision"].(string)})
				wantState = "cancelled"
			case "withdraw":
				u := f.envelope.Update
				u.Sequence, u.Operation, u.PriorEndpoint, u.Endpoint = "8", "withdraw", u.Endpoint, ""
				f.envelope = f.sign(t, u)
				f.must(t, "accept", f.reviewed(t, f.input(t, false)))
				wantState, wantSequence = "withdrawn", "8"
			case "disable-follow":
				in := directLANEndpointInput{PeerID: f.envelope.Update.Issuer, Action: action}
				review := f.must(t, "follow.preview", in).(directLANEndpointReview)
				in.ExpectedRevision = review.Revision
				f.must(t, "follow.apply", in)
				wantState = "eligible" // independent exact approval is unchanged
			default:
				in := f.reviewed(t, directLANEndpointInput{PeerID: f.envelope.Update.Issuer, Action: action})
				f.must(t, action, in)
			}
			state := f.store.copy()
			e := state.Metadata.Peers[0].EndpointState
			if state.Metadata.PendingChange != nil || e.ReceiveStatus != wantState || e.ReceivedHighwater != wantSequence || *e.ReceivedProof != f.envelope || (e.Approval != nil) != (action == "disable-follow") || e.Follow == nil || e.Follow.Active != (action != "disable-follow") {
				t.Fatal("independent expired process consent blocked reduction or erased evidence")
			}
		})
	}
}

func TestOfflineEndpointFenceStagesDeadlineBeforeFallibleSave(t *testing.T) {
	for _, interrupt := range []string{"clock-cutoff", "caller-cancelled"} {
		t.Run(interrupt, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, nil)
			in := f.reviewed(t, f.input(t, true))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writes := 0
			f.store.write = func(path string, data []byte) error {
				writes++
				if writes != 1 || len(f.store.endpointDeadlines) != 2 {
					t.Fatal("fence save was reached before its proof and approval bounds")
				}
				if err := config.AtomicWrite(path, data); err != nil {
					return err
				}
				if interrupt == "caller-cancelled" {
					cancel()
				} else {
					// The writer is called with Core.op and store.mu held.
					for key, deadline := range f.store.endpointDeadlines {
						if key.kind == "proof" {
							deadline.monotonic = time.Now().Add(-time.Minute)
							f.store.endpointDeadlines[key] = deadline
						}
					}
				}
				return nil
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			value, err := f.core.Command(ctx, webui.Command{RequestID: "interrupted-fence", Name: "direct-lan.endpoint.accept", Payload: raw})
			if interrupt == "caller-cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("fence cancellation was not reported", err)
				}
			} else if networkErrorCode(err) != "direct_lan_endpoint_clock_review_required" {
				t.Fatal("save time restarted pending proof deadline", err)
			}
			state := f.store.copy()
			if value != nil || writes != 1 || !f.store.needsRecovery() || state.Metadata.PendingChange == nil || state.Metadata.Revision != "2" || state.Metadata.Peers[0].EndpointState.ReceivedHighwater != "0" {
				t.Fatal("interrupted fence was lost or final acceptance was acknowledged")
			}
			loaded, err := readDirectLANStore(f.store.path, f.store.currentCapacity().bytes, f.store.currentCapacity().peers)
			if err != nil || !loaded.needsRecovery() || !reflect.DeepEqual(loaded.copy(), state) {
				t.Fatal("interrupted fence did not survive private-file reload", err)
			}
		})
	}
}

func TestOfflineEndpointCapacityFailsBeforeFencePublication(t *testing.T) {
	for _, limit := range []string{"whole-file", "process-revision", "persisted-revision"} {
		t.Run(limit, func(t *testing.T) {
			f := newOfflineEndpointFixture(t, func(s *directLANState) {
				if limit == "persisted-revision" {
					s.Metadata.Revision = "18446744073709551614"
				}
			})
			in := f.reviewed(t, f.input(t, true))
			before, state := offlineEndpointRead(t, f.store.path), f.store.copy()
			if limit == "whole-file" {
				// Construct a discriminator: both the saved file and compact
				// pending model fit; Core's complete indented fence does not.
				now := time.Now()
				m, _, err := endpointmeta.ProposeReceive(*state.Metadata, in.PeerID, f.envelope, in.Approval, now)
				if err != nil {
					t.Fatal(err)
				}
				review, err := endpointmeta.PreviewMutation(*state.Metadata, m)
				if err != nil {
					t.Fatal(err)
				}
				fence, err := endpointmeta.Fence(*state.Metadata, m, review, strings.Repeat("A", 43), now, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				compact, err := endpointmeta.EncodeSnapshot(fence, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				limit := max(len(before), len(compact)) + 64 // allow timestamp width variation
				file := directLANMetadataFile{Version: 3, Identity: state.Identity, Selection: state.Selection, Revision: fence.Revision, PreviousLocalEndpoint: fence.PreviousLocalEndpoint, ObservedAt: fence.ObservedAt, Peers: fence.Peers, PendingChange: fence.PendingChange}
				indented, err := json.MarshalIndent(file, "", "  ")
				if err != nil || len(indented)+1 <= limit {
					t.Fatal("fixture does not distinguish model and complete-file budgets", err)
				}
				f.store.limits.Store(&lanStoreLimits{bytes: int64(limit), peers: f.store.currentCapacity().peers})
			} else if limit == "process-revision" {
				f.store.reviewRevision = ^uint64(0) - 1
				in = f.reviewed(t, f.input(t, true))
			}
			f.store.write = func(string, []byte) error { t.Fatal("capacity failure reached first writer"); return os.ErrPermission }
			if _, err := f.call(t, "accept", in); networkErrorCode(err) != "direct_lan_capacity" {
				t.Fatal("transaction did not preflight both revisions and whole file", err)
			}
			f.assertUnchanged(t, before, state)
			if f.store.needsRecovery() {
				t.Fatal("preflight-only rejection fabricated uncertain persistence")
			}
		})
	}
}
