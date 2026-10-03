package main

import (
	"github.com/webkaz-labs/sobalink/internal/servicepresets"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestServiceExamplesFollowCatalogAndRemainEditable(t *testing.T) {
	for _, preset := range servicepresets.All() {
		for _, ja := range []bool{false, true} {
			payload, err := servicePayload("connect", []string{"--preset", preset.ID, "--peer", "peer-fixture"}, ja, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if payload["ports"] != strconv.Itoa(preset.Port) || payload["network"] != preset.Network || payload["localPort"] != preset.LocalPort || payload["lifetime"] != "until-stopped" || payload["ttlSeconds"] != 0 {
				t.Fatalf("catalog mismatch: %v", payload)
			}
			payload, err = servicePayload("share", []string{"--preset", preset.ID, "--ports", "9000", "--local-port", "9001", "--network", "udp", "--peers", "peer-fixture"}, ja, io.Discard)
			if err != nil || payload["ports"] != "9000" || payload["network"] != "udp" || payload["localPort"] != 9001 || payload["lifetime"] != "finite" || payload["ttlSeconds"] != 3600 {
				t.Fatalf("override failed: %v %v", payload, err)
			}
			if !strings.Contains(servicePresetHelp(ja), strconv.Itoa(preset.Port)) {
				t.Fatal("actual example port absent from help")
			}
		}
	}
}
func TestServiceLifetimeAndLoopbackValidation(t *testing.T) {
	for _, command := range []string{"connect", "share"} {
		scope := []string{"--ports", "8080", "--peer", "peer-fixture"}
		persistent := "until-stopped"
		if command == "share" {
			scope[2] = "--peers"
			persistent = "until-revoked"
		}
		for _, args := range [][]string{{"--ttl", "72h"}, {"--lifetime", persistent}, {"--loopback-host", "::1", "--local-port", "9000"}} {
			payload, err := servicePayload(command, append(append([]string{}, scope...), args...), false, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if args[0] == "--ttl" && (payload["lifetime"] != "finite" || payload["ttlSeconds"] != 259200) {
				t.Fatal("custom finite lifetime lost")
			}
			if args[0] == "--lifetime" && (payload["lifetime"] != persistent || payload["ttlSeconds"] != 0) {
				t.Fatal("explicit persistent lifetime lost")
			}
		}
		for _, args := range [][]string{{"--ttl", "0s"}, {"--ttl", "1.5s"}, {"--ttl", "-1s"}, {"--lifetime", persistent, "--ttl", "1h"}, {"--lifetime", "forever"}, {"--loopback-host", "localhost"}, {"--loopback-host", "127.0.0.2"}, {"--loopback-host", "0.0.0.0"}} {
			if _, err := servicePayload(command, append(append([]string{}, scope...), args...), false, io.Discard); err == nil {
				t.Fatalf("unsafe input accepted: %v", args)
			}
		}
	}
	for _, args := range [][]string{{"--ports", "8080-8081", "--local-port", "9000"}, {"--ports", "8080", "--local-port", "54543"}} {
		if _, err := servicePayload("share", append(args, "--peers", "peer-fixture"), false, io.Discard); err == nil {
			t.Fatal("invalid mapped share accepted")
		}
	}
}
func TestSavedNoExpiryMappedServiceRoundTrip(t *testing.T) {
	for _, direction := range []string{"share", "forward"} {
		lifetime := "until-revoked"
		if direction == "forward" {
			lifetime = "until-stopped"
		}
		saved := serviceConfiguration{Configuration: savedService{ID: "saved-fixture", Backend: "tailnet", Name: "fixture", Direction: direction, Network: "tcp", Ports: "8080", LocalPort: 9000, LoopbackHost: "::1", Lifetime: lifetime, PeerID: "peer-fixture", PeerIDs: []string{"peer-fixture"}, Purpose: "custom"}, Revision: strings.Repeat("a", 64)}
		for _, op := range []string{"copy", "restart"} {
			m := &mockCLI{saved: saved}
			output, err := runMock(t, m, []string{"--dry-run", "service", op, "saved-fixture"}, "")
			if err != nil {
				t.Fatal(err)
			}
			payload := previewOf(t, output)
			if payload["loopbackHost"] != "::1" || payload["localPort"] != float64(9000) || payload["lifetime"] != lifetime || payload["ttlSeconds"] != float64(0) {
				t.Fatalf("saved fields lost: %v", payload)
			}
			if op == "restart" && payload["expectedRevision"] != saved.Revision {
				t.Fatal("revision lost")
			}
		}
	}
}

func TestServicePreviewDoesNotInventRuntimeCapacity(t *testing.T) {
	var peers []string
	for i := 0; i < 40; i++ {
		peers = append(peers, "peer-fixture-"+strconv.Itoa(i))
	}
	for _, input := range []struct {
		command string
		args    []string
	}{
		{"share", []string{"--ports", "8080", "--peers", strings.Join(peers, ",")}},
		{"connect", []string{"--ports", "8000-8099", "--peer", "peer-fixture"}},
	} {
		if _, err := servicePayload(input.command, input.args, false, io.Discard); err != nil {
			t.Fatalf("runtime budget should be enforced by Core: %v", err)
		}
	}
}
