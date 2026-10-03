package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
)

func diagnosticShare(t *testing.T, c *Core, ports string) string {
	t.Helper()
	return mustCommand(t, c, "service.share", map[string]any{"name": "diagnostic-example", "network": "tcp", "ports": ports, "localPort": 0, "peerIds": []string{"peer-b"}, "ttlSeconds": 600, "discoverable": true}).(map[string]any)["id"].(string)
}
func TestDiagnosticsExplicitTCPAndFailureRetention(t *testing.T) {
	p := newCorePair(t)
	id := diagnosticShare(t, p.a, "8443")
	calls := 0
	failing := true
	p.a.diagnosticsDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		if network != "tcp" || address != "127.0.0.1:8443" {
			t.Fatal("probe escaped approved local target")
		}
		if failing {
			return nil, errors.New("private target and internal failure must not be returned")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	passive := mustCommand(t, p.a, "diagnostics.run", map[string]any{}).(map[string]any)
	if calls != 0 || passive["tcpProbePerformed"] != false {
		t.Fatal("passive doctor opened a connection")
	}
	before := savedService(t, p.a, id)
	result := mustCommand(t, p.a, "diagnostics.run", map[string]any{"serviceId": id, "probeTCP": true}).(ServiceDiagnostic)
	if result.Code != "tcp_unreachable" || result.Application != "unverified" || result.CheckedAt.IsZero() || result.NextSteps["ja"] == "" || result.NextSteps["en"] == "" {
		t.Fatal("failure evidence missing", result)
	}
	failure := p.a.serviceViews()[0]["lastFailure"].(serviceFailure)
	failing = false
	success := mustCommand(t, p.a, "diagnostics.run", map[string]any{"serviceId": id, "probeTCP": true}).(ServiceDiagnostic)
	if success.Code != "tcp_reachable" || success.Application != "unverified" || success.Transport != "reachable" {
		t.Fatal("TCP handshake was called application health", success)
	}
	if got := p.a.serviceViews()[0]["lastFailure"].(serviceFailure); got.Code != failure.Code || !got.At.Equal(failure.At) {
		t.Fatal("successful check erased prior failure")
	}
	after := savedService(t, p.a, id)
	if before.Revision != after.Revision {
		t.Fatal("diagnostic renewed or changed service scope")
	}
	wire, _ := json.Marshal(p.a.permittedServices("peer-b"))
	local, _ := json.Marshal(p.a.serviceViews())
	for _, secret := range []string{"private target", "127.0.0.1", "lastFailure", "nextSteps", "diagnostic"} {
		if strings.Contains(string(wire), secret) {
			t.Fatal("diagnostic information leaked into peer discovery", secret)
		}
	}
	if strings.Contains(string(local), "private target") {
		t.Fatal("raw diagnostic error was returned")
	}
	mustCommand(t, p.a, "service.stop", map[string]string{"id": id})
	if p.a.serviceViews()[0]["lastFailure"].(serviceFailure).Code != "tcp_unreachable" {
		t.Fatal("stop erased diagnostic failure")
	}
	if _, err := command(p.a, randomID(), "diagnostics.run", map[string]any{"serviceId": id, "probeTCP": true}); networkErrorCode(err) != "diagnostic_service_inactive" {
		t.Fatal("stopped scope could be probed", err)
	}
}
func TestDiagnosticsRequiresOneApprovedPort(t *testing.T) {
	p := newCorePair(t)
	id := diagnosticShare(t, p.a, "8443-8445")
	p.a.diagnosticsDial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("invalid check dialed")
		return nil, nil
	}
	for _, input := range []map[string]any{{"serviceId": id}, {"serviceId": id, "probeTCP": true}, {"serviceId": id, "probeTCP": true, "port": 80}, {"serviceId": "missing", "probeTCP": true}, {"serviceId": id, "probeTCP": true, "port": 65536}} {
		if _, err := command(p.a, randomID(), "diagnostics.run", input); err == nil {
			t.Fatal("invalid probe scope accepted", input)
		}
	}
}
func TestDiagnosticsMappedShareUsesOnlyApprovedLoopbackTarget(t *testing.T) {
	p := newCorePair(t)
	id := mustCommand(t, p.a, "service.share", map[string]any{"name": "mapped-diagnostic", "network": "tcp", "ports": "8443", "localPort": 9443, "loopbackHost": "::1", "peerIds": []string{"peer-b"}, "ttlSeconds": 600}).(map[string]any)["id"].(string)
	p.a.diagnosticsDial = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "[::1]:9443" {
			t.Fatal("mapped target changed", address)
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	got := mustCommand(t, p.a, "diagnostics.run", map[string]any{"serviceId": id, "probeTCP": true}).(ServiceDiagnostic)
	if got.Code != "tcp_reachable" || got.Port != 8443 {
		t.Fatal("wrong mapped probe result")
	}
}
