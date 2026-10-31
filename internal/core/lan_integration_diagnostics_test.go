//go:build lanlink_integration

package core

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestNativeLANCommandDiagnosticRetainsOnlyAllowedCodes(t *testing.T) {
	const private = "synthetic-private-detail"
	for _, code := range []string{
		"lan_saved_start_changed", "lan_start_failed", "lan_environment_proxy", "lan_environment_override", "lan_relay_mismatch", "network_restart_required", "lan_state_capacity", "lan_recovery_required",
		"lan_listener_conflict", "lan_listener_address_unavailable", "lan_listener_permission_denied", "lan_listener_capacity",
	} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("%s: %w", private, &lanCommandError{code, private})
			if got := nativeLANCommandErrorCode(err); got != code || strings.Contains(got, private) {
				t.Fatal("safe diagnostic classification lost or private detail retained")
			}
		})
	}
	for _, err := range []error{nil, errors.New(private), errors.New("lan_listener_conflict"), &lanCommandError{private, private}, &lanCommandError{"lan_unknown_future_code", private}} {
		if nativeLANCommandErrorCode(err) != "unclassified" {
			t.Fatal("untrusted text or unknown code became a diagnostic")
		}
	}
}

func TestNativeLANCommandDiagnosticPreservesProductionListenClassification(t *testing.T) {
	err := &net.OpError{Op: "listen", Net: "tcp", Addr: &net.TCPAddr{Port: 48443}, Err: fixtureListenerConflict()}
	if got := nativeLANCommandErrorCode(classifyLANRelayStartError(err)); got != "lan_listener_conflict" {
		t.Fatal("production bind-conflict classification was lost")
	}
	if got := nativeLANCommandErrorCode(classifyLANRelayStartError(errors.Join(err, errors.New("synthetic-private-detail")))); got != "lan_start_failed" {
		t.Fatal("ambiguous cause gained an exact listener diagnosis")
	}
}

// This negative control exercises the real host start with an intentionally
// occupied loopback endpoint. It verifies diagnostic fidelity, not the cause
// of an earlier CI failure or recovery by retry.
func TestNativeLANHeldRelayPortDiagnosticIntegration(t *testing.T) {
	f := newNativeLANCoreFixture(t)
	host := f.open()
	address := f.relayAddress()
	held, err := net.Listen("tcp4", address.String())
	if err != nil {
		t.Fatal("could not reserve isolated negative-control endpoint")
	}
	f.resources = append(f.resources, held)
	_, err = f.invoke(host, "network.configure", map[string]any{"mode": "lan", "hostname": "native-host", "lan": LANSelection{Kind: "host", Address: address.String()}})
	if err == nil || nativeLANCommandErrorCode(err) != "lan_listener_conflict" {
		t.Fatalf("occupied loopback endpoint diagnostic mismatch (code=%s errorPresent=%t)", nativeLANCommandErrorCode(err), err != nil)
	}
	if host.nodeCopy() != nil || host.lanStatus()["relayReady"] != false {
		t.Fatal("failed native host retained runtime readiness")
	}
}
