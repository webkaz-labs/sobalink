//go:build lanlink_integration

package core

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Uses the existing opt-in isolated native fixture and its restricted engine
// build. No mDNS or non-loopback address is used; real-LAN acceptance is separate.
func TestLANCoreNearbySetupManualIntegration(t *testing.T) {
	// The production backend deliberately refuses inherited proxy/engine
	// overrides. Isolate this fixture's environment without changing that guard
	// or any host settings; t.Setenv restores each value after owner cleanup.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	f := newNativeLANCoreFixture(t)
	a, b := f.open(), f.open()
	type activationFailure struct {
		stage string
		err   error
	}
	activationFailures := make(chan activationFailure, 4)
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic-loopback", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "isolated-test"}
	scope.ScopeID = setupScopeID(scope)
	for _, c := range []*Core{a, b} {
		c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
		// Observe existing real factory/start results without replacing the
		// engine, relay, listener or persistence behavior. Never print raw errors.
		c.lanFactory = func(store *lanStore) (lanNetworkBackend, error) {
			n, err := c.newLANBackend(store)
			if err != nil {
				activationFailures <- activationFailure{"construct", err}
				return n, err
			}
			backend := n.(*lanBackend)
			start := backend.start
			backend.start = func() (hostedLANRelay, error) {
				relay, err := start()
				if err != nil {
					activationFailures <- activationFailure{"start", err}
				}
				return relay, err
			}
			return n, nil
		}
	}
	ports := map[uint16]bool{}
	port := func() uint16 {
		for {
			p := f.relayAddress().Port()
			if !ports[p] {
				ports[p] = true
				return p
			}
		}
	}
	av := f.must(a, "setup.start", SetupStartInput{Alias: "Synthetic peer A", ScopeID: scope.ScopeID, BootstrapPort: port(), AllowHost: true, RelayPort: port(), Consent: true}).(SetupSnapshot)
	bv := f.must(b, "setup.start", SetupStartInput{Alias: "Synthetic peer B", ScopeID: scope.ScopeID, BootstrapPort: port(), AllowHost: true, RelayPort: port(), Consent: true}).(SetupSnapshot)
	if av.Discovery.State != "unavailable" || bv.Discovery.State != "unavailable" {
		t.Fatal("loopback fixture unexpectedly started multicast")
	}
	f.must(a, "setup.manual", SetupManualInput{SessionID: av.SessionID, ContactText: bv.ContactText})
	f.must(b, "setup.manual", SetupManualInput{SessionID: bv.SessionID, ContactText: av.ContactText})
	wait := func(condition func() bool, message string) {
		t.Helper()
		until := time.Now().Add(45 * time.Second)
		for time.Now().Before(until) {
			if condition() {
				return
			}
			select {
			case failure := <-activationFailures:
				t.Fatalf("native setup activation %s failed (type=%T permission=%t canceled=%t timeout=%t)", failure.stage, failure.err, errors.Is(failure.err, os.ErrPermission), errors.Is(failure.err, context.Canceled), errors.Is(failure.err, context.DeadlineExceeded))
			default:
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s (A=%s/%s B=%s/%s)", message, av.State, av.ReasonCode, bv.State, bv.ReasonCode)
	}
	view := func(c *Core, id string) SetupSnapshot {
		return f.must(c, "setup.status", SetupSessionInput{SessionID: id}).(SetupSnapshot)
	}
	wait(func() bool {
		av, bv = view(a, av.SessionID), view(b, bv.SessionID)
		return av.Plan != nil && bv.Plan != nil
	}, "exact setup plan did not converge")
	if av.Plan.Fingerprint != bv.Plan.Fingerprint || len(av.Plan.Fingerprint) != 64 {
		t.Fatal("full plan fingerprint differs")
	}
	for _, p := range av.Plan.Participants {
		if p.Scope.ConsentRevision == scope.ScopeID || !setupHex(p.Scope.ConsentRevision) {
			t.Fatal("transcript consent revision exposed the local interface binding")
		}
	}
	for _, c := range []*Core{a, b} {
		if c.nodeCopy() != nil || len(c.lanStoreCopy().copy().Trust.Peers) != 0 || len(c.profileCopy().Peers) != 0 {
			t.Fatal("plan preparation activated a backend or granted pairing/trust")
		}
	}
	f.must(a, "setup.approve", SetupApproveInput{SessionID: av.SessionID, Revision: av.Plan.Revision, Fingerprint: av.Plan.Fingerprint, Verified: true})
	time.Sleep(1200 * time.Millisecond)
	for _, c := range []*Core{a, b} {
		if c.nodeCopy() != nil || len(c.lanStoreCopy().copy().Trust.Peers) != 0 {
			t.Fatal("one-sided approval activated pairing")
		}
	}
	f.must(b, "setup.approve", SetupApproveInput{SessionID: bv.SessionID, Revision: bv.Plan.Revision, Fingerprint: bv.Plan.Fingerprint, Verified: true})
	wait(func() bool {
		av, bv = view(a, av.SessionID), view(b, bv.SessionID)
		return av.Connected && bv.Connected && av.Discovery.State == "stopped" && bv.Discovery.State == "stopped"
	}, "mutual authenticated protocol confirmation did not complete")
	for _, c := range []*Core{a, b} {
		if len(c.lanStoreCopy().copy().Trust.Peers) != 1 || len(c.profileCopy().Peers) != 0 {
			t.Fatal("pairing did not remain separate from application trust")
		}
		if !c.lanStoreCopy().copy().DestinationPolicy.Strict() {
			t.Fatal("strict destination permission was not saved")
		}
	}
	for _, v := range []SetupSnapshot{av, bv} {
		if !v.Paired || v.Trusted || !v.LocalApproved || !v.RemoteApproved || v.Discovery.State != "stopped" {
			t.Fatal("connected result lacks required independent evidence")
		}
		if strings.Contains(v.ContactText, "invitation") {
			t.Fatal("public contact contains pairing material")
		}
	}
	for _, pair := range []struct {
		c *Core
		v SetupSnapshot
	}{{a, av}, {b, bv}} {
		before := pair.c.nodeCopy()
		cancelled := f.must(pair.c, "setup.cancel", SetupSessionInput{SessionID: pair.v.SessionID}).(SetupSnapshot)
		if cancelled.State != "cancelling" {
			t.Fatal("cancel claimed physical closure synchronously")
		}
		wait(func() bool {
			v := view(pair.c, pair.v.SessionID)
			return v.State == "connected" && v.NextAction == "start_new_setup" && v.Process.State == "ready"
		}, "promoted setup cleanup did not join")
		if pair.c.nodeCopy() != before {
			t.Fatal("temporary cleanup closed promoted backend")
		}
		conn, e := net.DialTimeout("tcp", pair.v.Contact.Bootstrap, 100*time.Millisecond)
		if e == nil {
			_ = conn.Close()
			t.Fatal("cancel left bootstrap listener open")
		}
	}
}
