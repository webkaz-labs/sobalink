package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These fixtures exercise private Core/store gates using synthetic metadata,
// inert owners and temporary private files. They never start a Node, prepare a
// transport, dial, listen, call Exchange or manufacture authenticated evidence.
// A zero Node is only a nonnil identity and supports signal-only RequestClose;
// its Close method must never be called. Actual TLS claims and transport joins
// belong to the separate in-memory DirectLAN transport fixtures.
func localContextRuntimeOwner(t *testing.T, f *localContextFixture) *contextControlOwner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	o := &contextControlOwner{
		node:          &directlan.Node{},
		store:         f.store,
		process:       f.core.lanStartNonce,
		configuration: contextConfigurationDigest(f.store.state),
		ctx:           ctx,
		cancel:        cancel,
		started:       make(chan struct{}),
		operations:    make(map[*directlan.ContextAttempt]*contextExchangeOperation),
	}
	f.core.contextControl = o
	return o
}

func localContextRuntimePublish(t *testing.T, f *localContextFixture) {
	t.Helper()
	o := f.core.contextControl
	if o == nil {
		o = localContextRuntimeOwner(t, f)
	}
	f.core.op.Lock()
	defer f.core.op.Unlock()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if err := f.store.publishContextBeforeArmLocked(context.Background(), o, f.peer, f.now); err != nil {
		t.Fatal(err)
	}
}

func localContextRuntimeReply(f *localContextFixture, in contextInputs, transcript contextTranscript) (endpointmeta.Reply, error) {
	f.core.op.Lock()
	defer f.core.op.Unlock()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return f.store.contextReplyLocked(f.core.lanStartNonce, in, transcript, f.now)
}

// A local response slot is deliberately not an authenticated transport permit.
// Tests use it only to check rejection by Core's additional send gate. No test
// passes this slot to a transport or treats it as a successful wire response.
func localContextRuntimeSlot(f *localContextFixture, o *contextControlOwner, attempt *directlan.ContextAttempt, reply endpointmeta.Reply) *contextResponseSlot {
	return &contextResponseSlot{owner: o, attempt: attempt, store: f.store, process: o.process,
		receipt: f.store.contextPublication, epoch: f.store.contextEpochLocked(),
		writeRevision: f.store.reviewRevision, replyDigest: privateRevision(reply), deadline: time.Now().Add(time.Minute)}
}

func TestContextPreparedRepublishPreservesStateAndNoWindow(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	f.reopen(t)
	before := cloneDirectLANState(f.store.state)
	beforeFile, err := os.ReadFile(f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	bound := f.bound("pair-context-status")
	in := contextInputs{Operation: contextStatusInbound, PeerKey: f.peer, Bound: bound}
	transcript := contextTranscript{Bound: bound}
	if _, err := localContextRuntimeReply(f, in, transcript); !errors.Is(err, endpointmeta.ErrReview) {
		t.Fatal("reopened phase supplied a publication receipt", err)
	}
	localContextRuntimePublish(t, f)
	afterFile, err := os.ReadFile(f.store.path)
	if err != nil || !bytes.Equal(beforeFile, afterFile) || !reflect.DeepEqual(before, f.store.state) ||
		f.writes != 1 || f.store.reviewRevision != 1 || len(f.store.contextWindows) != 0 || !f.store.contextPublicationCurrentLocked(f.core.lanStartNonce) {
		t.Fatal("prepared republication changed state or restored a window", err)
	}
	reply, err := localContextRuntimeReply(f, in, transcript)
	if err != nil || reply.(endpointmeta.ContextReply).State != "prepared" {
		t.Fatal("exact status data did not become locally reply-eligible", err)
	}
	localContextRuntimePublish(t, f)
	if f.writes != 1 || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("current receipt caused another publication")
	}
	commit := f.bound("pair-context-commit")
	a, err := f.capture(contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.apply(a, contextTranscript{Bound: commit}); !errors.Is(err, endpointmeta.ErrReview) || result.durable || f.writes != 1 || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("prepared republication restored first-commit permission", err, result)
	}
}

