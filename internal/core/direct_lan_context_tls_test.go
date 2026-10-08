//go:build directlan_context_fixture

package core

import (
	"bytes"
	"context"
	"errors"
	"go/build"
	"go/build/constraint"
	"io/fs"
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

// This opt-in closure authenticates synthetic peers through the ordinary
// DirectLAN accept/TLS/completion path using one owned in-memory connection.
// Only the local review uses the existing store fixture. Prepared and committed
// remote state always comes from the genuine completion below. No application,
// outbound Exchange, OS listener, endpoint movement or real-device acceptance
// is established here. The exact last publisher-admission interleaving remains
// outside this seam; writer hooks below act after that admission.
const contextTLSFixtureTimeout = 5 * time.Second

type contextTLSObservation struct {
	calls                     int
	completionErr             error
	hasResponse               bool
	oldEpochInvalid           bool
	currentResponseEpoch      bool
	currentReceipt            bool
	writesBefore, writesAfter int
}

type contextTLSRound struct {
	f      *localContextFixture
	owner  *contextControlOwner
	bridge *directlan.ContextFixtureBridge
	seen   contextTLSObservation
	before func(context.Context, *directlan.ContextAttempt)
	after  func(context.Context, *directlan.ContextAttempt, directlan.VerifiedContextExchange, directlan.ContextResponse, error)
}

func newContextTLSLocalReview(t *testing.T) *localContextFixture {
	t.Helper()
	f := newLocalContextFixture(t)
	f.step(t, f.prepareInput(), contextTranscript{})
	if f.writes != 1 || f.store.state.Metadata.Peers[0].UpgradePending == nil || contextSavedPair(f.store.state.Metadata.Peers[0]) != nil {
		t.Fatal("local review did not leave exactly one reviewed-only publication")
	}
	return f
}

func newContextTLSRound(t *testing.T, f *localContextFixture, gate *directlan.ContextFixtureReadGate) *contextTLSRound {
	t.Helper()
	// A prior one-shot owner must finish before a replacement uses this store.
	f.core.op.Lock()
	err := f.core.stopContextControlLocked()
	f.core.op.Unlock()
	if err != nil {
		t.Fatal("previous Core owner did not join", err)
	}
	f.store.mu.Lock()
	cfg, configuration, err := f.store.contextConfigLocked(time.Now(), 4)
	f.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	lifetime, cancel := context.WithCancel(f.core.ctx)
	o := &contextControlOwner{store: f.store, process: f.core.lanStartNonce, configuration: configuration,
		ctx: lifetime, cancel: cancel, started: make(chan struct{}), operations: make(map[*directlan.ContextAttempt]*contextExchangeOperation)}
	r := &contextTLSRound{f: f, owner: o}
	cfg.Completion = func(ctx context.Context, attempt *directlan.ContextAttempt, verified directlan.VerifiedContextExchange) (directlan.ContextResponse, error) {
		r.seen.calls++
		if r.before != nil {
			r.before(ctx, attempt)
		}
		r.seen.writesBefore = f.writes
		response, err := f.core.completeContextExchange(ctx, o, attempt, verified)
		r.seen.completionErr, r.seen.writesAfter = err, f.writes
		r.seen.hasResponse = response.Reply != nil && response.Epoch != nil && response.Admit != nil
		if err == nil {
			f.store.mu.Lock()
			r.seen.currentResponseEpoch = response.Epoch != nil && response.Epoch.Valid() && response.Epoch == f.store.contextEpoch
			r.seen.currentReceipt = f.store.contextPublicationCurrentLocked(o.process)
			f.store.mu.Unlock()
		}
		if r.after != nil {
			r.after(ctx, attempt, verified, response, err)
		}
		// Keep the genuine one-shot admission and reply unchanged. Neither the
		// response nor the received evidence is retained after this callback.
		return response, err
	}
	remote := directlan.Identity{Seed: strings.Repeat("02", 32)}
	bridge, err := directlan.NewContextFixtureBridge(cfg, remote, gate)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	r.bridge, o.node = bridge, bridge.Node()
	t.Cleanup(func() { r.stop(t) })
	f.core.mu.Lock()
	f.core.contextControl = o
	f.core.mu.Unlock()
	close(o.started)
	return r
}

func (r *contextTLSRound) stop(t *testing.T) {
	t.Helper()
	// A failed join gives no permission to inspect callback-owned observations.
	if err := r.bridge.Close(); err != nil {
		t.Fatal("fixture transport did not join", err)
	}
	r.f.core.op.Lock()
	r.f.core.mu.RLock()
	current := r.f.core.contextControl == r.owner
	r.f.core.mu.RUnlock()
	var err error
	if current {
		err = r.f.core.stopContextControlLocked()
	} else {
		// Also clean the actual fixture owner if a failed assertion leaves a
		// changed current pointer. Never stop a different, later Core owner.
		r.owner.requestClose()
		for attempt := range r.owner.operations {
			attempt.Cancel()
			delete(r.owner.operations, attempt)
		}
		err = r.owner.close()
	}
	remaining := len(r.owner.operations)
	r.f.core.op.Unlock()
	if err != nil || remaining != 0 {
		t.Fatalf("Core owner retained work after stop: operations=%d, error=%v", remaining, err)
	}
}

func (r *contextTLSRound) arm(t *testing.T, operation directlan.ContextOperation) *contextExchangeOperation {
	t.Helper()
	r.f.core.op.Lock()
	armed, err := r.f.core.captureContextExchangeLocked(context.Background(), r.owner, r.f.peer, operation, directlan.ContextInbound)
	r.f.core.op.Unlock()
	if err != nil {
		t.Fatal("ordinary inbound capture failed", err)
	}
	return armed
}

func (r *contextTLSRound) exchange(t *testing.T, request []byte) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contextTLSFixtureTimeout)
	defer cancel()
	reply, err := r.bridge.Exchange(ctx, request)
	if errors.Is(err, directlan.ErrContextFixtureCleanup) {
		t.Fatal("exchange cleanup exceeded its bound; no completion observations may be inspected", err)
	}
	if closeErr := r.bridge.Close(); closeErr != nil {
		t.Fatal("transport join failed; callback observations are not safe to inspect", closeErr)
	}
	return reply, err
}

