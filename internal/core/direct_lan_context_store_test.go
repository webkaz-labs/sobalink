package core

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Inert Core values, fabricated transcripts and temporary private files only.
// These helpers never construct a runtime, authenticate a peer or open a socket.
type localContextFixture struct {
	core   *Core
	store  *directLANStore
	peer   string
	now    time.Time
	writes int
}

func newLocalContextFixture(t *testing.T) *localContextFixture {
	t.Helper()
	c, s, peer := localEndpointExportFixture(t, func(s *directLANState) {
		r := &s.Metadata.Peers[0]
		r.PairContext, r.EndpointState, r.UpgradePending = nil, nil, nil
		r.ContextConfirmed = false
		other := directlan.Identity{Seed: strings.Repeat("05", 32)}
		p := directlan.Peer{Key: other.PublicKey(), TunnelKey: other.TunnelKey(), Name: "synthetic-unrelated-peer", Endpoint: netip.MustParseAddrPort("127.0.0.3:22003")}
		s.Peers = append(s.Peers, p)
		s.Metadata.Peers = append(s.Metadata.Peers, endpointmeta.PeerRecord{Peer: directLANPeerWire(p), Revision: "1"})
	})
	f := &localContextFixture{core: c, store: s, peer: peer, now: time.Now()}
	f.publisher()
	return f
}

func (f *localContextFixture) publisher() {
	f.store.write = func(path string, data []byte) error {
		f.writes++
		return config.AtomicWritePrivate(path, data)
	}
}

func (f *localContextFixture) capture(in contextInputs) (contextAdmission, error) {
	f.core.op.Lock()
	defer f.core.op.Unlock()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return f.store.captureContextAdmissionLocked(f.core.lanStartNonce, in, f.now)
}

func (f *localContextFixture) apply(a contextAdmission, transcript contextTranscript) (contextSaveResult, error) {
	f.core.op.Lock()
	defer f.core.op.Unlock()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return f.store.applyContextTransitionLocked(context.Background(), f.core.lanStartNonce, a, transcript, f.now)
}

func (f *localContextFixture) step(t *testing.T, in contextInputs, transcript contextTranscript) contextSaveResult {
	t.Helper()
	a, err := f.capture(in)
	if err != nil {
		t.Fatal("context admission", err)
	}
	result, err := f.apply(a, transcript)
	if err != nil {
		t.Fatal("context completion", err)
	}
	return result
}

func (f *localContextFixture) prepareInput() contextInputs {
	return contextInputs{Operation: contextPrepare, PeerKey: f.peer, Deadline: f.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)}
}

func (f *localContextFixture) request(outbound bool) endpointmeta.PrepareRequest {
	m := f.store.state.Metadata
	local, remote := m.LocalPeer, m.Peers[0].Peer
	request := endpointmeta.PrepareRequest{Version: 2, Operation: "pair-context-prepare", Sender: remote.Key, Recipient: local.Key,
		SenderTunnelKey: remote.TunnelKey, RecipientTunnelKey: local.TunnelKey, SenderEndpoint: remote.Endpoint, RecipientEndpoint: local.Endpoint,
		SenderNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)), SenderScope: endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}}
	if outbound {
		request.Sender, request.Recipient = local.Key, remote.Key
		request.SenderTunnelKey, request.RecipientTunnelKey = local.TunnelKey, remote.TunnelKey
		request.SenderEndpoint, request.RecipientEndpoint = local.Endpoint, remote.Endpoint
		request.SenderNonce = m.Peers[0].UpgradePending.OwnNonce
		request.SenderScope = endpointmeta.Scope{Family: m.LocalScope.Family, Prefixes: append([]string(nil), m.LocalScope.Prefixes...)}
	}
	return request
}

func (f *localContextFixture) bound(operation string) endpointmeta.BoundRequest {
	r := f.store.state.Metadata.Peers[0]
	p := r.PairContext
	if p == nil {
		p = r.UpgradePending.Context
	}
	binding, _ := p.Binding()
	return endpointmeta.BoundRequest{Version: 2, Operation: operation, PairBinding: binding}
}