func TestContextInboundStatusExactBindingObservational(t *testing.T) {
	for _, phase := range []string{"prepared", "committed"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, phase)
			before, window, receipt := cloneDirectLANState(f.store.state), f.store.contextWindows[f.peer], f.store.contextPublication
			writes, revision := f.writes, f.store.reviewRevision
			// Status remains observational after the original preparation expires.
			f.now = f.now.Add(2 * time.Hour)
			bound := f.bound("pair-context-status")
			in := contextInputs{Operation: contextStatusInbound, PeerKey: f.peer, Bound: bound}
			result := f.step(t, in, contextTranscript{Bound: bound})
			if result != (contextSaveResult{phase: phase}) || f.writes != writes || f.store.reviewRevision != revision ||
				!reflect.DeepEqual(before, f.store.state) || f.store.contextWindows[f.peer] != window || f.store.contextPublication != receipt {
				t.Fatal("status renewed, confirmed or mutated saved state", result)
			}
			reply, err := localContextRuntimeReply(f, in, contextTranscript{Bound: bound})
			if err != nil || reply.(endpointmeta.ContextReply).State != phase {
				t.Fatal("exact binding lost its saved phase", err)
			}
			wrong := bound
			wrong.PairBinding = strings.Repeat("0", 64)
			bad := contextInputs{Operation: contextStatusInbound, PeerKey: f.peer, Bound: wrong}
			a, err := f.capture(bad)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := f.apply(a, contextTranscript{Bound: wrong}); !errors.Is(err, endpointmeta.ErrIdentity) || result != (contextSaveResult{}) {
				t.Fatal("wrong binding obtained status", err, result)
			}
			if _, err := localContextRuntimeReply(f, bad, contextTranscript{Bound: wrong}); !errors.Is(err, endpointmeta.ErrIdentity) || f.writes != writes || !reflect.DeepEqual(before, f.store.state) {
				t.Fatal("wrong binding obtained reply data or changed state", err)
			}
		})
	}
}

func TestContextDuplicateReplyRequiresCurrentProcessReceipt(t *testing.T) {
	for _, operation := range []string{"prepare", "commit", "status"} {
		t.Run(operation, func(t *testing.T) {
			f := newLocalContextFixture(t)
			phase := "committed"
			if operation == "prepare" {
				phase = "prepared"
			}
			f.advance(t, phase)
			in, transcript := contextInputs{PeerKey: f.peer}, contextTranscript{}
			if operation == "prepare" {
				in.Operation, transcript.Prepare = contextRecordInbound, f.request(false)
			} else {
				in.Operation = contextCommit
				if operation == "status" {
					in.Operation = contextStatusInbound
				}
				in.Bound = f.bound("pair-context-" + operation)
				transcript.Bound = in.Bound
			}
			f.reopen(t)
			before := cloneDirectLANState(f.store.state)
			result := f.step(t, in, transcript)
			if result.changed || result.published || result.durable || f.writes != 0 || f.store.contextPublication != nil {
				t.Fatal("observational duplicate inherited durability", result)
			}
			if _, err := localContextRuntimeReply(f, in, transcript); !errors.Is(err, endpointmeta.ErrReview) {
				t.Fatal("reopen alone qualified reply data", err)
			}
			localContextRuntimePublish(t, f)
			if _, err := localContextRuntimeReply(f, in, transcript); err != nil || f.writes != 1 || !reflect.DeepEqual(before, f.store.state) {
				t.Fatal("exact duplicate lacked its current-process receipt", err)
			}
			if _, err := f.store.contextReplyLocked("synthetic-other-process", in, transcript, f.now); !errors.Is(err, endpointmeta.ErrReview) {
				t.Fatal("receipt crossed process ownership", err)
			}
			if _, err := localContextRuntimeReply(f, in, contextTranscript{}); err == nil || f.writes != 1 {
				t.Fatal("receipt alone supplied an exact request")
			}
			// These are reducer inputs only. The completion gate below separately
			// proves that a receipt and association cannot replace a TLS proof.
			o := localContextRuntimeOwner(t, f)
			a, err := f.capture(in)
			if err != nil {
				t.Fatal(err)
			}
			attempt := &directlan.ContextAttempt{}
			o.operations[attempt] = &contextExchangeOperation{owner: o, attempt: attempt, admission: a, epoch: f.store.contextEpochLocked(), direction: directlan.ContextInbound}
			response, err := f.core.completeContextExchange(context.Background(), o, attempt, directlan.VerifiedContextExchange{})
			if !errors.Is(err, directlan.ErrUntrusted) || response.Reply != nil || response.Admit != nil || f.writes != 1 || len(o.operations) != 0 {
				t.Fatal("receipt/association replaced authentication", err)
			}
		})
	}
}

