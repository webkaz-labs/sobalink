package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"strings"
	"testing"
	"time"
)

func TestNearbyV2OutboundDryRunNeverOpensAnything(t *testing.T) {
	var out bytes.Buffer
	err := runWith(context.Background(), []string{"--dry-run", "nearby", "start", "--mode", "outbound", "--alias", "Device", "--scope", strings.Repeat("a", 64), "--consent", "--expires-in", "1200"}, &out, nil, nearbyNoCalls(t))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Applied bool `json:"applied"`
		Payload struct {
			Mode      string `json:"mode"`
			Bootstrap int    `json:"bootstrapPort"`
			Relay     int    `json:"relayPort"`
			Host      bool   `json:"allowHost"`
			Seconds   int    `json:"expiresInSeconds"`
		} `json:"payload"`
	}
	if json.Unmarshal(out.Bytes(), &value) != nil || value.Applied || value.Payload.Mode != "outbound" || value.Payload.Bootstrap != 0 || value.Payload.Relay != 0 || value.Payload.Host || value.Payload.Seconds != 1200 {
		t.Fatalf("unsafe preview: %s", out.String())
	}
}
func TestNearbyV2RejectsUnsafeInputWithoutCore(t *testing.T) {
	sid := strings.Repeat("a", 64)
	cases := [][]string{
		{"start", "--mode", "outbound", "--alias", "Device", "--scope", sid, "--consent", "--allow-host"},
		{"start", "--mode", "outbound", "--alias", "Device", "--scope", sid, "--consent", "--bootstrap-port", "48445"},
		{"start", "--mode", "receive", "--alias", "Device", "--scope", sid, "--consent"},
		{"start", "--mode", "receive", "--alias", "Device", "--scope", sid, "--consent", "--allow-host", "--expires-in", "1201"},
		{"inspect-target", "--session", sid, "--address", "http://10.0.0.1:48445/"},
		{"inspect-target", "--session", sid, "--address", "127.0.0.1:48445"},
		{"inspect-target", "--session", sid, "--address", "169.254.169.254:48445"},
		{"inspect-target", "--session", sid, "--address", "10.0.0.1:48445", "--candidate", sid},
		{"request", "--session", sid, "--target-session", sid},
		{"approve", "--session", sid, "--request", sid, "--revision", "1", "--fingerprint", sid},
		{"approve", "--session", sid, "--request", sid, "--revision", "1", "--fingerprint", sid, "--verified", "--acknowledge-first-use"},
	}
	for _, args := range cases {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--dry-run", "nearby"}, args...), &out, nil, nearbyNoCalls(t))
		if err == nil {
			t.Fatalf("accepted unsafe args %q", args)
		}
	}
}
func TestNearbyV2SnapshotRejectsListenerAndModeConfusion(t *testing.T) {
	s := nearbyTestSnapshot("selecting")
	s.Version = 2
	s.Alias = "Device A"
	s.Mode = "outbound"
	s.Verification = "tofu"
	s.Contact = nil
	s.ContactText = ""
	s.Discovery.State = "unavailable"
	s.Target = &nearbyV2Target{Revision: 1, RelayAddress: "192.168.70.11:48443", ScopePrefix: "192.168.70.0/24", RelayCertificateSHA256: strings.Repeat("e", 64), SessionID: strings.Repeat("c", 64), PeerKey: strings.Repeat("d", 64), Alias: "Device B", Address: "192.168.70.11:48445", ExpiresAt: time.Now().Add(time.Minute), Verification: "tofu", KeyStatus: "new"}
	raw, _ := json.Marshal(s)
	if _, err := nearbyDecodeSnapshot(raw, s.SessionID, false); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*nearbySnapshot){func(v *nearbySnapshot) { v.Discovery.State = "active" }, func(v *nearbySnapshot) { v.Target.Verification = "full-sha256" }, func(v *nearbySnapshot) { v.Target.Address = "127.0.0.1:48445" }, func(v *nearbySnapshot) { v.RequestID = "wrong" }} {
		var changed nearbySnapshot
		json.Unmarshal(raw, &changed)
		mutate(&changed)
		data, _ := json.Marshal(changed)
		if _, err := nearbyDecodeSnapshot(data, s.SessionID, false); err == nil {
			t.Fatal("accepted inconsistent v2 snapshot")
		}
	}
}
func TestNearbyV2HelpIsBilingualAndLocal(t *testing.T) {
	for _, locale := range []string{"ja", "en"} {
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"--locale", locale, "nearby", "request", "--help"}, &out, nil, nearbyNoCalls(t)); err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"--peer-key", "--expires-in", "--acknowledge-first-use"} {
			if !strings.Contains(out.String(), word) {
				t.Fatal("missing", word)
			}
		}
	}
}
func TestNearbyV2WizardEOFDoesNotStart(t *testing.T) {
	var out bytes.Buffer
	c := &nearbyCLI{ctx: context.Background(), out: &out, in: strings.NewReader("")}
	p := newPrompts(c.in, c.out)
	if _, err := c.requestWizardStart(p); err == nil {
		t.Fatal("EOF unexpectedly authorized setup")
	}
}

