package core

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These are isolated store-gate tests, not authenticated transport evidence or
// product activation. The existing reducer fixture obtains its receipt through
// the sole publisher. No Node constructor, Start, socket or preparer is used.
func localManagedCompletionOwner(t *testing.T) (*localContextFixture, *managedCompletionOwner) {
	t.Helper()
	f := newLocalContextFixture(t)
	f.advance(t, "committed")
	request := f.bound("pair-context-status")
	f.step(t, contextInputs{Operation: contextConfirmStatus, PeerKey: f.peer, Bound: request}, contextTranscript{
		Committed: endpointmeta.ContextReply{Version: 2, Operation: request.Operation, PairBinding: request.PairBinding, OK: true, State: "committed"}})
	f.store.contextEpoch = directlan.NewContextEpoch()
	o := &managedCompletionOwner{core: f.core, coreDone: f.core.ctx.Done(), store: f.store, process: f.core.lanStartNonce, configuration: contextConfigurationDigest(f.store.state),
		receipt: f.store.contextPublication, epoch: f.store.contextEpoch, revision: f.store.reviewRevision, limits: *f.store.currentCapacity(), limitsSource: f.store.limits.Load()}
	f.store.write = func(string, []byte) error {
		t.Error("read-only completion attempted publication")
		return errors.New("forbidden write")
	}
	return f, o
}

func TestManagedCompletionStoreObservesConfirmedCommitAndStatus(t *testing.T) {
	for _, operation := range []string{"pair-context-commit", "pair-context-status"} {
		t.Run(operation, func(t *testing.T) {
			f, o := localManagedCompletionOwner(t)
			before, revision, receipt, epoch, writes := f.store.copy(), f.store.reviewRevision, f.store.contextPublication, f.store.contextEpoch, f.writes
			raw := offlineEndpointRead(t, f.store.path)
			f.store.mu.Lock()
			reply, err := o.currentReplyLocked(f.peer, f.bound(operation), time.Now())
			f.store.mu.Unlock()
			if err != nil || reply.Operation != operation || !reply.OK || reply.State != "committed" || reply.PairBinding != f.bound(operation).PairBinding {
				t.Fatal("exact confirmed observation rejected", err)
			}
			if !reflect.DeepEqual(before, f.store.copy()) || f.store.reviewRevision != revision || f.store.contextPublication != receipt || f.store.contextEpoch != epoch || f.writes != writes || string(raw) != string(offlineEndpointRead(t, f.store.path)) {
				t.Fatal("observation changed authority or saved bytes")
			}
		})
	}
}