func TestContextOutboundCommitCapturesAfterLocalCommit(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	o := localContextRuntimeOwner(t, f)
	bound := f.bound("pair-context-commit")
	old, err := f.capture(contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound})
	if err != nil {
		t.Fatal(err)
	}
	writes := f.writes
	sawCommit := false
	f.store.write = func(path string, data []byte) error {
		f.writes++
		var candidate directLANState
		if err := json.Unmarshal(data, &candidate); err != nil {
			return err
		}
		sawCommit = candidate.Metadata.Peers[0].PairContext != nil && !candidate.Metadata.Peers[0].ContextConfirmed && f.store.state.Metadata.Peers[0].PairContext == nil
		return config.AtomicWritePrivate(path, data)
	}
	f.core.op.Lock()
	f.store.mu.Lock()
	a, request, epoch, err := f.store.contextExchangeAdmissionLocked(context.Background(), o, f.peer, directlan.ContextCommit, directlan.ContextOutbound, f.now)
	f.store.mu.Unlock()
	f.core.op.Unlock()
	if err != nil || !sawCommit || f.writes != writes+1 || request != bound || a.inputs.Operation != contextConfirmCommit || a.inputs.Bound != bound ||
		a.writeRevision != f.store.reviewRevision || a.writeRevision <= old.writeRevision || !epoch.Valid() || f.store.state.Metadata.Peers[0].ContextConfirmed {
		t.Fatal("outbound confirmation admission did not follow durable local commit", err)
	}
	if err := f.store.matchContextAdmissionLocked(o.process, old, f.now); !errors.Is(err, endpointmeta.ErrReview) {
		t.Fatal("pre-commit admission remained current", err)
	}
	if err := f.store.matchContextAdmissionLocked(o.process, a, f.now); err != nil || !f.store.contextPublicationCurrentLocked(o.process) {
		t.Fatal("post-commit admission/receipt was not current", err)
	}
	// Core cannot create an authentic reply proof. A zero proof must leave the
	// locally committed record unconfirmed, even with a complete association.
	attempt := &directlan.ContextAttempt{}
	o.operations[attempt] = &contextExchangeOperation{owner: o, attempt: attempt, admission: a, epoch: epoch, direction: directlan.ContextOutbound, operation: directlan.ContextCommit}
	response, err := f.core.completeContextExchange(context.Background(), o, attempt, directlan.VerifiedContextExchange{})
	if !errors.Is(err, directlan.ErrUntrusted) || response.Admit != nil || f.store.state.Metadata.Peers[0].ContextConfirmed || f.writes != writes+1 {
		t.Fatal("local commit or unverified reply confirmed the context", err)
	}
}