func (f *localContextFixture) advance(t *testing.T, phase string) {
	t.Helper()
	f.step(t, f.prepareInput(), contextTranscript{})
	if phase == "reviewed" {
		return
	}
	f.step(t, contextInputs{Operation: contextRecordInbound, PeerKey: f.peer}, contextTranscript{Prepare: f.request(false)})
	if phase == "prepared" {
		return
	}
	bound := f.bound("pair-context-commit")
	f.step(t, contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound}, contextTranscript{Bound: bound})
}

func (f *localContextFixture) reopen(t *testing.T) {
	t.Helper()
	s, err := readDirectLANStore(f.store.path, 1<<20, 16)
	if err != nil {
		t.Fatal(err)
	}
	f.store, f.writes = s, 0
	f.core = &Core{ctx: context.Background(), lanStartNonce: "synthetic-reopened-context-process", directLAN: s}
	f.publisher()
}

func TestContextAdmissionRejectsStaleOwnershipAndInputs(t *testing.T) {
	for _, change := range []string{"process", "store", "protected file", "same-byte write", "operation", "budget", "peer proposal"} {
		t.Run(change, func(t *testing.T) {
			f := newLocalContextFixture(t)
			a, err := f.capture(f.prepareInput())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "process":
				f.core.lanStartNonce = "synthetic-other-process"
			case "store":
				process := f.core.lanStartNonce
				f.reopen(t)
				f.core.lanStartNonce = process // store identity alone must reject
			case "protected file":
				data, err := os.ReadFile(f.store.path)
				if err != nil {
					t.Fatal(err)
				}
				if err := config.AtomicWritePrivate(f.store.path, append(data, '\n')); err != nil {
					t.Fatal(err)
				}
			case "same-byte write":
				before := f.store.fileDigest
				if err := f.store.writeStateLocked(cloneDirectLANState(f.store.state)); err != nil {
					t.Fatal(err)
				}
				if f.store.fileDigest != before || f.store.reviewRevision == a.writeRevision {
					t.Fatal("fixture did not perform an identical-byte process write")
				}
			case "operation":
				a.inputs.Operation = contextRepublish
			case "budget":
				f.store.bytes--
			case "peer proposal":
				a.proposal = strings.Repeat("0", 64)
			}
			writes := f.writes
			if result, err := f.apply(a, contextTranscript{}); err == nil || result != (contextSaveResult{}) || f.writes != writes {
				t.Fatal("stale admission reached publication", err, result)
			}
		})
	}
}

func TestContextAdmissionCopiesRequestsAndRejectsWrongTranscript(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "reviewed")
	in := contextInputs{Operation: contextRecordOutbound, PeerKey: f.peer, Prepare: f.request(true)}
	a, err := f.capture(in) // no returned transcript exists at admission time
	if err != nil {
		t.Fatal(err)
	}
	in.Prepare.SenderScope.Prefixes[0] = "127.0.0.0/24"
	if a.inputs.Prepare.SenderScope.Prefixes[0] != "127.0.0.0/8" {
		t.Fatal("admission retained the caller's scope slice")
	}
	prepared, err := endpointmeta.RecordInboundContextPrepare(*f.store.state.Metadata, f.peer, f.request(false), f.store.contextWindowLocked(*f.store.state.Metadata, f.peer), f.now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	pair := *prepared.Snapshot.Peers[0].UpgradePending.Context
	binding, _ := pair.Binding()
	reply := endpointmeta.PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairContext: pair, PairBinding: binding}
	for _, bad := range []contextTranscript{{Prepare: f.request(false)}, {Prepared: reply, Prepare: f.request(false)}} {
		if result, err := f.apply(a, bad); err == nil || result.durable || f.writes != 1 {
			t.Fatal("wrong-direction or mixed transcript reached publication", err)
		}
	}
	result, err := f.apply(a, contextTranscript{Prepared: reply})
	if err != nil || !result.changed || !result.durable || result.phase != "prepared" || f.writes != 2 {
		t.Fatal("captured outbound request did not govern completion", err, result)
	}
	reply.PairContext.HostScope.Prefixes[0] = "127.0.0.0/16"
	got, err := f.store.state.Metadata.Peers[0].UpgradePending.Context.Binding()
	if err != nil || got != binding {
		t.Fatal("saved context retained the completion caller's scope slice", err)
	}
}

