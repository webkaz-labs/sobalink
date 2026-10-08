//go:build endpoint_following_acceptance

package core

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// Source-only explicit follow-consent revocation. This removes the follow
// approval and disables future automatic following. It is deliberately NOT
// revoke-current-endpoint-only (which retains follow consent), pair terminal
// removal, finite expiry, or evidence clearing the historical execution denial.
func TestIntegratedDisableFollowDeniesResumption(t *testing.T) {
	f, a, b := runIntegratedEndpointMoveDelivery(t)
	aKey := a.directLANStoreCopy().copy().Identity.PublicKey()
	bKey := b.directLANStoreCopy().copy().Identity.PublicKey()
	probe := newEndpointTCPProbe(f, a, b)
	stale := probe.exchange(f, b, a, "PRE!")
	terminal, ok := stale.(transportorigin.TerminalConnection)
	if !ok {
		t.Fatal("revocation baseline lacks real terminal ownership")
	}
	old := f.capture(b, aKey)
	before := b.directLANStoreCopy().copy()
	original := before.Metadata.Peers[0].EndpointState
	if original == nil || original.Follow == nil || original.Follow.Lifetime != "until-revoked" || original.Follow.Expires != "" || !original.Follow.Active || original.Approval == nil || original.Approval.Kind != "follow" || original.ReceivedProof == nil {
		t.Fatal("baseline is not explicit until-revoked follow authority")
	}
	proof := *original.ReceivedProof
	proofExpiry, err := time.Parse(time.RFC3339Nano, proof.Update.Expires)
	if err != nil || !time.Now().Before(proofExpiry) {
		t.Fatal("signed proof is not independently current")
	}
	trustA, hasA := a.trust(bKey)
	trustB, hasB := b.trust(aKey)
	if !hasA || !hasB {
		t.Fatal("application trust missing before revocation")
	}
	a.mu.RLock()
	var service *activeService
	for _, active := range a.active {
		if active.spec.Name == "synthetic-tcp-boundary" {
			service = active
		}
	}
	appValid := service != nil && service.permissionActiveAt(time.Now()) && service.expires.After(proofExpiry)
	a.mu.RUnlock()
	if !appValid {
		t.Fatal("application grant does not outlive the signed proof")
	}
	serviceCutoff := service.expires
	digest, _ := proof.Digest()
	proofKey := directLANEndpointDeadlineKey{original.PairBinding, "proof", digest}
	store := b.directLANStoreCopy()
	store.mu.Lock()
	originalProofBound, found := store.endpointDeadlines[proofKey]
	store.mu.Unlock()
	if !found || originalProofBound.expired {
		t.Fatal("receiver lacks original valid proof cutoff")
	}
	sender := a.directLANStoreCopy()
	sender.mu.Lock()
	issuedBound, issuedFound := sender.endpointIssuedDeadlines[digest]
	sender.mu.Unlock()
	if !issuedFound || issuedBound.expired {
		t.Fatal("issuer lacks original valid proof cutoff")
	}
	trace := f.observeWrites(b)
	in := directLANEndpointInput{PeerID: aKey, Action: "disable-follow"}
	review := f.command(b, "direct-lan.endpoint.follow.preview", in).(directLANEndpointReview)
	in.ExpectedRevision = review.Revision
	result := f.command(b, "direct-lan.endpoint.follow.apply", in).(map[string]any)
	if result["saved"] != true || result["active"] != true {
		t.Fatal("disable-follow did not durably publish its inactive-peer generation")
	}
	f.until(func() bool { b.mu.RLock(); defer b.mu.RUnlock(); return b.endpointJob == nil }, "disable-follow job did not join")
	trace.mu.Lock()
	tx, fenced, finished, writeFailed := trace.transaction, trace.fence, trace.finished, trace.err
	trace.mu.Unlock()
	if tx == nil || !fenced || !finished || writeFailed {
		t.Fatal("disable-follow lacks actual fence/final writer evidence")
	}
	b.op.Lock()
	store.mu.Lock()
	owner, real := tx.old.(*endpointNodeOwner)
	durable := real && owner.retired != nil && old.MatchesRetirement(owner.retired) && tx.joined && tx.durable && tx.published && tx.receipt == store.contextPublication && store.contextPublicationCurrentLocked(b.lanStartNonce)
	store.mu.Unlock()
	b.op.Unlock()
	if !durable || old.WaitPhysicalJoin(f.ctx) != nil || owner.retired.Wait(f.ctx) != nil {
		t.Fatal("disable-follow lacks exact real old-generation retirement")
	}
	if terminal.TransportOrigin() == nil || terminal.TransportOrigin().Identity() != owner.retired.Origin().Identity() {
		t.Fatal("retired TCP stream had a different generation")
	}
	// No explicit outer wrapper close precedes the stale-write assertion.
	if n, err := stale.Write([]byte("OLD!")); n != 0 || err == nil {
		t.Fatal("disabled follow authority accepted stale TCP data")
	}
	if err := probe.closeClient(stale); !endpointTCPClosed(err) {
		t.Fatal("revoked TCP wrapper close failed")
	}
	if terminal.WaitClosed(f.ctx) != nil {
		t.Fatal("revoked TCP wrapper cleanup did not join")
	}
	probe.requireNoExtra(f)
	assertDisabled := func() {
		t.Helper()
		if !time.Now().Before(proofExpiry) {
			t.Fatal("proof expired; explicit revocation evidence is no longer isolated")
		}
		snapshot := store.copy()
		record := snapshot.Metadata.Peers[0]
		state := record.EndpointState
		expectedFollow := *original.Follow
		expectedFollow.Active = false
		if record.PairRevocation != nil || state == nil || state.Follow == nil || !reflect.DeepEqual(*state.Follow, expectedFollow) || state.Approval != nil || state.ReceiveStatus != "locally_revoked" || state.ReceivedProof == nil || *state.ReceivedProof != proof || endpointmeta.SavedEligibility(*snapshot.Metadata, state.PairBinding, time.Now()) {
			t.Fatal("disabled follow or retained proof history changed unexpectedly")
		}
		store.mu.Lock()
		bound, boundPresent := store.endpointDeadlines[proofKey]
		store.mu.Unlock()
		sender.mu.Lock()
		issued := sender.endpointIssuedDeadlines[digest]
		sender.mu.Unlock()
		if !boundPresent || bound != originalProofBound || issued != issuedBound {
			t.Fatal("revocation or replay renewed an original proof cutoff")
		}
		againA, okA := a.trust(bKey)
		againB, okB := b.trust(aKey)
		if !okA || !okB || !reflect.DeepEqual(trustA, againA) || !reflect.DeepEqual(trustB, againB) {
			t.Fatal("application consent changed during endpoint follow revocation")
		}
		a.mu.RLock()
		valid := service.permissionActiveAt(time.Now()) && service.expires == serviceCutoff
		a.mu.RUnlock()
		if !valid {
			t.Fatal("application service grant also changed or expired")
		}
	}
	denyNew := func() {
		t.Helper()
		assertDisabled()
		call, cancel := context.WithTimeout(f.ctx, 3*time.Second)
		conn, err := b.dial(call, aKey, "tcp", 32101)
		cancel()
		if conn != nil {
			probe.retain(conn)
			_ = probe.closeClient(conn)
			t.Fatal("disabled follow authority returned new application TCP")
		}
		var timeout net.Error
		if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("new application attempt lacked timely explicit denial")
		}
	}
	denyNew()
	delivery := directLANEndpointExportInput{PeerID: bKey}
	deliveryReview := f.command(a, "direct-lan.endpoint.delivery.preview", delivery).(map[string]any)
	delivery.ExpectedRevision = deliveryReview["revision"].(string)
	f.command(a, "direct-lan.endpoint.delivery.apply", delivery)
	// A lost/negative acknowledgement is not itself permission or denial proof.
	// Actual state, original cutoffs, new dial and target observations decide.
	denyNew()
	window := time.NewTimer(time.Second)
	select {
	case <-window.C:
	case <-f.ctx.Done():
		window.Stop()
		t.Fatal("non-resumption observation cancelled")
	}
	probe.mu.Lock()
	accepted := probe.accepted
	probe.mu.Unlock()
	if accepted != 1 {
		t.Fatal("disabled authority reached the application target again")
	}
	probe.requireNoExtra(f)
	// This sanity connection is independent of peer authority and does not
	// require/force EOF on the first remote target stream.
	probe.verifyLocalTarget(f)
	assertDisabled()
	t.Log("verified explicit disable-follow retirement and bounded non-resumption while original proof, application grants and owned target remained valid")
}
