package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func TestNetworkGuidanceClassifiesOnlyReportedEvidence(t *testing.T) {
	tests := []struct{ code, category, action string }{
		{"direct_lan_recovery_required", "recovery", "review_network"},
		{"mixed_recovery_required", "recovery", "review_network"},
		{"direct_lan_address_unavailable", "address", "review_network"},
		{"direct_lan_address_unknown", "unknown", "refresh_state"},
		{"direct_lan_start_failed", "listener", "review_network"},
		{"lan_relay_mismatch", "identity", "review_network"},
		{"lan_certificate_expired", "certificate", "review_network"},
		{"lan_certificate_rotation_required", "certificate", "review_network"},
		{"direct_lan_capacity", "capacity", "review_capacity"},
		{"lan_relay_presence_capacity", "capacity", "review_capacity"},
		{"lan_environment_proxy", "configuration", "review_network"},
		{"lan_policy_relay_outside", "configuration", "review_network"},
		{"future_error_code", "unknown", "review_network"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			g := networkDiagnosticGuidance("running", tt.code, true, "lan")
			if g == nil || g.Code != tt.code || g.Category != tt.category || g.Action != tt.action || len(g.Summary) != 2 || len(g.NextSteps) != 2 {
				t.Fatal("reported code or action changed", g)
			}
			for _, locale := range []string{"en", "ja"} {
				if g.Summary[locale] == "" || g.NextSteps[locale] == "" {
					t.Fatal("missing localized guidance", locale)
				}
			}
		})
	}
	unknown := networkDiagnosticGuidance("error", "", true, "lan")
	if unknown.Code != "network_failure_unclassified" || unknown.Category != "unknown" {
		t.Fatal("uncoded error acquired an invented cause", unknown)
	}
	for _, state := range []string{"running", "ready", "online", "future-state"} {
		if g := networkDiagnosticGuidance(state, "", false, "lan"); g != nil {
			t.Fatal("guidance invented a failure or route result", state, g)
		}
	}
}

func TestNetworkGuidanceSeparatesSetupAuthenticationAndWaiting(t *testing.T) {
	for _, tt := range []struct{ state, mode, code, action string }{
		{"idle", "none", "network_selection_required", "review_network"},
		{"stopped", "direct-lan", "network_stopped", "review_network"},
		{"NeedsLogin", "tailnet", "network_authentication_required", "review_network"},
		{"NeedsMachineAuth", "tailnet", "network_authentication_required", "review_network"},
		{"starting", "mixed", "network_transition_pending", "wait"},
		{"stopping", "lan", "network_transition_pending", "wait"},
		{"unavailable", "lan", "network_state_unconfirmed", "refresh_state"},
	} {
		g := networkDiagnosticGuidance(tt.state, "", false, tt.mode)
		if g == nil || g.Code != tt.code || g.Action != tt.action {
			t.Fatal(tt, g)
		}
	}
}

func TestNetworkGuidanceIsSharedByPassiveDoctorAndSnapshot(t *testing.T) {
	c := openLANTestCore(t)
	c.mu.Lock()
	c.networkState, c.networkErrorCode, c.networkError = "error", "direct_lan_address_unknown", "private diagnostic details"
	c.mu.Unlock()
	before := definitionsRevision(c.profileCopy())
	passive := mustCommand(t, c, "diagnostics.run", map[string]any{}).(map[string]any)
	snapshot, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := passive["networkGuidance"].(*DiagnosticGuidance)
	if passive["tcpProbePerformed"] != false || passive["application"] != "unverified" || !reflect.DeepEqual(g, snapshot["self"].(map[string]any)["guidance"]) {
		t.Fatal("passive surfaces disagree", passive)
	}
	wire, _ := json.Marshal(g)
	if strings.Contains(string(wire), "private diagnostic") || strings.Contains(string(wire), `"path"`) || c.nodeCopy() != nil || definitionsRevision(c.profileCopy()) != before {
		t.Fatal("guidance copied private errors, invented route evidence or changed configuration")
	}
}

func TestOfflineDirectLANPeerPathRemainsUnknown(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	next := s.copy()
	next.Peers = []directlan.Peer{directLANTestPeer(t)}
	if err := s.save(next); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile.Settings.Network = "direct-lan"
	c.mu.Unlock()
	snapshot, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	peers := snapshot["peers"].([]map[string]any)
	if len(peers) != 1 || peers[0]["path"] != "unknown" || peers[0]["verified"] != false || peers[0]["online"] != false {
		t.Fatal("saved direct LAN metadata became observed route evidence", peers)
	}
	if peers[0]["endpoint"] != next.Peers[0].Endpoint.String() {
		t.Fatal("saved endpoint metadata was lost")
	}
}