func TestContextInputsRejectOversizedAndCrossOperationShapes(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "reviewed")
	for _, in := range []contextInputs{
		{Operation: contextPrepare, PeerKey: f.peer, Deadline: strings.Repeat("x", endpointmeta.MaxFrameBytes+1)},
		{Operation: contextRecordInbound, PeerKey: f.peer, Prepare: f.request(false)},
		{Operation: contextResume, PeerKey: f.peer, Resume: endpointmeta.ContextResumeReview{PeerKey: f.peer, NewDeadline: strings.Repeat("x", endpointmeta.MaxFrameBytes+1)}},
	} {
		if _, err := f.capture(in); err == nil {
			t.Fatal("oversized or cross-operation admission accepted")
		}
	}
	a, err := f.capture(contextInputs{Operation: contextRecordInbound, PeerKey: f.peer})
	if err != nil {
		t.Fatal(err)
	}
	request := f.request(false)
	request.SenderScope.Prefixes = make([]string, endpointmeta.MaxFrameBytes)
	for i := range request.SenderScope.Prefixes {
		request.SenderScope.Prefixes[i] = "127.0.0.0/24"
	}
	if result, err := f.apply(a, contextTranscript{Prepare: request}); err == nil || result.durable || f.writes != 1 {
		t.Fatal("oversized completion reached publication", err)
	}
}

func TestContextPreparationFailureRetainsBoundedProvisionalWindow(t *testing.T) {
	f := newLocalContextFixture(t)
	a, err := f.capture(f.prepareInput())
	if err != nil {
		t.Fatal(err)
	}
	before := cloneDirectLANState(f.store.state)
	failure := errors.New("synthetic unpublished context save")
	f.store.write = func(string, []byte) error {
		f.writes++
		w, ok := f.store.contextWindows[f.peer]
		if !ok || w.nonce != a.inputs.OwnNonce || w.cutoff.IsZero() || len(f.store.contextWindows) != 1 {
			t.Fatal("provisional bound was absent before the fallible publisher")
		}
		return failure
	}
	result, err := f.apply(a, contextTranscript{})
	if err == nil || result != (contextSaveResult{}) || !f.store.recovery || f.writes != 1 || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("unpublished save granted success or changed saved state", err, result)
	}
	want := f.store.contextWindows[f.peer]
	// Inspect retention without clearing recovery or retrying any file write.
	f.store.pruneContextWindowsLocked(*f.store.state.Metadata)
	in, err := f.store.contextPreparationInputsLocked(*f.store.state.Metadata, f.peer, a.inputs.Deadline)
	if err != nil || in.OwnNonce != a.inputs.OwnNonce {
		t.Fatal("failed-save re-review changed the provisional nonce", err)
	}
	_, candidate, err := f.store.stateWithContextMetadataLocked(in, contextTranscript{}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.reserveContextWindowLocked(in, candidate.Snapshot, f.now.Add(time.Second)); err != nil || f.store.contextWindows[f.peer] != want {
		t.Fatal("exact retry moved the original cutoff", err)
	}
	changed := *cloneDirectLANMetadata(&candidate.Snapshot)
	changed.Peers[0].UpgradePending.PrepareDeadline = f.now.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if err := f.store.reserveContextWindowLocked(in, changed, f.now); !errors.Is(err, endpointmeta.ErrReview) || f.store.contextWindows[f.peer] != want {
		t.Fatal("retry silently replaced the provisional deadline", err)
	}
	if err := f.store.reserveContextWindowLocked(in, candidate.Snapshot, want.cutoff); !errors.Is(err, endpointmeta.ErrExpired) {
		t.Fatal("exact retry renewed an expired cutoff", err)
	}
	f.store.contextWindows["not-a-saved-peer"] = want
	f.store.pruneContextWindowsLocked(*f.store.state.Metadata)
	if len(f.store.contextWindows) != 1 || f.store.contextWindows[f.peer] != want {
		t.Fatal("pruning lost the provisional slot or retained an unsaved peer")
	}
	if _, err := f.capture(f.prepareInput()); err == nil || f.writes != 1 || !f.store.recovery {
		t.Fatal("failed-save retry bypassed recovery", err)
	}
}