func TestContextCompletionRejectsUnverifiedAssociationsAndStaleResponseEpoch(t *testing.T) {
	for _, change := range []string{"nil attempt", "owner pointer", "store pointer", "process", "stopped owner", "missing association", "association owner", "association attempt", "zero proof"} {
		t.Run(change, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "prepared")
			o := localContextRuntimeOwner(t, f)
			attempt := &directlan.ContextAttempt{}
			bound := f.bound("pair-context-status")
			a, err := f.capture(contextInputs{Operation: contextStatusInbound, PeerKey: f.peer, Bound: bound})
			if err != nil {
				t.Fatal(err)
			}
			operation := &contextExchangeOperation{owner: o, attempt: attempt, admission: a, epoch: f.store.contextEpochLocked(), direction: directlan.ContextInbound, operation: directlan.ContextStatus}
			o.operations[attempt] = operation
			want := directlan.ErrUnavailable
			switch change {
			case "nil attempt":
				attempt, want = nil, endpointmeta.ErrInvalid
			case "owner pointer":
				f.core.contextControl = &contextControlOwner{}
			case "store pointer":
				f.core.directLAN = &directLANStore{}
			case "process":
				f.core.lanStartNonce = "synthetic-other-process"
			case "stopped owner":
				o.requestClose()
			case "missing association":
				delete(o.operations, attempt)
				want = endpointmeta.ErrReview
			case "association owner":
				operation.owner, want = &contextControlOwner{}, endpointmeta.ErrReview
			case "association attempt":
				operation.attempt, want = &directlan.ContextAttempt{}, endpointmeta.ErrReview
			case "zero proof":
				want = directlan.ErrUntrusted
			}
			before, writes := cloneDirectLANState(f.store.state), f.writes
			response, err := f.core.completeContextExchange(context.Background(), o, attempt, directlan.VerifiedContextExchange{})
			if !errors.Is(err, want) || response.Reply != nil || response.Epoch != nil || response.Admit != nil || f.writes != writes || !reflect.DeepEqual(before, f.store.state) {
				t.Fatal("inexact completion reached store or response", err)
			}
			if change == "zero proof" && len(o.operations) != 0 {
				t.Fatal("rejected claim retained its Core association")
			}
		})
	}
	// The Core response gate independently rejects a stale epoch. Reaching the
	// completion's post-claim epoch comparison needs an authentic TLS fixture.
	f := newLocalContextFixture(t)
	f.advance(t, "committed")
	o := localContextRuntimeOwner(t, f)
	attempt := &directlan.ContextAttempt{}
	bound := f.bound("pair-context-status")
	reply := endpointmeta.ContextReply{Version: 2, Operation: bound.Operation, OK: true, PairBinding: bound.PairBinding, State: "committed"}
	slot := localContextRuntimeSlot(f, o, attempt, reply)
	slot.epoch.Invalidate()
	if f.core.admitContextResponse(context.Background(), slot, attempt, reply) || !slot.used.Load() {
		t.Fatal("stale response epoch was accepted or left reusable")
	}
}