func TestNearbyV2ActionsExposeOnlyCurrentOperations(t *testing.T) {
	s := nearbySnapshot{Mode: "receive", State: "receiving"}
	for _, ja := range []bool{false, true} {
		value := nearbyV2Actions(ja, s)
		for _, forbidden := range []string{"m ", "s ", "a ", "n "} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("irrelevant operation %q in %q", forbidden, value)
			}
		}
		s.Request = &nearbyV2Request{RequestID: strings.Repeat("a", 64)}
		value = nearbyV2Actions(ja, s)
		if !strings.Contains(value, "n ") {
			t.Fatal("missing decline")
		}
		s.Request = nil
	}
}

func TestNearbyV2RecoveryCannotStartReplacement(t *testing.T) {
	for _, state := range []string{"recovery_required", "failed", "needs_restart"} {
		s := nearbyTestSnapshot(state)
		s.Version = 2
		s.Mode = "outbound"
		s.Alias = "Device A"
		s.Verification = "tofu"
		s.Contact = nil
		s.ContactText = ""
		var out bytes.Buffer
		calls := []webui.Command{}
		err := runWith(context.Background(), []string{"nearby", "--interactive"}, &out, strings.NewReader("3\nDevice B\n1\n120\n1\ny\n"), nearbyFixtureClient(t, &calls, nil, s))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].Name != "setup.status" {
			t.Fatalf("replaced uncertain session: %v", calls)
		}
	}
}

func TestNearbyV2ConnectedEOFDoesNotReplaceOrCancel(t *testing.T) {
	s := nearbyTestConnected(false)
	s.Version = 2
	s.Mode = "receive"
	s.Alias = "Device A"
	s.Verification = "tofu"
	s.Contact.Version = 2
	s.ContactText = ""
	s.Plan.Verification = "tofu"
	for i := range s.Plan.Participants {
		s.Plan.Participants[i].Protocol = 2
		s.Plan.Participants[i].Verification = "tofu"
		s.Plan.Participants[i].Mode = "outbound"
		s.Plan.Participants[i].Bootstrap = "192.168.70.11:0"
	}
	s.Plan.Participants[0].Mode = "receive"
	s.Plan.Participants[0].Bootstrap = "192.168.70.10:54546"
	s.RequestID = s.Plan.Participants[1].SessionID
	peer := s.Plan.Participants[1]
	s.Request = &nearbyV2Request{RequestID: s.RequestID, PeerKey: peer.Key, Alias: peer.Alias, Address: peer.Bootstrap, ExpiresAt: s.Plan.ExpiresAt, Verification: "tofu", KeyStatus: "new"}
	var out bytes.Buffer
	calls := []webui.Command{}
	if err := runWith(context.Background(), []string{"nearby", "--interactive"}, &out, strings.NewReader(""), nearbyFixtureClient(t, &calls, nil, s)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("EOF changed established pair: %v", calls)
	}
}

func TestNearbyV2WizardPinsReviewedTargetAndExplicitConsent(t *testing.T) {
	for _, input := range []string{"s\nn\nx\n", "s\ny\nx\n"} {
		s := nearbyTestSnapshot("selecting")
		s.Version = 2
		s.Alias = "Device A"
		s.Mode = "outbound"
		s.Verification = "tofu"
		s.Contact = nil
		s.ContactText = ""
		s.Target = &nearbyV2Target{Revision: 1, SessionID: strings.Repeat("c", 64), PeerKey: strings.Repeat("d", 64), Alias: "Device B", Address: "192.168.70.11:48445", ExpiresAt: s.ExpiresAt, Verification: "tofu", KeyStatus: "new", RelayAddress: "192.168.70.11:48443", ScopePrefix: "192.168.70.0/24", RelayCertificateSHA256: strings.Repeat("e", 64)}
		requests := 0
		client := func(_ context.Context, _ string, raw string, result any) error {
			var command webui.Command
			json.Unmarshal([]byte(raw), &command)
			if command.Name == "setup.request" {
				requests++
				var fields map[string]any
				json.Unmarshal(command.Payload, &fields)
				if fields["sessionId"] != s.SessionID || fields["targetSessionId"] != s.Target.SessionID || fields["expectedPeerKey"] != s.Target.PeerKey || fields["tofuAcknowledged"] != true || fields["targetRevision"] != float64(1) {
					t.Fatal("unbound request", fields)
				}
				s.State = "requesting"
				s.RequestID = s.SessionID
				s.Request = &nearbyV2Request{RequestID: s.SessionID, PeerKey: s.Target.PeerKey, Alias: s.Target.Alias, Address: s.Target.Address, ExpiresAt: s.Target.ExpiresAt, Verification: s.Verification, KeyStatus: "new"}
			} else if command.Name != "setup.status" {
				t.Fatal("unexpected command", command.Name)
			}
			data, _ := json.Marshal(s)
			return json.Unmarshal(data, result)
		}
		var out bytes.Buffer
		if err := runWith(context.Background(), []string{"nearby", "--interactive"}, &out, strings.NewReader(input), client); err != nil {
			t.Fatal(err)
		}
		expected := 0
		if strings.Contains(input, "\ny\n") {
			expected = 1
		}
		if requests != expected {
			t.Fatalf("requests %d want %d", requests, expected)
		}
		for _, visible := range []string{s.Target.RelayAddress, s.Target.ScopePrefix, s.Target.RelayCertificateSHA256, s.Target.PeerKey} {
			if !strings.Contains(out.String(), visible) {
				t.Fatalf("missing consent detail %s", visible)
			}
		}
	}
}
func TestNearbyV2StartMatchRejectsReplacedIdentityOrRole(t *testing.T) {
	s := nearbyTestSnapshot("selecting")
	s.Version = 2
	s.Alias = "Device A"
	s.Mode = "outbound"
	s.Verification = "tofu"
	s.Contact = nil
	payload := map[string]any{"alias": s.Alias, "mode": s.Mode, "verification": s.Verification, "scopeId": s.Scope.ScopeID, "bootstrapPort": 0}
	if !nearbyV2StartMatches(s, payload) {
		t.Fatal("matching start rejected")
	}
	for _, key := range []string{"alias", "mode", "verification", "scopeId"} {
		old := payload[key]
		payload[key] = "changed"
		if nearbyV2StartMatches(s, payload) {
			t.Fatal("accepted replaced", key)
		}
		payload[key] = old
	}
}