func TestContextReopenRequiresExplicitResumeForFirstRecordAndCommit(t *testing.T) {
	for _, phase := range []string{"reviewed", "prepared"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, phase)
			before := cloneDirectLANState(f.store.state)
			f.reopen(t)
			in := contextInputs{Operation: contextRecordInbound, PeerKey: f.peer}
			transcript := contextTranscript{Prepare: f.request(false)}
			if phase == "prepared" {
				in.Operation, in.Bound = contextCommit, f.bound("pair-context-commit")
				transcript = contextTranscript{Bound: in.Bound}
			}
			a, err := f.capture(in)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := f.apply(a, transcript); !errors.Is(err, endpointmeta.ErrReview) || result.durable || f.writes != 0 {
				t.Fatal("saved UTC deadline reconstructed first-use permission", err)
			}
			deadline := f.store.state.Metadata.Peers[0].UpgradePending.PrepareDeadline
			review, err := endpointmeta.ReviewContextResume(*f.store.state.Metadata, f.peer, deadline, f.now)
			if err != nil {
				t.Fatal(err)
			}
			result := f.step(t, contextInputs{Operation: contextResume, PeerKey: f.peer, Resume: review}, contextTranscript{})
			if result.changed || result.published || result.durable || f.writes != 0 || !reflect.DeepEqual(before, f.store.state) || len(f.store.contextWindows) != 1 {
				t.Fatal("same-deadline explicit resume manufactured a durable write", result)
			}
			if result := f.step(t, in, transcript); !result.changed || !result.durable || f.writes != 1 {
				t.Fatal("explicit resume did not permit the first saved transition", result)
			}
		})
	}
}

func TestContextTransitionsPreserveUnrelatedStateAndInitialHistory(t *testing.T) {
	f := newLocalContextFixture(t)
	original := cloneDirectLANState(f.store.state)
	f.advance(t, "committed")
	r := f.store.state.Metadata.Peers[0]
	initial, err := endpointmeta.InitialState(*r.PairContext, original.Metadata.LocalPeer.Key)
	if err != nil || !reflect.DeepEqual(*r.EndpointState, initial) || r.ContextConfirmed || r.Revision != "1" || f.store.state.Metadata.Revision != "4" {
		t.Fatal("commit did not initialize absent endpoint state exactly once", err)
	}
	if f.store.state.Identity != original.Identity || !reflect.DeepEqual(f.store.state.Selection, original.Selection) || !reflect.DeepEqual(f.store.state.Peers, original.Peers) ||
		!reflect.DeepEqual(f.store.state.Metadata.Peers[1], original.Metadata.Peers[1]) || f.store.state.Metadata.LocalPeer != original.Metadata.LocalPeer ||
		!reflect.DeepEqual(f.store.state.Metadata.LocalScope, original.Metadata.LocalScope) || f.store.state.Metadata.PreviousLocalEndpoint != original.Metadata.PreviousLocalEndpoint {
		t.Fatal("context operations changed unrelated Core or peer data")
	}
	// A retained inactive authority revision is meaningful saved history even
	// without an endpoint offer. Duplicate commit may not replace it with zero.
	next := cloneDirectLANState(f.store.state)
	next.Metadata.Peers[0].EndpointState.AuthorityRevision = "9"
	if err := f.store.writeStateLocked(next); err != nil {
		t.Fatal(err)
	}
	before := cloneDirectLANState(f.store.state)
	bound := f.bound("pair-context-commit")
	writes := f.writes
	result := f.step(t, contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound}, contextTranscript{Bound: bound})
	if result.changed || result.published || result.durable || f.writes != writes || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("duplicate commit reset existing state or claimed durability", result)
	}
	// The independent projection guard must reject a reducer candidate that
	// smuggles an unrelated edit, even when its requested transition is valid.
	f = newLocalContextFixture(t)
	a, err := f.capture(f.prepareInput())
	if err != nil {
		t.Fatal(err)
	}
	_, transition, err := f.store.stateWithContextMetadataLocked(a.inputs, contextTranscript{}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	transition.Snapshot.Peers[1].Peer.Name = "synthetic-unrequested-edit"
	if err := validateContextProjection(*f.store.state.Metadata, transition, a.inputs, f.now); !errors.Is(err, endpointmeta.ErrIdentity) {
		t.Fatal("operation projection admitted unrelated peer mutation", err)
	}
}