func TestContextCompletionContentionReturnsWithoutWaiting(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	o := localContextRuntimeOwner(t, f)
	attempt := &directlan.ContextAttempt{}
	o.operations[attempt] = &contextExchangeOperation{owner: o, attempt: attempt}
	before, writes := cloneDirectLANState(f.store.state), f.writes
	type completion struct {
		response directlan.ContextResponse
		err      error
	}
	done := make(chan completion, 1)
	f.core.op.Lock()
	defer f.core.op.Unlock()
	go func() {
		response, err := f.core.completeContextExchange(context.Background(), o, attempt, directlan.VerifiedContextExchange{})
		done <- completion{response, err}
	}()
	select {
	case got := <-done:
		if !errors.Is(got.err, directlan.ErrUnavailable) || got.response.Reply != nil || got.response.Admit != nil ||
			f.writes != writes || !reflect.DeepEqual(before, f.store.state) || len(o.operations) != 1 {
			t.Fatal("contended callback touched store/association or released success", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion waited for Core.op while its owner held the lock")
	}
	// Actual Node.Close work/join is intentionally absent here. Returning while
	// Core.op remains held is the Core side of the transport cleanup contract.
}

func TestContextWriteInvalidatesArmsAndResponseSameBytes(t *testing.T) {
	for _, mode := range []string{"same bytes", "unrelated peer name", "unpublished failure", "published uncertainty"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "committed")
			o := localContextRuntimeOwner(t, f)
			attempt := &directlan.ContextAttempt{}
			bound := f.bound("pair-context-status")
			reply := endpointmeta.ContextReply{Version: 2, Operation: bound.Operation, OK: true, PairBinding: bound.PairBinding, State: "committed"}
			slot := localContextRuntimeSlot(f, o, attempt, reply)
			armEpoch, revision, digest := slot.epoch, f.store.reviewRevision, f.store.fileDigest
			next := cloneDirectLANState(f.store.state)
			switch mode {
			case "unrelated peer name":
				next.Peers[1].Name = "synthetic-renamed-peer"
				next.Metadata.Peers[1].Peer.Name = next.Peers[1].Name
			case "unpublished failure":
				f.store.write = func(string, []byte) error { f.writes++; return errors.New("synthetic unpublished write") }
			case "published uncertainty":
				f.store.write = func(path string, data []byte) error {
					f.writes++
					if err := config.AtomicWritePrivate(path, data); err != nil {
						return err
					}
					return config.ErrAtomicCommitted
				}
			}
			f.core.op.Lock()
			f.store.mu.Lock()
			err := f.store.writeStateLocked(next)
			f.store.mu.Unlock()
			f.core.op.Unlock()
			failed := mode == "unpublished failure" || mode == "published uncertainty"
			if (err != nil) != failed || armEpoch.Valid() || f.store.contextEpoch != nil || f.store.contextPublication != nil || f.store.reviewRevision != revision+1 {
				t.Fatal("attempted write retained prior arm/receipt/epoch", err)
			}
			if mode == "same bytes" && f.store.fileDigest != digest {
				t.Fatal("same-byte fixture changed file contents")
			}
			if f.core.admitContextResponse(context.Background(), slot, attempt, reply) || !slot.used.Load() {
				t.Fatal("prior response survived a whole-file publication attempt")
			}
			if !failed {
				current := f.store.contextEpochLocked()
				if current == armEpoch || !current.Valid() {
					t.Fatal("later arm reused an invalidated epoch")
				}
			}
		})
	}
}

