//go:build directlan_context_fixture

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Real registered attempts and in-memory TLS only. The outbound is captured
// but never sent; this isolates maintenance while that exact association exists.
func newUpgradeRefreshTLSRound(t *testing.T) (*contextTLSRound, *contextExchangeOperation, *contextExchangeOperation, *contextExchangeOperation) {
	t.Helper()
	f := newLocalContextFixture(t)
	f.advance(t, "committed")
	r := newContextTLSRound(t, f, nil)
	operation, sends, opposite := upgradeExchangePlan(f.store.state.Metadata.LocalPeer.Key, f.store.state.Metadata.Peers[0])
	if !sends || opposite == 0 {
		t.Fatal("fixture lacks a bound exchange plan")
	}
	f.core.op.Lock()
	outbound, err := f.core.prepareContextOutboundLocked(context.Background(), r.owner, f.peer, operation)
	f.core.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bound := r.arm(t, opposite)
	prepare := r.arm(t, directlan.ContextPrepare)
	return r, outbound, bound, prepare
}

func TestUpgradeInboundRefreshExistingArmsAvoidMaintenanceCompletionContention(t *testing.T) {
	r, outbound, bound, prepare := newUpgradeRefreshTLSRound(t)
	f := r.f
	policy := f.core.upgradePolicyDigest()
	var marker atomic.Uint64
	r.owner.diagnosticRefresh = &marker
	var observation contextCompletionObservation
	r.completionObservation = &observation
	before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
	var refreshDone chan error
	var blocked, retainedBlocked bool
	var refreshErr, retainedErr error
	var retainedSame bool

	r.before = func(context.Context, *directlan.ContextAttempt) {
		// Pin the actual expensive-store boundary before maintenance starts.
		// A no-op refresh must finish without acquiring this mutex. This is a
		// forbidden-wait guard, not a sleep that guesses when locks overlap.
		f.store.mu.Lock()
		refreshDone = make(chan error, 1)
		go func() { refreshDone <- f.core.refreshUpgradeInbound(context.Background(), r.owner, outbound, policy) }()
		select {
		case refreshErr = <-refreshDone:
			f.store.mu.Unlock()
		case <-time.After(time.Second):
			// Leave the store boundary pinned through the genuine completion.
			// Old maintenance owns Core.op here and rejects that TLS proof.
			blocked = true
		}
	}
	r.after = func(_ context.Context, attempt *directlan.ContextAttempt, _ directlan.VerifiedContextExchange, response directlan.ContextResponse, err error) {
		if blocked {
			f.store.mu.Unlock()
			refreshErr = <-refreshDone
			return
		}
		if err != nil {
			return
		}
		// Its successful completion retains PREPARE until the wire sends the
		// exact response. A maintenance tick must also leave that owner intact.
		f.store.mu.Lock()
		done := make(chan error, 1)
		go func() { done <- f.core.refreshUpgradeInbound(context.Background(), r.owner, outbound, policy) }()
		select {
		case retainedErr = <-done:
			retainedSame = r.owner.operations[attempt] == prepare && prepare.responseEpoch == response.Epoch && r.owner.operations[bound.attempt] == bound
			f.store.mu.Unlock()
		case <-time.After(time.Second):
			retainedBlocked = true
			f.store.mu.Unlock()
			retainedErr = <-done
		}
	}
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if blocked || retainedBlocked || refreshErr != nil || retainedErr != nil || !retainedSame || err != nil || len(reply) == 0 || r.seen.calls != 1 || r.seen.completionErr != nil || observation.Site != "inbound-success" {
		t.Fatalf("inert maintenance obstructed authenticated completion: blocked=%v retainedBlocked=%v refresh=%v retained=%v same=%v wire=%v completion=%v observation=%+v marker=%d", blocked, retainedBlocked, refreshErr, retainedErr, retainedSame, err, r.seen.completionErr, observation, marker.Load())
	}
	if f.writes != writes || f.store.reviewRevision != revision || !reflect.DeepEqual(before, f.store.state) || marker.Load() != 4 {
		t.Fatal("inert refresh/duplicate response changed state, revision or lifetime")
	}
	contextTLSAssertDisk(t, f)
	contextTLSAssertNoApplication(t, f)
}