func TestContextPreflightUsesWholeIndentedFileAndFiniteCounters(t *testing.T) {
	t.Run("whole private file", func(t *testing.T) {
		f := newLocalContextFixture(t)
		a, err := f.capture(f.prepareInput())
		if err != nil {
			t.Fatal(err)
		}
		next, transition, err := f.store.stateWithContextMetadataLocked(a.inputs, contextTranscript{}, f.now)
		if err != nil {
			t.Fatal(err)
		}
		compact, _ := json.Marshal(transition.Snapshot)
		whole, _ := json.MarshalIndent(next, "", "  ")
		current, err := os.ReadFile(f.store.path)
		if err != nil {
			t.Fatal(err)
		}
		f.store.bytes = int64(max(len(current), len(compact)+1))
		if int64(len(whole)+1) <= f.store.bytes {
			t.Fatal("fixture does not distinguish compact metadata and whole-file limits")
		}
		if _, err := f.capture(a.inputs); !errors.Is(err, endpointmeta.ErrCapacity) || f.writes != 0 || len(f.store.contextWindows) != 0 {
			t.Fatal("whole-file preflight happened after publication or reservation", err)
		}
	})
	for _, counter := range []string{"snapshot", "process"} {
		t.Run(counter, func(t *testing.T) {
			f := newLocalContextFixture(t)
			if counter == "snapshot" {
				next := cloneDirectLANState(f.store.state)
				next.Metadata.Revision = strconv.FormatUint(^uint64(0), 10)
				if err := f.store.writeStateLocked(next); err != nil {
					t.Fatal(err)
				}
			} else {
				f.store.reviewRevision = ^uint64(0)
			}
			writes := f.writes
			a, err := f.capture(f.prepareInput())
			if err == nil {
				_, err = f.apply(a, contextTranscript{})
			}
			if !errors.Is(err, endpointmeta.ErrCapacity) || f.writes != writes || f.store.state.Metadata.Peers[0].UpgradePending != nil {
				t.Fatal("counter exhaustion reached publication or wrapped", err)
			}
		})
	}
}