func contextTLSRequest(t *testing.T, request endpointmeta.Request) []byte {
	t.Helper()
	data, err := endpointmeta.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func contextTLSPrepared(t *testing.T, f *localContextFixture) {
	t.Helper()
	r := newContextTLSRound(t, f, nil)
	r.arm(t, directlan.ContextPrepare)
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if err != nil || len(reply) == 0 || r.seen.calls != 1 || r.seen.completionErr != nil || f.store.state.Metadata.Peers[0].UpgradePending.Context == nil {
		t.Fatal("authentic prepare prerequisite failed", err, r.seen.completionErr)
	}
	r.stop(t)
}

func contextTLSCommitted(t *testing.T, f *localContextFixture) {
	t.Helper()
	contextTLSPrepared(t, f)
	r := newContextTLSRound(t, f, nil)
	r.arm(t, directlan.ContextCommit)
	reply, err := r.exchange(t, contextTLSRequest(t, f.bound("pair-context-commit")))
	if err != nil || len(reply) == 0 || r.seen.calls != 1 || r.seen.completionErr != nil || f.store.state.Metadata.Peers[0].PairContext == nil {
		t.Fatal("authentic commit prerequisite failed", err, r.seen.completionErr)
	}
	r.stop(t)
}

func contextTLSAssertDisk(t *testing.T, f *localContextFixture) {
	t.Helper()
	saved, digest, err := readDirectLANFile(f.store.path, f.store.bytes)
	if err != nil || digest != f.store.fileDigest || !reflect.DeepEqual(saved, f.store.state) {
		t.Fatal("adopted whole-file state differs from private disk state", err)
	}
}

func contextTLSAssertNoApplication(t *testing.T, f *localContextFixture) {
	t.Helper()
	if f.store.state.Metadata.Peers[0].ContextConfirmed || f.core.node != nil || f.core.networkReady.Load() {
		t.Fatal("inbound control completion granted confirmation or application readiness")
	}
	recovery := f.store.recovery
	_, err := f.store.runtimeConfig()
	if recovery {
		if !errors.Is(err, directlan.ErrRecovery) {
			t.Fatal("uncertain saved state bypassed its recovery guard", err)
		}
	} else if networkErrorCode(err) != "direct_lan_endpoint_integration_pending" {
		t.Fatal("managed-state activation guard changed", err)
	}
}

func TestContextCoreTLSPreparePublishesBeforeReply(t *testing.T) {
	f := newContextTLSLocalReview(t)
	r := newContextTLSRound(t, f, nil)
	armed := r.arm(t, directlan.ContextPrepare)
	setupWrites, setupRevision := f.writes, f.store.reviewRevision
	var publishedBeforeResponse bool
	var diskErr error
	r.after = func(_ context.Context, _ *directlan.ContextAttempt, _ directlan.VerifiedContextExchange, response directlan.ContextResponse, err error) {
		if err != nil {
			return
		}
		saved, digest, readErr := readDirectLANFile(f.store.path, f.store.bytes)
		diskErr = readErr
		publishedBeforeResponse = readErr == nil && reflect.DeepEqual(saved, f.store.state) && digest == f.store.fileDigest
		r.seen.oldEpochInvalid = !armed.epoch.Valid() && response.Epoch != armed.epoch
	}
	request := f.request(false)
	replyBytes, err := r.exchange(t, contextTLSRequest(t, request))
	if err != nil || r.seen.calls != 1 || r.seen.completionErr != nil || !r.seen.hasResponse || !publishedBeforeResponse || diskErr != nil {
		t.Fatal("prepare did not publish before returning its real response", err, r.seen.completionErr, diskErr)
	}
	parsed, err := endpointmeta.ParseReply(replyBytes, request.Operation)
	if err != nil {
		t.Fatal("framed TLS reply was not a canonical prepare reply", err)
	}
	reply, ok := parsed.(*endpointmeta.PrepareReply)
	saved := f.store.state.Metadata.Peers[0].UpgradePending.Context
	binding, bindErr := saved.Binding()
	if !ok || bindErr != nil || !reply.OK || reply.PairBinding != binding || !reflect.DeepEqual(reply.PairContext, *saved) {
		t.Fatal("reply did not describe the exact newly published context", bindErr)
	}
	if f.writes != setupWrites+1 || f.store.reviewRevision != setupRevision+1 || r.seen.writesAfter != r.seen.writesBefore+1 ||
		!r.seen.oldEpochInvalid || !r.seen.currentResponseEpoch || !r.seen.currentReceipt || len(r.owner.operations) != 1 || r.owner.operations[armed.attempt] != armed || !armed.attempt.Cancelled() {
		t.Fatal("successful completion lost its response epoch or retained live work after transport join")
	}
	contextTLSAssertDisk(t, f)
	contextTLSAssertNoApplication(t, f)
}

func TestContextCoreTLSCommitAndStatusAreReceiptBound(t *testing.T) {
	for _, name := range []string{"prepared-status", "commit-save", "duplicate-commit", "committed-status", "wrong-commit-binding", "wrong-status-binding", "reopened-prepared-status", "reopened-committed-status"} {
		t.Run(name, func(t *testing.T) {
			f := newContextTLSLocalReview(t)
			committed := name == "duplicate-commit" || name == "committed-status" || name == "reopened-committed-status"
			if committed {
				contextTLSCommitted(t, f)
			} else {
				contextTLSPrepared(t, f)
			}
			if strings.HasPrefix(name, "reopened-") {
				f.reopen(t)
				if f.store.contextPublication != nil || len(f.store.contextWindows) != 0 {
					t.Fatal("reading the saved file recreated process publication or preparation permission")
				}
			}
			operation, wireOperation := directlan.ContextStatus, "pair-context-status"
			if name == "commit-save" || name == "duplicate-commit" || name == "wrong-commit-binding" {
				operation, wireOperation = directlan.ContextCommit, "pair-context-commit"
			}
			r := newContextTLSRound(t, f, nil)
			beforeArm := f.writes
			r.arm(t, operation)
			republication := 0
			if strings.HasPrefix(name, "reopened-") {
				republication = 1
			}
			if f.writes != beforeArm+republication {
				t.Fatal("pre-arm republication count did not match current-process receipt ownership")
			}
			before, writes, receipt := cloneDirectLANState(f.store.state), f.writes, f.store.contextPublication
			request := f.bound(wireOperation)
			wrong := strings.HasPrefix(name, "wrong-")
			if wrong {
				request.PairBinding = strings.Repeat("0", 64)
			}
			replyBytes, err := r.exchange(t, contextTLSRequest(t, request))
			if wrong {
				if err == nil || len(replyBytes) != 0 || r.seen.calls != 0 || f.writes != writes || !reflect.DeepEqual(before, f.store.state) || f.store.contextPublication != receipt {
					t.Fatal("wrong binding passed the exact authenticated request gate", err)
				}
				return
			}
			if err != nil || r.seen.calls != 1 || r.seen.completionErr != nil || !r.seen.currentReceipt || !r.seen.currentResponseEpoch {
				t.Fatal("exact exchange lacked its real current receipt", err, r.seen.completionErr)
			}
			parsed, err := endpointmeta.ParseReply(replyBytes, wireOperation)
			if err != nil {
				t.Fatal(err)
			}
			reply, ok := parsed.(*endpointmeta.ContextReply)
			phase, saveCount := "prepared", 0
			if committed || name == "commit-save" {
				phase = "committed"
			}
			if name == "commit-save" {
				saveCount = 1
			}
			if !ok || !reply.OK || reply.PairBinding != request.PairBinding || reply.State != phase || f.writes != writes+saveCount || r.seen.writesAfter != r.seen.writesBefore+saveCount {
				t.Fatal("phase reply or save count did not match the exact operation")
			}
			if saveCount == 0 && (!reflect.DeepEqual(before, f.store.state) || f.store.contextPublication != receipt) {
				t.Fatal("observational status or duplicate commit changed publication ownership")
			}
			if republication != 0 && len(f.store.contextWindows) != 0 {
				t.Fatal("republication recreated first-commit permission")
			}
			contextTLSAssertDisk(t, f)
			contextTLSAssertNoApplication(t, f)
		})
	}
}

func TestContextCoreTLSCompletionRejectsStaleAdmission(t *testing.T) {
	for _, name := range []string{"core-op-contention", "changed-process", "changed-store", "changed-current-owner", "wrong-association-pointer", "same-byte-revision", "genuine-proof-reuse"} {
		t.Run(name, func(t *testing.T) {
			f := newContextTLSLocalReview(t)
			r := newContextTLSRound(t, f, nil)
			armed := r.arm(t, directlan.ContextPrepare)
			before, writes, digest := cloneDirectLANState(f.store.state), f.writes, f.store.fileDigest
			var replacementStore *directLANStore
			if name == "changed-store" {
				var err error
				replacementStore, err = readDirectLANStore(f.store.path, f.store.bytes, f.store.peers)
				if err != nil || replacementStore == nil || replacementStore == f.store {
					t.Fatal("could not read the same private state into a distinct store", err)
				}
			}
			// This distinct pointer has the same live fields but owns no new
			// lifecycle. It is used only to test the current-owner identity gate.
			replacementOwner := &contextControlOwner{node: r.owner.node, store: r.owner.store, process: r.owner.process,
				configuration: r.owner.configuration, ctx: r.owner.ctx, cancel: r.owner.cancel,
				started: r.owner.started, operations: r.owner.operations}
			var hookErr, reuseErr, secondReuseErr, repeatCompletionErr error
			var repeatedResponse bool
			var associationsAfter int
			r.before = func(_ context.Context, attempt *directlan.ContextAttempt) {
				switch name {
				case "core-op-contention":
					f.core.op.Lock()
				case "changed-process", "changed-store", "changed-current-owner":
					f.core.mu.Lock()
					switch name {
					case "changed-process":
						f.core.lanStartNonce = "synthetic-replacement-process"
					case "changed-store":
						f.core.directLAN = replacementStore
					case "changed-current-owner":
						f.core.contextControl = replacementOwner
					}
					f.core.mu.Unlock()
				case "wrong-association-pointer":
					f.core.op.Lock()
					copyOfAttempt := *attempt
					armed.attempt = &copyOfAttempt
					f.core.op.Unlock()
				case "same-byte-revision":
					f.core.op.Lock()
					f.store.mu.Lock()
					hookErr = f.store.writeStateLocked(cloneDirectLANState(f.store.state))
					f.store.mu.Unlock()
					f.core.op.Unlock()
				}
			}
			r.after = func(ctx context.Context, attempt *directlan.ContextAttempt, verified directlan.VerifiedContextExchange, _ directlan.ContextResponse, _ error) {
				// Restore only the test's pointer mutation. Cancellation and the
				// genuine proof's claim state are never restored or replaced.
				switch name {
				case "core-op-contention":
					f.core.op.Unlock()
				case "changed-process", "changed-store", "changed-current-owner":
					f.core.mu.Lock()
					f.core.lanStartNonce, f.core.directLAN, f.core.contextControl = r.owner.process, f.store, r.owner
					f.core.mu.Unlock()
				case "wrong-association-pointer":
					f.core.op.Lock()
					armed.attempt = attempt
					f.core.op.Unlock()
				case "genuine-proof-reuse":
					response, err := f.core.completeContextExchange(ctx, r.owner, attempt, verified)
					repeatCompletionErr = err
					repeatedResponse = response.Reply != nil || response.Epoch != nil || response.Admit != nil
				}
				// Copies of the authentic proof are tried synchronously while the
				// callback still owns it. Early rejection may leave its claim bit
				// unused, but its canceled attempt still cannot be claimed.
				copyOfVerified := verified
				_, reuseErr = copyOfVerified.TryClaimFor(attempt)
				_, secondReuseErr = verified.TryClaimFor(attempt)
				f.core.op.Lock()
				associationsAfter = len(r.owner.operations)
				f.core.op.Unlock()
			}
			reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
			if err == nil || len(reply) != 0 || r.seen.calls != 1 || hookErr != nil || reuseErr == nil || !errors.Is(secondReuseErr, directlan.ErrUntrusted) {
				t.Fatal("stale admission or genuine proof reuse escaped rejection", err, hookErr, reuseErr, secondReuseErr)
			}
			if name == "genuine-proof-reuse" {
				if r.seen.completionErr != nil || !r.seen.hasResponse || !errors.Is(repeatCompletionErr, endpointmeta.ErrReview) || repeatedResponse || f.writes != writes+1 || associationsAfter != 0 {
					t.Fatal("reused completion saved again or produced another response", r.seen.completionErr, repeatCompletionErr)
				}
			} else {
				extraWrite, retained := 0, 1
				if name == "same-byte-revision" {
					extraWrite, retained = 1, 0
				}
				if r.seen.completionErr == nil || r.seen.hasResponse || r.seen.writesBefore != r.seen.writesAfter || f.writes != writes+extraWrite ||
					!reflect.DeepEqual(before, f.store.state) || f.store.fileDigest != digest || associationsAfter != retained {
					t.Fatal("rejected completion changed state or misreported early association cleanup", r.seen.completionErr)
				}
			}
			contextTLSAssertDisk(t, f)
			r.stop(t)
			if len(r.owner.operations) != 0 || !armed.attempt.Cancelled() {
				t.Fatal("terminal owner stop left an association or reusable attempt")
			}
		})
	}
}

type contextTLSExchangeResult struct {
	reply []byte
	err   error
}

func contextTLSWaitReached(t *testing.T, r *contextTLSRound, gate *directlan.ContextFixtureReadGate) {
	t.Helper()
	select {
	case <-gate.Reached():
	case <-time.After(contextTLSFixtureTimeout):
		_ = r.bridge.Close()
		t.Fatal("production accept did not reach the first server read")
	}
}

func contextTLSWaitExchange(t *testing.T, r *contextTLSRound, done <-chan contextTLSExchangeResult) contextTLSExchangeResult {
	t.Helper()
	select {
	case result := <-done:
		if errors.Is(result.err, directlan.ErrContextFixtureCleanup) {
			t.Fatal("exchange cleanup exceeded its bound; no completion observations may be inspected", result.err)
		}
		if err := r.bridge.Close(); err != nil {
			t.Fatal("transport did not join; callback observations remain unavailable", err)
		}
		return result
	case <-time.After(2 * contextTLSFixtureTimeout):
		_ = r.bridge.Close()
		t.Fatal("one owned exchange failed to return before its bounded cleanup deadline")
		return contextTLSExchangeResult{}
	}
}

func TestContextCoreTLSPreReadCutoffRejectsLateArm(t *testing.T) {
	for _, name := range []string{"first-arm-after-cutoff", "replacement-arm-after-cutoff"} {
		t.Run(name, func(t *testing.T) {
			f := newContextTLSLocalReview(t)
			gate := directlan.NewContextFixtureReadGate()
			r := newContextTLSRound(t, f, gate)
			var old *contextExchangeOperation
			if name == "replacement-arm-after-cutoff" {
				old = r.arm(t, directlan.ContextPrepare)
			}
			request := contextTLSRequest(t, f.request(false))
			ctx, cancel := context.WithTimeout(context.Background(), contextTLSFixtureTimeout)
			defer cancel()
			done := make(chan contextTLSExchangeResult, 1)
			go func() {
				reply, err := r.bridge.Exchange(ctx, request)
				done <- contextTLSExchangeResult{reply, err}
			}()
			contextTLSWaitReached(t, r, gate)
			// The gate is the first server Read, after production accept has
			// installed its wire/cutoff, rather than listener delivery alone.
			late := r.arm(t, directlan.ContextPrepare)
			if old != nil && (late == old || !old.attempt.Cancelled()) {
				t.Fatal("rearm failed to replace and cancel the old association")
			}
			writes := f.writes
			gate.Release()
			gate.Release()
			result := contextTLSWaitExchange(t, r, done)
			if result.err == nil || len(result.reply) != 0 || r.seen.calls != 0 || f.writes != writes || contextSavedPair(f.store.state.Metadata.Peers[0]) != nil {
				t.Fatal("a post-cutoff arm admitted already accepted traffic", result.err)
			}
			r.stop(t)
			if len(r.owner.operations) != 0 || !late.attempt.Cancelled() {
				t.Fatal("late arm remained owned after terminal stop")
			}
		})
	}
}

func TestContextCoreTLSPostPublicationStopPreservesDisk(t *testing.T) {
	f := newContextTLSLocalReview(t)
	r := newContextTLSRound(t, f, nil)
	armed := r.arm(t, directlan.ContextPrepare)
	writes := f.writes
	var published bool
	f.store.write = func(path string, data []byte) error {
		f.writes++
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		published = true
		// This hook is after publication admission. It cannot establish the
		// separately deferred exact last-check cancellation interleaving.
		r.owner.requestClose()
		return nil
	}
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if err == nil || len(reply) != 0 || !published || r.seen.calls != 1 || r.seen.completionErr == nil || r.seen.hasResponse || f.writes != writes+1 ||
		contextSavedPair(f.store.state.Metadata.Peers[0]) == nil || f.store.recovery || armed.epoch.Valid() || !r.owner.stopping.Load() {
		t.Fatal("post-publication stop rolled back the saved state or released a success frame", err, r.seen.completionErr)
	}
	contextTLSAssertDisk(t, f)
	contextTLSAssertNoApplication(t, f)
}

func TestContextCoreTLSUncertainPublicationStopsOwnerAfterUnlock(t *testing.T) {
	f := newContextTLSLocalReview(t)
	r := newContextTLSRound(t, f, nil)
	r.arm(t, directlan.ContextPrepare)
	writes := f.writes
	var published, firstCancelAfterUnlock bool
	var cancelCalls int
	originalCancel := r.owner.cancel
	r.owner.cancel = func() {
		cancelCalls++
		if cancelCalls == 1 {
			firstCancelAfterUnlock = f.store.mu.TryLock()
			if firstCancelAfterUnlock {
				f.store.mu.Unlock()
			}
		}
		originalCancel()
	}
	f.store.write = func(path string, data []byte) error {
		f.writes++
		if err := config.AtomicWritePrivate(path, data); err != nil {
			return err
		}
		published = true
		// Leave stopping to the real completion's uncertain-publication path.
		return config.ErrAtomicCommitted
	}
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if err == nil || len(reply) != 0 || !published || r.seen.calls != 1 || !errors.Is(r.seen.completionErr, directlan.ErrRecovery) || r.seen.hasResponse ||
		f.writes != writes+1 || !f.store.recovery || f.store.contextPublication != nil || contextSavedPair(f.store.state.Metadata.Peers[0]) == nil ||
		cancelCalls == 0 || !firstCancelAfterUnlock || !r.owner.stopping.Load() || r.owner.ctx.Err() == nil {
		t.Fatal("uncertain publication lost adoption, supplied a receipt, or closed under the store lock", err, r.seen.completionErr)
	}
	contextTLSAssertDisk(t, f)
	contextTLSAssertNoApplication(t, f)
	r.stop(t)
}

func TestContextCoreTLSResponseRejectsLaterWriteOrStop(t *testing.T) {
	for _, name := range []string{"same-byte-whole-file-write", "terminal-owner-stop"} {
		t.Run(name, func(t *testing.T) {
			f := newContextTLSLocalReview(t)
			r := newContextTLSRound(t, f, nil)
			r.arm(t, directlan.ContextPrepare)
			writes := f.writes
			var hookErr error
			var invalidated, sameDigest bool
			r.after = func(_ context.Context, _ *directlan.ContextAttempt, _ directlan.VerifiedContextExchange, response directlan.ContextResponse, err error) {
				if err != nil {
					return
				}
				if name == "terminal-owner-stop" {
					r.owner.requestClose()
					invalidated = r.owner.stopping.Load()
					return
				}
				f.core.op.Lock()
				f.store.mu.Lock()
				digest := f.store.fileDigest
				hookErr = f.store.writeStateLocked(cloneDirectLANState(f.store.state))
				sameDigest = digest == f.store.fileDigest
				invalidated = !response.Epoch.Valid() && f.store.contextEpoch == nil && f.store.contextPublication == nil
				f.store.mu.Unlock()
				f.core.op.Unlock()
			}
			reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
			wantWrites := writes + 1
			if name == "same-byte-whole-file-write" {
				wantWrites++
				if !sameDigest {
					t.Fatal("same-byte publication changed the private file digest")
				}
			}
			if err == nil || len(reply) != 0 || r.seen.calls != 1 || r.seen.completionErr != nil || !r.seen.hasResponse || !r.seen.currentReceipt ||
				!r.seen.currentResponseEpoch || !invalidated || hookErr != nil || f.writes != wantWrites {
				t.Fatal("unchanged genuine response survived a later invalidation before send admission", err, hookErr, r.seen.completionErr)
			}
			contextTLSAssertDisk(t, f)
			contextTLSAssertNoApplication(t, f)
		})
	}
}

func TestContextCoreTLSBridgeJoinsOneOwnedExchange(t *testing.T) {
	for _, name := range []string{"success", "authenticated-peer-close", "canceled-context", "unreleased-read-gate-deadline", "unreleased-read-gate-close", "close-before-exchange"} {
		t.Run(name, func(t *testing.T) {
			f := newContextTLSLocalReview(t)
			var gate *directlan.ContextFixtureReadGate
			if strings.HasPrefix(name, "unreleased-") || name == "canceled-context" {
				gate = directlan.NewContextFixtureReadGate()
			}
			r := newContextTLSRound(t, f, gate)
			armed := r.arm(t, directlan.ContextPrepare)
			request := contextTLSRequest(t, f.request(false))
			if name == "authenticated-peer-close" {
				request = nil // fixed bridge mode: pinned TLS, then raw peer close
			}
			if name == "close-before-exchange" {
				if err := r.bridge.Close(); err != nil {
					t.Fatal("unused bridge did not join", err)
				}
			}
			var result contextTLSExchangeResult
			if gate == nil {
				result.reply, result.err = r.exchange(t, request)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), contextTLSFixtureTimeout)
				defer cancel()
				done := make(chan contextTLSExchangeResult, 1)
				go func() {
					reply, err := r.bridge.Exchange(ctx, request)
					done <- contextTLSExchangeResult{reply, err}
				}()
				contextTLSWaitReached(t, r, gate)
				switch name {
				case "canceled-context":
					cancel()
				case "unreleased-read-gate-close":
					if err := r.bridge.Close(); err != nil {
						t.Fatal("Close did not wake an unreleased first Read", err)
					}
				}
				result = contextTLSWaitExchange(t, r, done)
			}
			if name == "success" {
				if result.err != nil || len(result.reply) == 0 || r.seen.calls != 1 || r.seen.completionErr != nil || f.writes != 2 {
					t.Fatal("successful one-shot exchange failed", result.err, r.seen.completionErr)
				}
			} else if result.err == nil || len(result.reply) != 0 || r.seen.calls != 0 || f.writes != 1 {
				t.Fatal("negative cleanup case completed an unexpected request", result.err)
			}
			if !armed.attempt.Cancelled() {
				t.Fatal("joined transport retained a live attempt")
			}
			// The bridge's join is the proof of released transport work. No
			// goroutine-count heuristic or timeout is accepted as that proof.
			second, secondErr := r.exchange(t, contextTLSRequest(t, f.request(false)))
			if secondErr == nil || len(second) != 0 {
				t.Fatal("one-shot bridge accepted a second submission", secondErr)
			}
			r.stop(t)
			if len(r.owner.operations) != 0 || !r.owner.stopping.Load() || f.core.contextControl != nil {
				t.Fatal("joined Core owner retained work or current-owner status")
			}
		})
	}
}