func TestManagedCompletionStoreRejectsChangedEvidence(t *testing.T) {
	for _, change := range []string{"binding", "operation", "version", "peer", "receipt absent", "receipt replaced", "process", "path", "revision", "capacity", "capacity identity", "epoch", "stopped", "recovery", "file", "clock", "unconfirmed", "terminal"} {
		t.Run(change, func(t *testing.T) {
			f, o := localManagedCompletionOwner(t)
			key, request := f.peer, f.bound("pair-context-status")
			now := time.Now()
			switch change {
			case "binding":
				request.PairBinding = strings.Repeat("0", 64)
			case "operation":
				request.Operation = "session"
			case "version":
				request.Version = 1
			case "peer":
				key = f.store.state.Metadata.Peers[1].Peer.Key
			case "receipt absent":
				f.store.contextPublication = nil
			case "receipt replaced":
				copy := *f.store.contextPublication
				f.store.contextPublication = &copy
			case "process":
				o.process = "different-process"
			case "path":
				f.store.path += ".absent"
			case "revision":
				f.store.reviewRevision++
			case "capacity":
				f.store.limits.Store(&lanStoreLimits{peers: o.limits.peers + 1, bytes: o.limits.bytes})
			case "capacity identity":
				copy := o.limits
				f.store.limits.Store(&copy)
			case "epoch":
				o.epoch.Invalidate()
			case "stopped":
				o.invalidate()
			case "recovery":
				f.store.recovery = true
			case "file":
				if err := os.WriteFile(f.store.path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "clock":
				now = f.store.endpointObservedAt.Add(-time.Second)
			case "unconfirmed":
				f.store.state.Metadata.Peers[0].ContextConfirmed = false
			case "terminal":
				f.store.state = pairRecordV4(t, f.store.state, time.Now(), true)
			}
			f.store.mu.Lock()
			_, err := o.currentReplyLocked(key, request, now)
			f.store.mu.Unlock()
			if err == nil {
				t.Fatal("changed evidence released completion", change)
			}
		})
	}
}

func TestManagedCompletionRequiresFreshOrdinaryEpochAfterContextJoin(t *testing.T) {
	f, o := localManagedCompletionOwner(t)
	old, receipt := o.epoch, o.receipt
	// stopContextControlLocked invalidates/removes exactly this old signal. Model
	// that boundary without invoking a Node lifecycle or pretending it is joined.
	old.Invalidate()
	f.store.contextEpoch = nil
	request := f.bound("pair-context-status")
	if _, err := o.currentReplyLocked(f.peer, request, time.Now()); err == nil {
		t.Fatal("joined context epoch remained usable")
	}
	if f.store.contextEpoch != nil || f.store.contextPublication != receipt {
		t.Fatal("observation minted epoch or receipt")
	}
	// The candidate constructor (not a callback) creates a distinct ordinary
	// cancellation signal after join. This fixture checks the store boundary only.
	fresh := directlan.NewContextEpoch()
	o.epoch, f.store.contextEpoch = fresh, fresh
	if _, err := o.currentReplyLocked(f.peer, request, time.Now()); err != nil {
		t.Fatal("fresh signal lost the same durable fact", err)
	}
	if fresh == old || old.Valid() || f.store.contextPublication != receipt {
		t.Fatal("old signal revived or receipt replaced")
	}
	f.store.contextPublication = nil
	if _, err := o.currentReplyLocked(f.peer, request, time.Now()); err == nil {
		t.Fatal("fresh epoch substituted for a receipt")
	}
}

func TestManagedCompletionSlotConsumesRejectedDuplicateAndLateCalls(t *testing.T) {
	f, o := localManagedCompletionOwner(t)
	// Populate only inert owner identities, not a running Node. This isolates
	// rejection at the missing transport identity rather than an absent owner.
	o.node = &directlan.Node{}
	o.backend = &directLANBackend{Node: o.node, ctx: f.core.ctx, store: f.store, ready: true, completion: o}
	f.core.node = o.backend
	o.root = o.backend
	// A zero transport request must never become evidence, even with a genuine
	// store receipt. Rejection still consumes the one-shot response slot.
	slot := &managedCompletionSlot{owner: o, request: &directlan.ManagedCompletionRequest{}, deadline: time.Now().Add(time.Minute)}
	if slot.admit(context.Background()) || !slot.used.Load() || slot.admit(context.Background()) {
		t.Fatal("zero or duplicate transport request admitted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{cancelled, context.Background()} {
		late := &managedCompletionSlot{owner: o, deadline: time.Now().Add(-time.Second)}
		if late.admit(ctx) || !late.used.Load() {
			t.Fatal("late/cancelled token remained reusable")
		}
	}
	if f.writes != 4 {
		t.Fatal("response gate unexpectedly wrote", f.writes)
	}
}

// Provisional B/C1 integration: these are inert Core ownership predicates only,
// never a transport request, ordinary constructor or positive send admission.
func TestManagedCompletionCoreOwnerRejectsRemovalAndCleanup(t *testing.T) {
	for _, change := range []string{"none", "backend", "store", "context owner", "process", "closing", "cancel", "cleanup pending", "cleanup error", "removal", "raw denial", "mixed denial", "unrelated denial"} {
		t.Run(change, func(t *testing.T) {
			f, o := localManagedCompletionOwner(t)
			o.node = &directlan.Node{}
			o.backend = &directLANBackend{Node: o.node, ctx: f.core.ctx, store: f.store, ready: true, completion: o}
			f.core.node = o.backend
			o.root = o.backend
			switch change {
			case "backend":
				f.core.node = &directLANBackend{}
			case "store":
				f.core.directLAN = &directLANStore{}
			case "context owner":
				f.core.contextControl = &contextControlOwner{}
			case "process":
				f.core.lanStartNonce = "replacement-process"
			case "closing":
				f.core.closing = true
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.core.ctx = ctx
			case "cleanup pending":
				f.core.managedCleanupPending = true
			case "cleanup error":
				f.core.managedCleanupError = errors.New("synthetic retained cleanup error")
			case "removal":
				f.core.managedRemoval = &managedRemovalOwner{}
			case "raw denial":
				f.core.managedDenied = map[string]bool{f.peer: true}
			case "mixed denial":
				f.core.managedDenied = map[string]bool{mixedID("direct-lan", f.peer): true}
			case "unrelated denial":
				f.core.managedDenied = map[string]bool{f.store.state.Peers[1].Key: true}
			}
			want := change == "none" || change == "unrelated denial"
			if o.coreCurrent(f.peer) != want {
				t.Fatal("wrong frozen Core owner/denial result", change)
			}
		})
	}
}