func TestNearbyV2ActualSenderRequestPlanIsInspectable(t *testing.T) {
	for _, mode := range []string{"request", "outbound"} {
		s := nearbyTestPlanned()
		s.Version = 2
		s.Mode = mode
		s.Alias = "Device B"
		s.Verification = "tofu"
		s.Contact = nil
		s.ContactText = ""
		s.SessionID = s.Plan.Participants[1].SessionID
		s.RequestID = s.SessionID
		s.Scope.LocalAddress = "192.168.70.11"
		s.Plan.Verification = "tofu"
		for i := range s.Plan.Participants {
			s.Plan.Participants[i].Protocol = 2
			s.Plan.Participants[i].Verification = "tofu"
		}
		s.Plan.Participants[0].Mode = "receive"
		s.Plan.Participants[1].Mode = mode
		if mode == "outbound" {
			s.Plan.Participants[1].Bootstrap = "192.168.70.11:0"
		} else {
			v := nearbyTestContact(s.SessionID, s.Alias, s.Plan.Participants[1].Bootstrap)
			v.Version = 2
			s.Contact = &v
		}
		peer := s.Plan.Participants[0]
		s.Target = &nearbyV2Target{Revision: 2, SessionID: peer.SessionID, PeerKey: peer.Key, Alias: peer.Alias, Address: peer.Bootstrap, ExpiresAt: s.ExpiresAt, Verification: "tofu", KeyStatus: "saved", RelayAddress: s.Plan.Relay.Address, RelayCertificateSHA256: s.Plan.Relay.CertificateSHA256, ScopePrefix: peer.Scope.Prefix}
		s.Request = &nearbyV2Request{RequestID: s.RequestID, PeerKey: peer.Key, Alias: peer.Alias, Address: peer.Bootstrap, ExpiresAt: s.ExpiresAt, Verification: "tofu", KeyStatus: "saved"}
		raw, _ := json.Marshal(s)
		if _, err := nearbyDecodeSnapshot(raw, s.SessionID, false); err != nil {
			t.Fatal(mode, err)
		}
		var out bytes.Buffer
		if err := nearbyWriteV2Human(&out, "/synthetic/profile", false, s); err != nil {
			t.Fatal(err)
		}
		for _, detail := range []string{"Target review revision: 2", "Saved key", s.Plan.Fingerprint, "Effective deadline"} {
			if !strings.Contains(out.String(), detail) {
				t.Fatalf("missing %q", detail)
			}
		}
		s.Request.PeerKey = strings.Repeat("8", 64)
		raw, _ = json.Marshal(s)
		if _, err := nearbyDecodeSnapshot(raw, s.SessionID, false); err == nil {
			t.Fatal("accepted replaced sender peer")
		}
	}
}

func TestNearbyV2PortEditsAreExplicitAndValidated(t *testing.T) {
	for _, tc := range []struct {
		input        string
		host         bool
		wantB, wantR int
	}{
		{"48455\n48453\n", true, 48455, 48453},
		{"54543\n48455\n48455\n48456\n48457\n", true, 48456, 48457},
		{"48455\n", false, 48455, 0},
	} {
		var out bytes.Buffer
		c := &nearbyCLI{out: &out}
		p := newPrompts(strings.NewReader(tc.input), &out)
		b, r, err := c.requestWizardPorts(p, 48445, map[bool]int{true: 48443, false: 0}[tc.host], tc.host)
		if err != nil || b != tc.wantB || r != tc.wantR {
			t.Fatalf("ports %d/%d err%v", b, r, err)
		}
	}
}
