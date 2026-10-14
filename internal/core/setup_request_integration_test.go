//go:build lanlink_integration

package core

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// These cases use the actual scoped HTTP bootstrap and restricted native LAN
// engine in isolated CI. They do not claim physical-device/OS-policy coverage.
func TestLANCoreRequestTOFUIntegration(t *testing.T) { runLANCoreRequestIntegration(t, "tofu") }
func TestLANCoreRequestFullVerificationIntegration(t *testing.T) {
	runLANCoreRequestIntegration(t, "full-sha256")
}

func isolateRequestIntegration(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
}
func requestIntegrationScope() SetupScope {
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic-loopback", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "isolated-request-fixture"}
	scope.ScopeID = setupScopeID(scope)
	return scope
}
func requestIntegrationOpen(f *nativeLANCoreFixture) *Core {
	c := f.open()
	scope := requestIntegrationScope()
	// Install the immutable fixture provider before starting any setup owner.
	// Reusing a live receiver must never rewrite a function its goroutine reads.
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
	return c
}
func requestIntegrationStart(t *testing.T, f *nativeLANCoreFixture, a, b *Core, verification string) (SetupSnapshot, SetupSnapshot) {
	t.Helper()
	scope := requestIntegrationScope()
	av := f.must(a, "setup.start", SetupStartInput{Alias: "Synthetic requester", ScopeID: scope.ScopeID, Consent: true, Mode: "outbound", Verification: verification, ExpiresInSeconds: 1200}).(SetupSnapshot)
	var bv SetupSnapshot
	if b.setupCopy() == nil {
		bootstrap, relay := f.relayAddress().Port(), f.relayAddress().Port()
		for relay == bootstrap {
			relay = f.relayAddress().Port()
		}
		bv = f.must(b, "setup.start", SetupStartInput{Alias: "Synthetic receiver", ScopeID: scope.ScopeID, Consent: true, Mode: "receive", Verification: verification, ExpiresInSeconds: 1200, BootstrapPort: bootstrap, AllowHost: true, RelayPort: relay}).(SetupSnapshot)
	} else {
		bv = b.setupCopy().snapshot()
	}
	av = f.must(a, "setup.inspect-target", SetupInspectTargetInput{SessionID: av.SessionID, Address: bv.Contact.Bootstrap}).(SetupSnapshot)
	if av.Target == nil || av.Target.RelayAddress == "" || av.Target.ScopePrefix != scope.Prefix || av.Target.RelayCertificateSHA256 == "" {
		t.Fatal("preflight did not disclose exact receiver plan")
	}
	av = f.must(a, "setup.request", SetupRequestInput{TargetRevision: 1, SessionID: av.SessionID, TargetSessionID: av.Target.SessionID, ExpectedPeerKey: av.Target.PeerKey, TOFUAcknowledged: verification == "tofu"}).(SetupSnapshot)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		av, bv = a.setupCopy().snapshot(), b.setupCopy().snapshot()
		if av.Plan != nil && bv.Plan != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if av.Plan == nil || bv.Plan == nil || av.Plan.Fingerprint != bv.Plan.Fingerprint || av.RequestID != bv.RequestID || bv.RequestID == "" {
		t.Fatal("actual request transcript did not converge")
	}
	a.setupCopy().mu.Lock()
	noListeners := a.setupCopy().listener == nil && a.setupCopy().server == nil && a.setupCopy().discovery == nil
	a.setupCopy().mu.Unlock()
	if !noListeners || av.Contact != nil || av.Mode != "outbound" {
		t.Fatal("outbound requester created a bootstrap/discovery listener")
	}
	if a.nodeCopy() != nil || b.nodeCopy() != nil || len(a.lanStoreCopy().copy().Trust.Peers) != 0 || len(b.lanStoreCopy().copy().Trust.Peers) != 0 || len(a.profileCopy().Peers) != 0 || len(b.profileCopy().Peers) != 0 {
		t.Fatal("request or key proof activated authority before bilateral approval")
	}
	if bv.LocalApproved || av.RemoteApproved || (verification == "full-sha256" && (av.LocalApproved || bv.RemoteApproved)) {
		t.Fatal("receiver/full-fingerprint consent was inferred")
	}
	return av, bv
}
func runLANCoreRequestIntegration(t *testing.T, verification string) {
	isolateRequestIntegration(t)
	f := newNativeLANCoreFixture(t)
	a, b := requestIntegrationOpen(f), requestIntegrationOpen(f)
	type activationFailure struct {
		stage string
		err   error
	}
	failures := make(chan activationFailure, 4)
	for _, c := range []*Core{a, b} {
		c.lanFactory = func(store *lanStore) (lanNetworkBackend, error) {
			node, e := c.newLANBackend(store)
			if e != nil {
				failures <- activationFailure{"construct", e}
				return node, e
			}
			backend := node.(*lanBackend)
			start := backend.start
			backend.start = func() (hostedLANRelay, error) {
				relay, e := start()
				if e != nil {
					failures <- activationFailure{"start", e}
				}
				return relay, e
			}
			return node, nil
		}
	}
	av, bv := requestIntegrationStart(t, f, a, b, verification)
	approve := func(c *Core, v SetupSnapshot) {
		f.must(c, "setup.approve", SetupApproveInput{SessionID: v.SessionID, RequestID: v.RequestID, Revision: v.Plan.Revision, Fingerprint: v.Plan.Fingerprint, Verified: verification == "full-sha256", TOFUAcknowledged: verification == "tofu"})
	}
	if verification == "full-sha256" {
		approve(a, av)
	}
	if _, e := f.invoke(b, "setup.approve", SetupApproveInput{SessionID: bv.SessionID, RequestID: strings.Repeat("f", 64), Revision: bv.Plan.Revision, Fingerprint: bv.Plan.Fingerprint, Verified: true, TOFUAcknowledged: true}); e == nil {
		t.Fatal("wrong request ID authorized native pairing")
	}
	approve(b, bv)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		av, bv = a.setupCopy().snapshot(), b.setupCopy().snapshot()
		if av.Connected && bv.Connected {
			break
		}
		select {
		case failure := <-failures:
			t.Fatalf("native request activation %s failed (permission=%t canceled=%t timeout=%t)", failure.stage, errors.Is(failure.err, os.ErrPermission), errors.Is(failure.err, context.Canceled), errors.Is(failure.err, context.DeadlineExceeded))
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !av.Connected || !bv.Connected || !av.Paired || !bv.Paired || !av.LocalApproved || !av.RemoteApproved || !bv.LocalApproved || !bv.RemoteApproved {
		t.Fatal("native v2 pair/save/connection did not complete", av.State, av.ReasonCode, bv.State, bv.ReasonCode)
	}
	for _, c := range []*Core{a, b} {
		state := c.lanStoreCopy().copy()
		if len(state.Trust.Peers) != 1 || len(state.SetupTrust) != 1 || state.SetupTrust[0].Verification != verification || len(c.profileCopy().Peers) != 0 {
			t.Fatal("durable verification basis or independent app trust failed")
		}
		loaded, e := readLANStore(c.lanStoreCopy().path)
		if e != nil || len(loaded.copy().Trust.Peers) != 1 || loaded.copy().SetupTrust[0].Verification != verification {
			t.Fatal("native pair/basis did not reload")
		}
	}
	aKey, bKey := a.setupCopy().local.Key, b.setupCopy().local.Key
	for _, pair := range []struct {
		from *Core
		peer string
	}{{a, bKey}, {b, aKey}} {
		f.waitPeerAPI(pair.from, pair.peer)
		if _, e := f.invoke(pair.from, "message.send", map[string]any{"peerId": pair.peer, "text": "Synthetic unapproved"}); e == nil {
			t.Fatal("pairing granted app-message permission")
		}
	}
	// Explicit test-only application approvals establish bidirectional app
	// traffic separately, after proving the pairing flow did not add them.
	f.must(a, "peer.trust", map[string]any{"peerId": bKey, "trusted": true})
	f.must(b, "peer.trust", map[string]any{"peerId": aKey, "trusted": true})
	for _, pair := range []struct {
		from *Core
		peer string
	}{{a, bKey}, {b, aKey}} {
		message := f.must(pair.from, "message.send", map[string]any{"peerId": pair.peer, "text": "Synthetic bidirectional request-mode message"}).(Message)
		if message.Status != "sent" {
			t.Fatal("bidirectional app receipt missing")
		}
	}
	f.must(b, "lan.revoke", map[string]any{"peerId": aKey})
	if _, e := f.invoke(a, "message.send", map[string]any{"peerId": bKey, "text": "Synthetic after revoke"}); e == nil {
		t.Fatal("revoked native pair still accepted new application traffic")
	}
	if !b.lanStoreCopy().copy().SetupTrust[0].Revoked {
		t.Fatal("native revoke lost durable key tombstone")
	}
}

func TestLANCoreRequestRejectCancelIntegration(t *testing.T) {
	isolateRequestIntegration(t)
	f := newNativeLANCoreFixture(t)
	a, b := requestIntegrationOpen(f), requestIntegrationOpen(f)
	av, bv := requestIntegrationStart(t, f, a, b, "tofu")
	oldID := bv.RequestID
	f.must(b, "setup.reject", SetupRejectInput{SessionID: bv.SessionID, RequestID: bv.RequestID})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		av = a.setupCopy().snapshot()
		if av.State == "rejected" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if av.State != "rejected" {
		t.Fatal("actual receiver rejection did not reach requester")
	}
	fresh := requestIntegrationOpen(f)
	fv, bv := requestIntegrationStart(t, f, fresh, b, "tofu")
	if bv.RequestID == oldID || bv.LocalApproved || bv.Plan == nil {
		t.Fatal("fresh request reused rejected approval")
	}
	f.must(fresh, "setup.cancel", SetupSessionInput{SessionID: fv.SessionID})
	bv = b.setupCopy().snapshot()
	if bv.Request != nil || bv.LocalApproved || bv.RemoteApproved || bv.State != "receiving" {
		t.Fatal("cancelled request remained actionable")
	}
	if _, e := f.invoke(b, "setup.approve", SetupApproveInput{SessionID: bv.SessionID, RequestID: oldID, Revision: 1, Fingerprint: av.Plan.Fingerprint, TOFUAcknowledged: true}); e == nil {
		t.Fatal("old request approval revived after rejection and cancellation")
	}
	for _, c := range []*Core{a, b, fresh} {
		if c.nodeCopy() != nil || len(c.lanStoreCopy().copy().Trust.Peers) != 0 || len(c.profileCopy().Peers) != 0 {
			t.Fatal("reject/cancel created saved pairing or application trust")
		}
	}
}