func TestContextPublicationCancellationPreservesSavedStateAndSuppressesResult(t *testing.T) {
	for _, route := range []string{"pre-arm publication", "outbound local commit"} {
		t.Run("stopped owner/"+route, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "prepared")
			if route == "pre-arm publication" {
				f.reopen(t)
			}
			o := localContextRuntimeOwner(t, f)
			o.requestClose()
			before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
			ctx := context.Background() // The independent caller remains live.
			f.core.op.Lock()
			f.store.mu.Lock()
			var err error
			if route == "pre-arm publication" {
				err = f.store.publishContextBeforeArmLocked(ctx, o, f.peer, f.now)
			} else {
				_, _, _, err = f.store.contextExchangeAdmissionLocked(ctx, o, f.peer, directlan.ContextCommit, directlan.ContextOutbound, f.now)
			}
			f.store.mu.Unlock()
			f.core.op.Unlock()
			if !errors.Is(err, context.Canceled) || ctx.Err() != nil || f.writes != writes || f.store.reviewRevision != revision ||
				!reflect.DeepEqual(before, f.store.state) || f.store.recovery {
				t.Fatal("adapter publication ignored its stopped owner", route, err)
			}
		})
	}
	for _, signal := range []string{"request", "owner context", "owner stop", "invalid attempt"} {
		t.Run("final publisher/"+signal, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "prepared")
			o := localContextRuntimeOwner(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			live := &contextSaveLiveness{ctx: ctx, owner: o}
			switch signal {
			case "request":
				cancel()
			case "owner context":
				o.cancel()
			case "owner stop":
				o.stopping.Store(true)
			case "invalid attempt":
				live.attempt = &directlan.ContextAttempt{}
			}
			before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
			epoch := f.store.contextEpochLocked()
			// Enter the publisher directly to test its last synchronous liveness
			// check, independently of the save helper's earlier checks.
			f.core.op.Lock()
			f.store.mu.Lock()
			err := f.store.writeContextStateLocked(cloneDirectLANState(before), live)
			f.store.mu.Unlock()
			f.core.op.Unlock()
			if !errors.Is(err, context.Canceled) || f.writes != writes || !reflect.DeepEqual(before, f.store.state) || f.store.recovery ||
				f.store.reviewRevision != revision+1 || epoch.Valid() || f.store.contextPublication != nil {
				t.Fatal("final writer admitted an already-terminal operation", err)
			}
		})
	}
	for _, signal := range []string{"request", "owner stop"} {
		t.Run("after durable save/"+signal, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.advance(t, "prepared")
			o := localContextRuntimeOwner(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bound := f.bound("pair-context-commit")
			a, err := f.capture(contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound})
			if err != nil {
				t.Fatal(err)
			}
			writes := f.writes
			f.store.write = func(path string, data []byte) error {
				f.writes++
				if err := config.AtomicWritePrivate(path, data); err != nil {
					return err
				}
				if signal == "request" {
					cancel()
				} else {
					o.requestClose()
				}
				return nil
			}
			f.core.op.Lock()
			f.store.mu.Lock()
			result, err := f.store.saveContextTransitionWithLivenessLocked(ctx, o.process, a, contextTranscript{Bound: bound}, f.now, &contextSaveLiveness{ctx: ctx, owner: o})
			f.store.mu.Unlock()
			f.core.op.Unlock()
			if !errors.Is(err, context.Canceled) || result != (contextSaveResult{published: true}) || f.writes != writes+1 || f.store.recovery ||
				f.store.state.Metadata.Peers[0].PairContext == nil || f.store.state.Metadata.Peers[0].ContextConfirmed {
				t.Fatal("cancellation rolled back publication or returned success", err, result)
			}
			disk, _, readErr := readDirectLANFile(f.store.path, 1<<20)
			if readErr != nil || !reflect.DeepEqual(disk, f.store.state) {
				t.Fatal("durably published cancellation lost adoption", readErr)
			}
			attempt := &directlan.ContextAttempt{}
			reply := endpointmeta.ContextReply{Version: 2, Operation: bound.Operation, OK: true, PairBinding: bound.PairBinding, State: "committed"}
			slot := localContextRuntimeSlot(f, o, attempt, reply)
			if f.core.admitContextResponse(ctx, slot, attempt, reply) {
				t.Fatal("stopped completion admitted reply data after saving")
			}
		})
	}
}

func TestContextUncertainPublicationAdoptedWithoutReceipt(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	o := localContextRuntimeOwner(t, f)
	bound := f.bound("pair-context-commit")
	a, err := f.capture(contextInputs{Operation: contextCommit, PeerKey: f.peer, Bound: bound})
	if err != nil {
		t.Fatal(err)
	}
	epoch := f.store.contextEpochLocked()
	f.store.write = func(path string, data []byte) error {
		f.writes++
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		return config.ErrAtomicCommitted
	}
	f.core.op.Lock()
	f.store.mu.Lock()
	result, err := f.store.saveContextTransitionWithLivenessLocked(context.Background(), o.process, a, contextTranscript{Bound: bound}, f.now, &contextSaveLiveness{ctx: context.Background(), owner: o})
	f.store.mu.Unlock()
	f.core.op.Unlock()
	if !errors.Is(err, config.ErrAtomicCommitted) || result != (contextSaveResult{published: true}) || !f.store.recovery || f.store.contextPublication != nil || epoch.Valid() {
		t.Fatal("uncertain save released receipt/success or retained old epoch", err, result)
	}
	disk, _, err := readDirectLANFile(f.store.path, 1<<20)
	if err != nil || !reflect.DeepEqual(disk, f.store.state) || disk.Metadata.Peers[0].PairContext == nil {
		t.Fatal("uncertain publication was not adopted", err)
	}
	if _, err := localContextRuntimeReply(f, a.inputs, contextTranscript{Bound: bound}); !errors.Is(err, endpointmeta.ErrReview) {
		t.Fatal("uncertainty produced eligible reply data", err)
	}
	// The store boundary owns no shutdown callback. The completeContextExchange
	// caller's deferred unlock-before-requestClose order needs authentic inbound
	// completion coverage; invoking shutdown here would only test this fixture.
	if o.stopping.Load() || o.ctx.Err() != nil {
		t.Fatal("store save itself invoked owner shutdown")
	}
}