func TestContextConfirmationRoutesPreserveSavedHistory(t *testing.T) {
	for _, operation := range []contextOperation{contextConfirmCommit, contextConfirmStatus, contextConfirmUpdate} {
		t.Run(strconv.Itoa(int(operation)), func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "committed")
			next := cloneDirectLANState(f.store.state)
			r, local := &next.Metadata.Peers[0], next.Metadata.LocalPeer
			scope, _ := next.Metadata.LocalScope.Digest()
			body := endpointmeta.UpdateBody{Version: 1, Domain: endpointmeta.UpdateDomain, PairBinding: r.EndpointState.PairBinding,
				Issuer: r.Peer.Key, Recipient: local.Key, IssuerTunnelKey: r.Peer.TunnelKey, RecipientTunnelKey: local.TunnelKey,
				Sequence: "7", PriorEndpoint: r.Peer.Endpoint, Operation: "set", Endpoint: r.Peer.Endpoint, ScopeDigest: scope,
				Issued: f.now.Add(-time.Minute).UTC().Format(time.RFC3339Nano), Lifetime: "until-revoked"}
			remote := directlan.Identity{Seed: strings.Repeat("02", 32)}
			proof, err := remote.SignEndpointUpdate(body)
			if err != nil {
				t.Fatal(err)
			}
			digest, _ := proof.Digest()
			r.EndpointState.ReceivedVersion, r.EndpointState.ReceivedHighwater, r.EndpointState.ReceivedProof = 1, "7", &proof
			r.EndpointState.ReceiveStatus, r.EndpointState.AuthorityRevision = "eligible", "2"
			r.EndpointState.Follow = &endpointmeta.FollowApproval{ScopeDigest: scope, Revision: "1", Granted: f.now.UTC().Format(time.RFC3339Nano), Lifetime: "until-revoked", Active: true}
			r.EndpointState.Approval = &endpointmeta.Approval{Kind: "follow", ProofDigest: digest, Endpoint: r.Peer.Endpoint, FollowRevision: "1", Granted: r.EndpointState.Follow.Granted, Lifetime: "until-revoked"}
			if err := f.store.writeStateLocked(next); err != nil {
				t.Fatal("invalid synthetic same-endpoint history", err)
			}
			beforeResume := cloneDirectLANState(f.store.state)
			deadline := f.now.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
			review, err := endpointmeta.ReviewContextResume(*f.store.state.Metadata, f.peer, deadline, f.now)
			if err != nil {
				t.Fatal(err)
			}
			resumed := f.step(t, contextInputs{Operation: contextResume, PeerKey: f.peer, Resume: review}, contextTranscript{})
			want := cloneDirectLANState(beforeResume)
			want.Metadata.Revision, want.Metadata.ObservedAt = f.store.state.Metadata.Revision, f.store.state.Metadata.ObservedAt
			want.Metadata.Peers[0].UpgradePending.PrepareDeadline = deadline
			if !resumed.changed || !resumed.durable || !reflect.DeepEqual(want, f.store.state) || f.store.contextWindows[f.peer].absolute != deadline || len(f.store.contextWindows) != 1 {
				t.Fatal("explicit deadline resume changed nonce, context or saved history", resumed)
			}
			before := cloneDirectLANState(f.store.state)
			in, transcript := contextInputs{Operation: operation, PeerKey: f.peer}, contextTranscript{}
			if operation == contextConfirmUpdate {
				body.Sequence = "8"
				in.Update, err = remote.SignEndpointUpdate(body)
				if err != nil {
					t.Fatal(err)
				}
				bad := in
				signature, err := base64.RawURLEncoding.DecodeString(bad.Update.Signature)
				if err != nil || len(signature) == 0 {
					t.Fatal("invalid synthetic signature fixture", err)
				}
				signature[0] ^= 1
				bad.Update.Signature = base64.RawURLEncoding.EncodeToString(signature)
				if _, err := f.capture(bad); err == nil {
					t.Fatal("unverified proof admitted for confirmation")
				}
			} else {
				name := "pair-context-commit"
				if operation == contextConfirmStatus {
					name = "pair-context-status"
				}
				in.Bound = f.bound(name)
				transcript.Committed = endpointmeta.ContextReply{Version: 2, Operation: name, OK: true, PairBinding: in.Bound.PairBinding, State: "committed"}
			}
			writes := f.writes
			result := f.step(t, in, transcript)
			got := f.store.state.Metadata.Peers[0]
			if !result.changed || !result.durable || result.phase != "confirmed" || f.writes != writes+1 || !got.ContextConfirmed || got.UpgradePending != nil ||
				!reflect.DeepEqual(got.EndpointState, before.Metadata.Peers[0].EndpointState) || !reflect.DeepEqual(got.PairContext, before.Metadata.Peers[0].PairContext) ||
				!reflect.DeepEqual(f.store.state.Metadata.Peers[1], before.Metadata.Peers[1]) {
				t.Fatal("confirmation changed endpoint history, authority or another peer", result)
			}
		})
	}
}

func TestContextUncertainPublicationAdoptsStateWithoutSuccess(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	bound := f.bound("pair-context-commit")
	a, err := f.capture(contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound})
	if err != nil {
		t.Fatal(err)
	}
	f.store.write = func(path string, data []byte) error {
		f.writes++
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	result, err := f.apply(a, contextTranscript{Bound: bound})
	if !errors.Is(err, config.ErrAtomicCommitted) || !result.published || result.changed || result.durable || result.phase != "" || !f.store.recovery || f.store.contextPublication != nil {
		t.Fatal("uncertain publication lost recovery or released success", err, result)
	}
	disk, _, err := readDirectLANFile(f.store.path, 1<<20)
	if err != nil || !reflect.DeepEqual(disk, f.store.state) || disk.Metadata.Peers[0].PairContext == nil || disk.Metadata.Peers[0].ContextConfirmed {
		t.Fatal("uncertain context publication was rolled back in memory", err)
	}
	for _, operation := range []contextOperation{contextCommit, contextConfirmStatus, contextRepublish} {
		in := contextInputs{Operation: operation, PeerKey: f.peer}
		if operation != contextRepublish {
			in.Bound = bound
			if operation == contextConfirmStatus {
				in.Bound.Operation = "pair-context-status"
			}
		}
		if _, err := f.capture(in); err == nil || f.writes != 3 || !f.store.recovery {
			t.Fatal("retry or status cleared uncertain publication", err)
		}
	}
}