func TestContextTLSFixtureExcludedFromProductBuilds(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const fixtureTag = "directlan_context_fixture"
	files := []string{"internal/directlan/context_fixture_bridge.go", "internal/core/direct_lan_context_tls_test.go"}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		line, _, found := bytes.Cut(data, []byte("\n\n"))
		if !found || string(line) != "//go:build "+fixtureTag {
			t.Fatalf("%s lacks the exact dedicated first-line build constraint", name)
		}
		expression, err := constraint.Parse(string(line))
		if err != nil || expression.Eval(func(string) bool { return false }) || !expression.Eval(func(tag string) bool { return tag == fixtureTag }) {
			t.Fatalf("%s has an ineffective fixture build constraint: %v", name, err)
		}
	}
	product := []string{"ts_omit_portmapper", "ts_omit_captiveportal", "ts_omit_useproxy"}
	profiles := []struct {
		name string
		tags []string
	}{
		{"default", nil},
		{"product", product},
		{"directlan-integration", append(append([]string{}, product...), "directlan_integration")},
		{"directlan-lifecycle", append(append([]string{}, product...), "directlan_integration", "directlan_lifecycle")},
		{"lanlink-integration", append(append([]string{}, product...), "lanlink_integration")},
		{"lanlink-without-udp", append(append([]string{}, product...), "lanlink_integration", "ts_omit_udptransport")},
		{"e2e", append(append([]string{}, product...), "soba_e2e")},
	}
	for _, target := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		for _, profile := range profiles {
			t.Run(target.os+"-"+target.arch+"/"+profile.name, func(t *testing.T) {
				selection := build.Default
				selection.GOOS, selection.GOARCH = target.os, target.arch
				selection.CgoEnabled, selection.UseAllFiles = false, false
				selection.BuildTags = append([]string{}, profile.tags...)
				for _, name := range files {
					path := filepath.Join(root, filepath.FromSlash(name))
					matched, err := selection.MatchFile(filepath.Dir(path), filepath.Base(path))
					if err != nil || matched {
						t.Fatalf("fixture selected by ordinary product/native tags: %s: %v", name, err)
					}
				}
				selection.BuildTags = append(selection.BuildTags, fixtureTag)
				for _, name := range files {
					path := filepath.Join(root, filepath.FromSlash(name))
					matched, err := selection.MatchFile(filepath.Dir(path), filepath.Base(path))
					if err != nil || !matched {
						t.Fatalf("fixture is not available under its explicit test tag: %s: %v", name, err)
					}
				}
			})
		}
	}
	allowed := make(map[string]bool, len(files))
	for _, name := range files {
		allowed[name] = true
	}
	// Only the dedicated go-test driver and its command/inventory unit tests
	// may mention the tag. Workflows and product build scripts remain excluded.
	allowed[".github/scripts/ci-context-control.py"] = true
	allowed[".github/scripts/test_ci_context_control.py"] = true
	// This is source selection and reference inspection, not a binary or
	// dependency-graph exclusion claim. Deliberately tagged builds opt in.
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if allowed[relative] {
			return nil
		}
		// Ignore only generated bytecode for the two permitted test scripts.
		if strings.HasPrefix(relative, ".github/scripts/__pycache__/") && strings.HasSuffix(relative, ".pyc") {
			name, _, _ := strings.Cut(filepath.Base(relative), ".")
			if allowed[".github/scripts/"+name+".py"] {
				return nil
			}
		}
		goSource := strings.HasSuffix(relative, ".go")
		buildSource := strings.HasPrefix(relative, ".github/workflows/") || strings.HasPrefix(relative, ".github/scripts/")
		if !goSource && !buildSource {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if buildSource && bytes.Contains(data, []byte(fixtureTag)) {
			t.Errorf("fixture tag appears in ordinary workflow/script source: %s", relative)
		}
		if goSource {
			for _, symbol := range []string{"ContextFixtureBridge", "ContextFixtureReadGate", "ErrContextFixtureCleanup"} {
				if bytes.Contains(data, []byte(symbol)) {
					t.Errorf("fixture symbol referenced outside the tagged test closure: %s", relative)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("source-only fixture boundary inspection failed", err)
	}
}