func TestContextControlExistingOwnerExcludesOrdinaryBackend(t *testing.T) {
	for _, phase := range []string{"building", "active", "stopping", "retained failure"} {
		t.Run(phase, func(t *testing.T) {
			f := newLocalContextFixture(t)
			o := localContextRuntimeOwner(t, f)
			if phase != "building" {
				close(o.started)
			}
			if phase == "stopping" || phase == "retained failure" {
				o.requestClose()
			}
			if phase == "retained failure" {
				o.closeErr = errors.New("synthetic retained terminal error")
			}
			// This path must return before profile/backend construction. The
			// profile is deliberately unconfigured, so even a broken exclusion
			// cannot fall through into a real network constructor.
			if err := f.core.startNetwork(context.Background()); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" || f.core.contextControl != o || f.core.attemptedNetwork != "" {
				t.Fatal("existing control owner did not exclude ordinary startup", err)
			}
		})
	}
	for _, attempted := range []string{"direct-lan", "lan", "tailnet", "mixed"} {
		t.Run("prior ordinary attempt/"+attempted, func(t *testing.T) {
			f := newLocalContextFixture(t)
			f.core.attemptedNetwork = attempted
			f.core.op.Lock()
			_, _, err := f.core.contextStoreLocked()
			f.core.op.Unlock()
			if networkErrorCode(err) != "direct_lan_endpoint_integration_pending" || f.core.attemptedNetwork != attempted || f.core.contextControl != nil {
				t.Fatal("ordinary attempt marker was reset or control construction began", err)
			}
		})
	}
	// This tests Core's handling of an already-recorded successful terminal
	// result, not execution of Node.Close, a builder or a transport join.
	f := newLocalContextFixture(t)
	o := localContextRuntimeOwner(t, f)
	o.closeOnce.Do(func() {})
	close(o.started)
	f.core.op.Lock()
	err := f.core.stopContextControlLocked()
	f.core.op.Unlock()
	if err != nil || f.core.contextControl != nil || !o.stopping.Load() {
		t.Fatal("recorded successful cleanup retained the Core owner", err)
	}
	replacement := localContextRuntimeOwner(t, f)
	if err := f.core.currentContextOwnerLocked(replacement); err != nil {
		t.Fatal("fresh offline ownership was not available after recorded cleanup", err)
	}
}

func TestContextControlRecordedCloseErrorRemainsRetained(t *testing.T) {
	f := newLocalContextFixture(t)
	f.advance(t, "prepared")
	o := localContextRuntimeOwner(t, f)
	attempt := &directlan.ContextAttempt{}
	o.operations[attempt] = &contextExchangeOperation{owner: o, attempt: attempt}
	epoch := f.store.contextEpochLocked()
	failure := errors.New("synthetic retained close failure")
	// Inject only an already-recorded terminal result. The DirectLAN fixture
	// must separately establish that real failed Close produces this result.
	o.closeOnce.Do(func() { o.closeErr = failure })
	close(o.started)
	for i := 0; i < 2; i++ {
		f.core.op.Lock()
		err := f.core.stopContextControlLocked()
		f.core.op.Unlock()
		if !errors.Is(err, failure) || f.core.contextControl != o || !o.stopping.Load() || o.ctx.Err() == nil || len(o.operations) != 0 || epoch.Valid() || f.store.contextEpoch != nil {
			t.Fatal("failed terminal result lost owner, error or invalidation", i, err)
		}
		if err := f.core.startNetwork(context.Background()); networkErrorCode(err) != "direct_lan_endpoint_integration_pending" {
			t.Fatal("retained cleanup failure stopped excluding ordinary startup", err)
		}
	}
}