func TestContextReopenedStatusNeedsExplicitUnchangedPublication(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "committed")
	f.reopen(t)
	before := cloneDirectLANState(f.store.state)
	bound := f.bound("pair-context-commit")
	duplicate := contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound}
	result := f.step(t, duplicate, contextTranscript{Bound: bound})
	if result.changed || result.published || result.durable || f.writes != 0 || f.store.contextPublication != nil {
		t.Fatal("reopened duplicate created publication evidence", result)
	}
	bound.Operation = "pair-context-status"
	in := contextInputs{Operation: contextConfirmStatus, PeerKey: f.peer, Bound: bound}
	transcript := contextTranscript{Committed: endpointmeta.ContextReply{Version: 2, Operation: bound.Operation, OK: true, PairBinding: bound.PairBinding, State: "committed"}}
	a, err := f.capture(in)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.apply(a, transcript); !errors.Is(err, endpointmeta.ErrReview) || result.durable || f.writes != 0 {
		t.Fatal("ordinary status certified reopened state", err)
	}
	result = f.step(t, contextInputs{Operation: contextRepublish, PeerKey: f.peer}, contextTranscript{})
	if result.changed || !result.published || !result.durable || f.writes != 1 || !reflect.DeepEqual(before, f.store.state) || f.store.reviewRevision != 1 {
		t.Fatal("explicit republish changed model state or skipped durable publication", result)
	}
	if _, err := f.apply(a, transcript); !errors.Is(err, endpointmeta.ErrReview) || f.writes != 1 {
		t.Fatal("same-byte republish did not invalidate older admission", err)
	}
	result = f.step(t, in, transcript)
	r := f.store.state.Metadata.Peers[0]
	if !result.changed || !result.durable || !r.ContextConfirmed || r.UpgradePending != nil || r.Revision != f.store.state.Metadata.Revision || f.writes != 2 || len(f.store.contextWindows) != 0 {
		t.Fatal("freshly admitted confirmation did not finish exactly once", result)
	}
	result = f.step(t, in, transcript)
	if result.changed || result.published || result.durable || result.phase != "confirmed" || f.writes != 2 {
		t.Fatal("observational confirmed status inherited earlier durability", result)
	}
}

func TestContextBoundaryRetainsRuntimePersistAndExposureGuards(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "reviewed")
	if _, err := f.store.runtimeConfig(); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" {
		t.Fatal("prepared metadata became runtime configuration", err)
	}
	before := cloneDirectLANState(f.store.state)
	if err := f.store.persist(f.store.state.Peers); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" || f.writes != 1 || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("legacy Persist discarded managed context metadata", err)
	}
	for _, name := range []string{"direct-lan.context.review", "direct-lan.context.prepare", "direct-lan.context.resume", "direct-lan.context.status", "direct-lan.context.exchange", "direct-lan.context.wait", "direct-lan.context.stop"} {
		if value, err := f.core.command(context.Background(), webui.Command{Name: name}); err == nil || value != nil || !strings.Contains(err.Error(), "unknown command") || f.writes != 1 {
			t.Fatal("private context boundary acquired management exposure", name, err)
		}
	}
	for _, blocked := range []string{"attempted network", "closing", "missing process", "cancelled owner"} {
		t.Run(blocked, func(t *testing.T) {
			f := newLocalContextFixture(t)
			switch blocked {
			case "attempted network":
				f.core.attemptedNetwork = "direct-lan"
			case "closing":
				f.core.closing = true
			case "missing process":
				f.core.lanStartNonce = ""
			case "cancelled owner":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.core.ctx = ctx
			}
			f.core.op.Lock()
			_, err := f.core.reviewContextLocked(f.prepareInput())
			f.core.op.Unlock()
			if networkErrorCode(err) != "direct_lan_endpoint_integration_pending" || f.writes != 0 {
				t.Fatal("Core lifecycle guard permitted metadata review", err)
			}
		})
	}
}
