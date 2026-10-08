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

// Source-only next tranche; historical non-resumption denial overlap remains
// disclosed. No clock adjustment, store/ready flag injection, reapproval or
// deadline extension is used. Only FOLLOW consent expires in this case: the
// signed proof, application trust and explicitly selected service live longer.
func TestIntegratedFiniteFollowExpiryDeniesTraffic(t *testing.T) {
	f, a, b := runIntegratedEndpointMoveDelivery(t)
	aKey := a.directLANStoreCopy().copy().Identity.PublicKey()
	bKey := b.directLANStoreCopy().copy().Identity.PublicKey()
	probe := newEndpointTCPProbe(f, a, b)
	stale := probe.exchange(f, b, a, "PRE!")
	terminal, ok := stale.(transportorigin.TerminalConnection)
	if !ok {
		t.Fatal("expiry baseline lacks production terminal attribution")
	}
	old := f.capture(b, aKey)
	b.op.Lock()
	backend := b.endpointBackendLocked()
	origin, originErr := backend.Node.CaptureTransportOrigin()
	store := b.directLANStoreCopy()
	store.mu.Lock()
	before := cloneDirectLANState(store.state)
	authority := before.Metadata.Peers[0].EndpointState
	originalCutoff := backend.currentCompletion().deadline
	store.mu.Unlock()
	b.op.Unlock()
	if originErr != nil || terminal.TransportOrigin() == nil || terminal.TransportOrigin().Identity() != origin.Identity() {
		t.Fatal("expiry baseline selected a different native generation")
	}
	if authority == nil || authority.Follow == nil || authority.Approval == nil || authority.Approval.Kind != "follow" || authority.ReceivedProof == nil {
		t.Fatal("expiry case lacks real followed endpoint authority")
	}
	followKey := directLANEndpointDeadlineKey{authority.PairBinding, "follow", authority.Follow.Revision}
	store.mu.Lock()
	originalFollow, found := store.endpointDeadlines[followKey]
	store.mu.Unlock()
	followExpiry, followErr := time.Parse(time.RFC3339Nano, authority.Follow.Expires)
	proofExpiry, proofErr := time.Parse(time.RFC3339Nano, authority.ReceivedProof.Update.Expires)
	if followErr != nil || proofErr != nil || !followExpiry.Before(proofExpiry) || !time.Now().Before(originalCutoff) || !originalCutoff.UTC().Equal(followExpiry) || !found || originalFollow.abs != authority.Follow.Expires || !originalFollow.monotonic.Equal(originalCutoff) {
		t.Fatal("follow and proof deadlines were not independently established")
	}
	trustA, hasA := a.trust(bKey)
	trustB, hasB := b.trust(aKey)
	if !hasA || !hasB {
		t.Fatal("application consent not established")
	}
	a.mu.RLock()
	var service *activeService
	for _, active := range a.active {
		if active.spec.Name == "synthetic-tcp-boundary" {
			service = active
		}
	}
	appGrantValid := service != nil && service.permissionActiveAt(time.Now()) && service.expires.After(proofExpiry)
	a.mu.RUnlock()
	if !appGrantValid {
		t.Fatal("application service does not outlive both endpoint deadlines")
	}
	// This blocks on the actual production deadline owner, not a fake timer or
	// a stop request supplied by the fixture. Early retirement is a failure.
	if old.WaitPhysicalJoin(f.ctx) != nil || time.Now().Before(originalCutoff) {
		t.Fatal("original finite-follow deadline did not cause actual native retirement")
	}
	if !time.Now().Before(proofExpiry) {
		t.Fatal("proof also expired; isolated follow-expiry evidence unavailable")
	}
	// Do not Close the policy wrapper before the rejection assertion.
	if n, err := stale.Write([]byte("OLD!")); n != 0 || err == nil {
		t.Fatal("expired follow authority accepted stale TCP write")
	}
	if err := probe.closeClient(stale); !endpointTCPClosed(err) {
		t.Fatal("expired TCP wrapper cleanup failed")
	}
	if terminal.WaitClosed(f.ctx) != nil {
		t.Fatal("expired TCP wrapper cleanup did not join")
	}
	probe.requireNoExtra(f)
	denyNew := func() {
		t.Helper()
		if !time.Now().Before(proofExpiry) {
			t.Fatal("proof expired during isolated follow check")
		}
		call, cancel := context.WithTimeout(f.ctx, 3*time.Second)
		conn, err := b.dial(call, aKey, "tcp", 32101)
		cancel()
		if conn != nil {
			probe.retain(conn)
			_ = probe.closeClient(conn)
			t.Fatal("expired follow authority returned a new application connection")
		}
		var timeout net.Error
		if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("new application attempt lacked explicit timely denial")
		}
	}
	denyNew()
	// The issuer's SAME signed proof is still current and is explicitly retried.
	// A delivery outcome alone is never evidence that application authority grew.
	sentBefore := a.directLANStoreCopy().copy().Metadata.Peers[0].EndpointState.IssuedProof
	if sentBefore == nil || *sentBefore != *authority.ReceivedProof {
		t.Fatal("retry proof differs from original received proof")
	}
	sentDigest, _ := sentBefore.Digest()
	senderStore := a.directLANStoreCopy()
	senderStore.mu.Lock()
	sentBound, sentBoundPresent := senderStore.endpointIssuedDeadlines[sentDigest]
	senderStore.mu.Unlock()
	if !sentBoundPresent || sentBound.expired || !time.Now().Before(sentBound.monotonic) {
		t.Fatal("issuer proof bound is not independently live")
	}
	in := directLANEndpointExportInput{PeerID: bKey}
	review := f.command(a, "direct-lan.endpoint.delivery.preview", in).(map[string]any)
	in.ExpectedRevision = review["revision"].(string)
	f.command(a, "direct-lan.endpoint.delivery.apply", in)
	senderStore.mu.Lock()
	afterSentBound := senderStore.endpointIssuedDeadlines[sentDigest]
	senderStore.mu.Unlock()
	if afterSentBound != sentBound {
		t.Fatal("unchanged proof retry renewed issuer cutoff")
	}
	if !time.Now().Before(proofExpiry) {
		t.Fatal("redelivery outlasted proof; isolated follow-expiry evidence unavailable")
	}
	denyNew()
	// Give the already-running production background workers a bounded window
	// to reveal accidental resumption; this is not a universal future-time proof.
	window := time.NewTimer(time.Second)
	select {
	case <-window.C:
	case <-f.ctx.Done():
		window.Stop()
		t.Fatal("no-resumption observation window cancelled")
	}
	probe.mu.Lock()
	accepted := probe.accepted
	probe.mu.Unlock()
	if accepted != 1 {
		t.Fatal("expired authority reached the owned application target again")
	}
	probe.requireNoExtra(f)
	probe.verifyLocalTarget(f)
	store.mu.Lock()
	after := cloneDirectLANState(store.state)
	retained, stillPresent := store.endpointDeadlines[followKey]
	store.mu.Unlock()
	state := after.Metadata.Peers[0].EndpointState
	if state.Follow == nil || state.Follow.Expires != authority.Follow.Expires || state.Follow.Granted != authority.Follow.Granted || state.Follow.Revision != authority.Follow.Revision || state.ReceivedProof == nil || *state.ReceivedProof != *authority.ReceivedProof || !stillPresent || retained.abs != originalFollow.abs || !retained.monotonic.Equal(originalFollow.monotonic) || endpointmeta.SavedEligibility(*after.Metadata, state.PairBinding, time.Now()) {
		t.Fatal("expired follow authority or original bound was renewed")
	}
	againA, okA := a.trust(bKey)
	againB, okB := b.trust(aKey)
	if !okA || !okB || !reflect.DeepEqual(trustA, againA) || !reflect.DeepEqual(trustB, againB) || !time.Now().Before(proofExpiry) {
		t.Fatal("application trust or proof lifetime changed during isolated follow-expiry case")
	}
	a.mu.RLock()
	grantStillValid := service.permissionActiveAt(time.Now())
	a.mu.RUnlock()
	if !grantStillValid {
		t.Fatal("application grant also expired; denial cause is not isolated")
	}
	t.Log("verified real finite-follow expiry with later-valid proof/application grants, physical retirement and bounded non-resumption checks")
}