func TestUpgradeInboundRefreshFastPathRejectsChangedAuthority(t *testing.T) {
	for _, name := range []string{
		"policy", "run-cancellation", "owner-cancellation", "owner-identity", "process-identity",
		"outbound-owner", "outbound-direction", "outbound-association", "outbound-attempt", "outbound-operation",
		"missing-prepare", "missing-opposite", "cancelled-prepare", "cancelled-opposite", "invalid-epoch",
		"prepare-owner", "prepare-attempt", "prepare-direction", "prepare-peer", "invalid-retained-epoch",
	} {
		t.Run(name, func(t *testing.T) {
			r, outbound, bound, prepare := newUpgradeRefreshTLSRound(t)
			f := r.f
			policy := f.core.upgradePolicyDigest()
			run, cancel := context.WithCancel(context.Background())
			defer cancel()
			var want error
			switch name {
			case "policy":
				f.core.profile.Settings.Hostname = "synthetic-policy-change"
				want = endpointmeta.ErrReview
			case "run-cancellation":
				cancel()
				want = context.Canceled
			case "owner-cancellation":
				r.owner.cancel()
				want = directlan.ErrUnavailable
			case "owner-identity":
				f.core.contextControl = nil
				want = directlan.ErrUnavailable
			case "process-identity":
				f.core.lanStartNonce = "synthetic-other-process"
				want = directlan.ErrUnavailable
			case "outbound-owner":
				outbound.owner = nil
			case "outbound-direction":
				outbound.direction = directlan.ContextInbound
			case "outbound-association":
				delete(r.owner.operations, outbound.attempt)
			case "outbound-attempt":
				outbound.attempt = &directlan.ContextAttempt{}
			case "outbound-operation":
				outbound.operation = directlan.ContextPrepare
			case "missing-prepare":
				delete(r.owner.operations, prepare.attempt)
			case "missing-opposite":
				delete(r.owner.operations, bound.attempt)
			case "cancelled-prepare":
				prepare.attempt.Cancel()
			case "cancelled-opposite":
				bound.attempt.Cancel()
			case "invalid-epoch":
				prepare.epoch.Invalidate()
			case "prepare-owner":
				prepare.owner = nil
			case "prepare-attempt":
				prepare.attempt = &directlan.ContextAttempt{}
			case "prepare-direction":
				prepare.direction = directlan.ContextOutbound
			case "prepare-peer":
				prepare.admission.inputs.PeerKey = "synthetic-other-peer"
			case "invalid-retained-epoch":
				prepare.epoch.Invalidate()
				prepare.responseEpoch = directlan.NewContextEpoch()
				prepare.responseEpoch.Invalidate()
			}
			// A missing file is an unmistakable fresh-path rejection. Existing
			// arms must never mask it once their exact inert predicate is false.
			f.store.path = filepath.Join(t.TempDir(), "missing-state.json")
			before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
			err := f.core.refreshUpgradeInbound(run, r.owner, outbound, policy)
			matched := want != nil && errors.Is(err, want) || want == nil && networkErrorCode(err) == "direct_lan_endpoint_review_changed"
			if !matched || f.writes != writes || f.store.reviewRevision != revision || !reflect.DeepEqual(before, f.store.state) {
				t.Fatalf("changed authority bypassed fresh handling: %v", err)
			}
		})
	}
}

func TestUpgradeInboundRefreshFastPathStillChecksFileAtAuthenticatedCompletion(t *testing.T) {
	r, outbound, _, _ := newUpgradeRefreshTLSRound(t)
	f := r.f
	policy := f.core.upgradePolicyDigest()
	before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
	if err := os.WriteFile(f.store.path, []byte("synthetic-changed-protected-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The maintenance fast path makes no admission or store observation.
	if err := f.core.refreshUpgradeInbound(context.Background(), r.owner, outbound, policy); err != nil {
		t.Fatal(err)
	}
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if err == nil || len(reply) != 0 || r.seen.calls != 1 || r.seen.completionErr == nil || r.seen.hasResponse || !f.store.recovery || f.writes != writes || f.store.reviewRevision != revision || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("inert maintenance admitted changed protected bytes", err, r.seen.completionErr)
	}
}

func TestUpgradeInboundRefreshConsumedPrepareGetsFreshArm(t *testing.T) {
	r, outbound, _, prepare := newUpgradeRefreshTLSRound(t)
	f := r.f
	policy := f.core.upgradePolicyDigest()
	before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
	prepare.attempt.Cancel()
	if err := f.core.refreshUpgradeInbound(context.Background(), r.owner, outbound, policy); err != nil {
		t.Fatal(err)
	}
	f.core.op.Lock()
	fresh := upgradeInboundArmedLocked(r.owner, f.peer, directlan.ContextPrepare)
	f.core.op.Unlock()
	if fresh == nil || fresh == prepare || fresh.attempt == prepare.attempt || fresh.owner != r.owner || !fresh.epoch.Valid() {
		t.Fatal("consumed prepare was retained or not freshly captured")
	}
	reply, err := r.exchange(t, contextTLSRequest(t, f.request(false)))
	if err != nil || len(reply) == 0 || r.seen.calls != 1 || r.seen.completionErr != nil || f.writes != writes || f.store.reviewRevision != revision || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("freshly rearmed prepare failed exact duplicate completion", err, r.seen.completionErr)
	}
}

// No proof is constructed or submitted here. This checks only maintenance's
// ownership bookkeeping for an invalid arm epoch and a distinct live response
// epoch, the state produced by successful publication before a reply is sent.
func TestUpgradeInboundRefreshRetainedResponseEpochIsNotRearmed(t *testing.T) {
	r, outbound, _, prepare := newUpgradeRefreshTLSRound(t)
	f := r.f
	policy := f.core.upgradePolicyDigest()
	prepare.responseEpoch = prepare.epoch
	prepare.epoch = directlan.NewContextEpoch()
	prepare.epoch.Invalidate()
	before, writes, revision := cloneDirectLANState(f.store.state), f.writes, f.store.reviewRevision
	f.store.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.core.refreshUpgradeInbound(context.Background(), r.owner, outbound, policy) }()
	select {
	case err := <-done:
		f.store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		f.store.mu.Unlock()
		<-done
		t.Fatal("maintenance entered store work for its exact retained response")
	}
	if r.owner.operations[prepare.attempt] != prepare || prepare.epoch.Valid() || !prepare.responseEpoch.Valid() || prepare.attempt.Cancelled() || f.writes != writes || f.store.reviewRevision != revision || !reflect.DeepEqual(before, f.store.state) {
		t.Fatal("maintenance replaced or revived retained-response ownership")
	}
}
