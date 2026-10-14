package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/webui"
)

func nearbyTestScope() nearbyScope {
	return nearbyScope{strings.Repeat("a1", 32), 7, "synthetic0", "192.168.70.10", "192.168.70.0/24"}
}
func nearbyTestContact(id, alias, address string) nearbyContact {
	return nearbyContact{"sobalink-setup-contact", 1, id, alias, address, time.Now().UTC().Add(time.Minute).Truncate(time.Second)}
}
func nearbyTestEncode(c nearbyContact) string {
	raw, _ := json.Marshal(c)
	return nearbyContactPrefix + base64.RawURLEncoding.EncodeToString(raw)
}
func nearbyTestSnapshot(state string) nearbySnapshot {
	s := nearbySnapshot{Kind: "sobalink-setup", Version: 1, State: state, Candidates: []nearbyCandidate{}}
	s.Process.State = "ready"
	s.Process.ProfilePreserved = true
	s.Discovery.State = "stopped"
	if state == "idle" || state == "stop_required" {
		if state == "stop_required" {
			s.Process.State = "stop_required"
		}
		return s
	}
	scope := nearbyTestScope()
	s.Scope = &scope
	s.SessionID = strings.Repeat("b2", 32)
	s.ExpiresAt = time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	contact := nearbyTestContact(s.SessionID, "Device A", "192.168.70.10:54546")
	s.Contact = &contact
	s.ContactText = nearbyTestEncode(contact)
	return s
}
func nearbyTestPlanned() nearbySnapshot {
	s := nearbyTestSnapshot("awaiting_approval")
	p := &nearbyPlan{Revision: 1, Fingerprint: strings.Repeat("c3", 32), Verification: "full-sha256", Backend: "lan", HostKey: strings.Repeat("d4", 32), ExpiresAt: s.ExpiresAt}
	p.VerificationText = nearbyVerifyPrefix + p.Fingerprint
	p.Relay.Kind = "host"
	p.Relay.Address = "192.168.70.10:54547"
	p.Relay.CertificateSHA256 = strings.Repeat("e5", 32)
	p.Participants[0] = nearbyParticipant{SessionID: s.SessionID, Key: p.HostKey, Alias: "Device A", Bootstrap: "192.168.70.10:54546", AllowHost: true, Expires: s.ExpiresAt}
	p.Participants[0].Scope.InterfaceIndex = 7
	p.Participants[0].Scope.LocalAddress = "192.168.70.10"
	p.Participants[0].Scope.Prefix = "192.168.70.0/24"
	p.Participants[1] = nearbyParticipant{SessionID: strings.Repeat("f6", 32), Key: strings.Repeat("a7", 32), Alias: "Device B", Bootstrap: "192.168.70.11:54546", Expires: s.ExpiresAt}
	p.Participants[1].Scope.InterfaceIndex = 8
	p.Participants[1].Scope.LocalAddress = "192.168.70.11"
	p.Participants[1].Scope.Prefix = "192.168.70.0/24"
	s.Plan = p
	return s
}
func nearbyFixtureClient(t *testing.T, calls *[]webui.Command, scopeList []nearbyScope, s nearbySnapshot) controlCaller {
	t.Helper()
	return func(_ context.Context, _ string, raw string, target any) error {
		var cmd webui.Command
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			t.Fatal(err)
		}
		*calls = append(*calls, cmd)
		var value any = s
		if cmd.Name == "setup.scopes" {
			value = nearbyScopes{"sobalink-setup-scopes", 1, scopeList}
		}
		b, _ := json.Marshal(value)
		return json.Unmarshal(b, target)
	}
}
func nearbyNoCalls(t *testing.T) controlCaller {
	t.Helper()
	return func(context.Context, string, string, any) error {
		t.Error("local-only operation called Core")
		return errors.New("unexpected call")
	}
}
func nearbyErrorCode(t *testing.T, err error) string {
	t.Helper()
	var c interface{ ErrorCode() string }
	if err == nil || !errors.As(err, &c) {
		t.Fatalf("missing coded error: %v", err)
	}
	return c.ErrorCode()
}
func TestNearbyHelpHasNoCoreProfileOrNetworkAccess(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, locale := range []string{"en", "ja"} {
		for _, args := range [][]string{{"nearby"}, {"nearby", "--help"}, {"nearby", "start", "--help"}, {"help", "nearby"}} {
			var out bytes.Buffer
			if err := runWith(context.Background(), append([]string{"--locale", locale}, args...), &out, nil, nearbyNoCalls(t)); err != nil {
				t.Fatal(err)
			}
			for _, term := range []string{"--consent", "--verified", "--dry-run", "--scope", "nearby cancel"} {
				if !strings.Contains(out.String(), term) {
					t.Fatalf("missing %s", term)
				}
			}
		}
	}
}
func TestNearbyDryRunNeverCallsCoreAndPreservesAbsentProfile(t *testing.T) {
	s := nearbyTestPlanned()
	contact := nearbyTestEncode(nearbyTestContact(strings.Repeat("f6", 32), "Device B", "192.168.70.11:54546"))
	cases := []struct {
		args    []string
		input   string
		command string
	}{
		{[]string{"scopes"}, "", "setup.scopes"},
		{[]string{"start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID, "--consent", "--allow-host"}, "", "setup.start"},
		{[]string{"status", "--session", s.SessionID}, "", "setup.status"},
		{[]string{"select", "--session", s.SessionID, "--candidate", strings.Repeat("a8", 32)}, "", "setup.select"},
		{[]string{"manual", "--session", s.SessionID, "--stdin"}, contact, "setup.manual"},
		{[]string{"approve", "--session", s.SessionID, "--revision", "1", "--fingerprint", s.Plan.Fingerprint, "--verified"}, "", "setup.approve"},
		{[]string{"cancel", "--session", s.SessionID}, "", "setup.cancel"},
		{[]string{"contact", "--session", s.SessionID, "--output", "unused-contact-file"}, "", "setup.status"},
	}
	for _, tc := range cases {
		var previous string
		for _, locale := range []string{"en", "ja"} {
			dir := filepath.Join(t.TempDir(), "absent-profile")
			args := append([]string{"--state-dir", dir, "--locale", locale, "--dry-run", "nearby"}, tc.args...)
			var out bytes.Buffer
			if err := runWith(context.Background(), args, &out, strings.NewReader(tc.input), nearbyNoCalls(t)); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			var preview struct {
				Applied    bool   `json:"applied"`
				Command    string `json:"command"`
				Validation string `json:"validation"`
			}
			if json.Unmarshal(out.Bytes(), &preview) != nil || preview.Applied || preview.Command != tc.command || preview.Validation != "local-input-only" {
				t.Fatal(out.String())
			}
			if previous != "" && previous != out.String() {
				t.Fatal("localized dry-run JSON")
			}
			previous = out.String()
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("dry-run materialized profile")
			}
			if tc.command == "setup.manual" && strings.Contains(out.String(), contact) {
				t.Fatal("preview exposed contact")
			}
		}
	}
}
func TestNearbyRequiresConsentExactScopeAndSeparateVerification(t *testing.T) {
	id := strings.Repeat("b2", 32)
	cases := []struct {
		args []string
		code string
	}{
		{[]string{"start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID}, "setup_consent_required"},
		{[]string{"start", "--alias", "Device A", "--consent"}, "setup_scope_required"},
		{[]string{"approve", "--session", id, "--revision", "1", "--fingerprint", strings.Repeat("c3", 32)}, "setup_verification_required"},
		{[]string{"approve", "--session", id, "--revision", "0", "--fingerprint", strings.Repeat("c3", 32), "--verified"}, "setup_invalid"},
		{[]string{"start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID, "--consent", "--bootstrap-port", "54544"}, "setup_invalid"},
		{[]string{"start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID, "--consent", "--bootstrap-port", "54547", "--allow-host"}, "setup_invalid"},
		{[]string{"start", "--alias", "private\x1b[2J", "--scope", nearbyTestScope().ScopeID, "--consent"}, "setup_invalid"},
		{[]string{"select", "--session", id, "--candidate", "unknown"}, "setup_invalid"},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--dry-run", "nearby"}, tc.args...), &out, nil, nearbyNoCalls(t))
		if nearbyErrorCode(t, err) != tc.code {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("error echoed input")
		}
	}
}
func TestNearbyCommandsUseExactIPCAndLocaleIndependentJSON(t *testing.T) {
	s := nearbyTestPlanned()
	scope := nearbyTestScope()
	candidate := strings.Repeat("a8", 32)
	contact := nearbyTestEncode(nearbyTestContact(strings.Repeat("f6", 32), "Device B", "192.168.70.11:54546"))
	cases := []struct {
		args  []string
		name  string
		input string
	}{
		{[]string{"scopes"}, "setup.scopes", ""},
		{[]string{"start", "--alias", "Device A", "--scope", scope.ScopeID, "--consent", "--allow-host"}, "setup.start", ""},
		{[]string{"status", "--session", s.SessionID}, "setup.status", ""},
		{[]string{"select", "--session", s.SessionID, "--candidate", candidate}, "setup.select", ""},
		{[]string{"manual", "--session", s.SessionID, "--stdin"}, "setup.manual", contact},
		{[]string{"approve", "--session", s.SessionID, "--revision", "1", "--stdin", "--verified"}, "setup.approve", s.Plan.VerificationText},
		{[]string{"cancel", "--session", s.SessionID}, "setup.cancel", ""},
	}
	for _, tc := range cases {
		var priorOutput string
		var priorPayload json.RawMessage
		for _, locale := range []string{"en", "ja"} {
			calls := []webui.Command{}
			args := append([]string{"--locale", locale, "--state-dir", "/synthetic/profile", "nearby"}, tc.args...)
			args = append(args, "--json")
			var out bytes.Buffer
			base := nearbyFixtureClient(t, &calls, []nearbyScope{scope}, s)
			client := func(ctx context.Context, dir, raw string, target any) error {
				if dir != "/synthetic/profile" {
					t.Fatal("profile lost")
				}
				return base(ctx, dir, raw, target)
			}
			if err := runWith(context.Background(), args, &out, strings.NewReader(tc.input), client); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			n := 1
			if tc.name == "setup.start" {
				n = 2
			}
			if len(calls) != n || calls[n-1].Name != tc.name {
				t.Fatal(calls)
			}
			if priorOutput != "" && (priorOutput != out.String() || !bytes.Equal(priorPayload, calls[n-1].Payload)) {
				t.Fatal("JSON or payload changed with locale")
			}
			priorOutput = out.String()
			priorPayload = calls[n-1].Payload
			if !strings.HasPrefix(calls[n-1].RequestID, "cli-nearby-") {
				t.Fatal("missing request ID")
			}
			if tc.name == "setup.start" {
				var input nearbyStartInput
				json.Unmarshal(calls[n-1].Payload, &input)
				if !input.AllowHost || !input.Consent || input.BootstrapPort != 54546 || input.RelayPort != 54547 || input.ScopeID != scope.ScopeID {
					t.Fatal(input)
				}
			}
		}
	}
}
func TestNearbyMachineRequiresScopeEvenWhenUnambiguous(t *testing.T) {
	calls := []webui.Command{}
	client := nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, nearbyTestSnapshot("discovering"))
	err := runWith(context.Background(), []string{"nearby", "start", "--alias", "Device A", "--consent", "--json"}, io.Discard, nil, client)
	if nearbyErrorCode(t, err) != "setup_scope_required" || len(calls) != 1 || calls[0].Name != "setup.scopes" {
		t.Fatal(err, calls)
	}
}
func TestNearbyLostResponseNeverRepeatsStartAndPreservesProfile(t *testing.T) {
	calls := []webui.Command{}
	base := nearbyFixtureClient(t, &calls, []nearbyScope{nearbyTestScope()}, nearbyTestSnapshot("discovering"))
	client := func(ctx context.Context, dir, raw string, target any) error {
		var cmd webui.Command
		json.Unmarshal([]byte(raw), &cmd)
		if cmd.Name == "setup.start" {
			calls = append(calls, cmd)
			return errors.New("private profile secret")
		}
		return base(ctx, dir, raw, target)
	}
	var out bytes.Buffer
	err := runWith(context.Background(), []string{"--state-dir", "/synthetic/profile", "nearby", "start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID, "--consent"}, &out, nil, client)
	if nearbyErrorCode(t, err) != "setup_control_unavailable" || len(calls) != 2 {
		t.Fatal(err, calls)
	}
	if strings.Contains(err.Error(), "private profile secret") || !strings.Contains(out.String(), "soba --state-dir /synthetic/profile nearby status") {
		t.Fatal(err, out.String())
	}
}
func TestNearbyMachinePreservesAdditiveResponseFields(t *testing.T) {
	raw, _ := json.Marshal(nearbyTestSnapshot("discovering"))
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	fields["futureObservation"] = "unknown"
	raw, _ = json.Marshal(fields)
	client := func(_ context.Context, _ string, _ string, target any) error { return json.Unmarshal(raw, target) }
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"nearby", "status", "--json"}, &out, nil, client); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(out.Bytes(), &got) != nil || !reflect.DeepEqual(fields, got) {
		t.Fatal(out.String())
	}
}
func TestNearbyHumanStatesAndRestartKeepSameProfile(t *testing.T) {
	states := []string{"idle", "discovering", "selecting", "authenticating", "awaiting_approval", "preparing", "connecting", "connected", "recovery_required", "cancelled", "expired", "stop_required", "failed"}
	for _, ja := range []bool{false, true} {
		for _, state := range states {
			s := nearbyTestSnapshot(state)
			var out bytes.Buffer
			if err := nearbyWriteHuman(&out, "/synthetic/profile", ja, s); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), nearbyStateLabel(ja, state)) || !strings.Contains(out.String(), "--state-dir /synthetic/profile nearby status") {
				t.Fatal(out.String())
			}
			if state == "stop_required" && (!strings.Contains(out.String(), "--state-dir /synthetic/profile stop") || !strings.Contains(out.String(), "--state-dir /synthetic/profile start --offline")) {
				t.Fatal(out.String())
			}
		}
	}
	s := nearbyTestSnapshot("connecting")
	s.Process.State = "needs_restart"
	var out bytes.Buffer
	nearbyWriteHuman(&out, "/synthetic/profile", false, s)
	if !strings.Contains(out.String(), "soba --state-dir /synthetic/profile start\n") || !strings.Contains(out.String(), "TCP sessions do not migrate") {
		t.Fatal(out.String())
	}
}
func TestNearbyJSONErrorsStableAndSafe(t *testing.T) {
	var previous string
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"--locale", locale, "--json-errors", "nearby", "approve", "--session", strings.Repeat("b2", 32), "--revision", "1", "--fingerprint", "private-marker", "--verified", "--json"}, io.Discard, nil, nearbyNoCalls(t))
		writeCommandError(&out, err)
		if previous != "" && previous != out.String() {
			t.Fatal("localized machine error")
		}
		previous = out.String()
		if !strings.Contains(out.String(), `"code":"setup_invalid"`) || strings.Contains(out.String(), "private-marker") {
			t.Fatal(out.String())
		}
	}
}
func TestNearbyContactInputStrictBoundedAndUntrusted(t *testing.T) {
	now := time.Now().UTC()
	contact := nearbyTestContact(strings.Repeat("f6", 32), "Device B", "192.168.70.11:54546")
	valid := nearbyTestEncode(contact)
	if got, err := nearbyInspectContact(valid, now); err != nil || got.SessionID != contact.SessionID {
		t.Fatal(err)
	}
	cases := []string{valid + "x", strings.Repeat("x", nearbyMaxInput+1), "soba-card1.any", nearbyContactPrefix + "bad", nearbyVerifyPrefix + strings.Repeat("c3", 32)}
	for _, change := range []func(*nearbyContact){func(c *nearbyContact) { c.ExpiresAt = now.Add(-time.Second) }, func(c *nearbyContact) { c.Bootstrap = "example.invalid:54546" }, func(c *nearbyContact) { c.Bootstrap = "192.168.70.11:54544" }, func(c *nearbyContact) { c.Alias = "x\u202ey" }, func(c *nearbyContact) { c.Alias = "x\u2028y" }, func(c *nearbyContact) { c.SessionID = strings.Repeat("0", 64) }} {
		copy := contact
		change(&copy)
		cases = append(cases, nearbyTestEncode(copy))
	}
	for _, input := range cases {
		if _, err := nearbyInspectContact(input, now); err == nil {
			t.Fatal("accepted malformed contact")
		}
	}
	raw, _ := json.Marshal(contact)
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	if _, err := nearbyInspectContact(nearbyContactPrefix+base64.RawURLEncoding.EncodeToString(raw), now); err == nil {
		t.Fatal("unknown contact fields accepted")
	}
}
func TestNearbyContactFileExclusiveAndDryRunDoesNotWrite(t *testing.T) {
	s := nearbyTestSnapshot("discovering")
	path := filepath.Join(t.TempDir(), "contact.txt")
	calls := []webui.Command{}
	client := nearbyFixtureClient(t, &calls, []nearbyScope{}, s)
	args := []string{"nearby", "contact", "--session", s.SessionID, "--output", path, "--json"}
	if err := runWith(context.Background(), append([]string{"--dry-run"}, args...), io.Discard, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run wrote file")
	}
	if err := runWith(context.Background(), args, io.Discard, nil, client); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != s.ContactText+"\n" {
		t.Fatal("wrong exported contact")
	}
	if err := runWith(context.Background(), args, io.Discard, nil, client); nearbyErrorCode(t, err) != "setup_file_exists" {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, contents) {
		t.Fatal("replaced existing contact")
	}
}
func TestNearbyWizardMatchingFingerprintNeedsSeparateConsent(t *testing.T) {
	for _, input := range []string{strings.Repeat("a8", 32) + "\n", nearbyVerifyPrefix + strings.Repeat("c3", 32) + "\nn\n"} {
		s := nearbyTestPlanned()
		var out bytes.Buffer
		c := nearbyCLI{ctx: context.Background(), out: &out, client: nearbyNoCalls(t)}
		p := newPrompts(strings.NewReader(input), &out)
		got, err := c.wizardApprove(p, s)
		if err != nil || got.LocalApproved {
			t.Fatal(err, got)
		}
	}
}
func TestNearbyWizardApprovalRechecksRevisionWithoutSilentlyRenewing(t *testing.T) {
	for _, stale := range []bool{false, true} {
		s := nearbyTestPlanned()
		latest := nearbyTestPlanned()
		if stale {
			latest.Plan.Revision++
		}
		calls := []webui.Command{}
		client := nearbyFixtureClient(t, &calls, nil, latest)
		var out bytes.Buffer
		c := nearbyCLI{ctx: context.Background(), dir: "/synthetic/profile", out: &out, client: client}
		p := newPrompts(strings.NewReader(s.Plan.Fingerprint+"\ny\n"), &out)
		_, err := c.wizardApprove(p, s)
		if stale {
			if nearbyErrorCode(t, err) != "setup_stale" || len(calls) != 1 {
				t.Fatal(err, calls)
			}
		} else if err != nil || len(calls) != 2 || calls[1].Name != "setup.approve" {
			t.Fatal(err, calls)
		}
	}
}
func TestNearbyWizardScopeChoiceReviewBackAndCancel(t *testing.T) {
	scopes := []nearbyScope{nearbyTestScope()}
	second := nearbyTestScope()
	second.ScopeID = strings.Repeat("f6", 32)
	second.InterfaceIndex = 8
	second.Interface = "synthetic1"
	second.LocalAddress = "192.168.71.10"
	second.Prefix = "192.168.71.0/24"
	scopes = append(scopes, second)
	calls := []webui.Command{}
	var out bytes.Buffer
	c := nearbyCLI{ctx: context.Background(), out: &out, client: nearbyFixtureClient(t, &calls, scopes, nearbyTestSnapshot("idle"))}
	p := newPrompts(strings.NewReader("Device A\n2\ny\nb\nDevice A edited\n1\nn\ny\n"), &out)
	got, err := c.wizardStart(p)
	if err != nil || got.Alias != "Device A edited" || got.ScopeID != scopes[0].ScopeID || got.AllowHost || !got.Consent || len(calls) != 1 {
		t.Fatal(err, got, calls)
	}
	if !strings.Contains(out.String(), "192.168.71.10:54546") || !strings.Contains(out.String(), "192.168.70.10:54546") {
		t.Fatal("review omitted exact endpoints")
	}
}
func TestNearbyWizardReusesExistingSessionWithoutStartingAnother(t *testing.T) {
	s := nearbyTestSnapshot("discovering")
	calls := []webui.Command{}
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"nearby", "--legacy-interactive"}, &out, strings.NewReader("x\n"), nearbyFixtureClient(t, &calls, []nearbyScope{}, s)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Name != "setup.status" {
		t.Fatal(calls)
	}
}
func TestNearbyDecodeRejectsInconsistentApprovalOrWrongSession(t *testing.T) {
	s := nearbyTestPlanned()
	for _, modify := range []func(*nearbySnapshot){func(v *nearbySnapshot) { v.Connected = true }, func(v *nearbySnapshot) { v.Process.ApprovalsSurviveRestart = true }, func(v *nearbySnapshot) { v.Process.ProfilePreserved = false }, func(v *nearbySnapshot) { v.Plan.Fingerprint = "short" }, func(v *nearbySnapshot) { v.Plan.VerificationText = "short" }, func(v *nearbySnapshot) { v.Candidates = nil }} {
		raw, _ := json.Marshal(s)
		var changed nearbySnapshot
		json.Unmarshal(raw, &changed)
		modify(&changed)
		raw, _ = json.Marshal(changed)
		if _, err := nearbyDecodeSnapshot(raw, s.SessionID, false); err == nil {
			t.Fatal("accepted contradictory snapshot")
		}
	}
	raw, _ := json.Marshal(s)
	if _, err := nearbyDecodeSnapshot(raw, strings.Repeat("f6", 32), false); err == nil {
		t.Fatal("accepted different original session")
	}
}

func TestNearbyNonHostStartHasNoRelayAuthority(t *testing.T) {
	var out bytes.Buffer
	args := []string{"--dry-run", "nearby", "start", "--alias", "Device A", "--scope", nearbyTestScope().ScopeID, "--consent"}
	if err := runWith(context.Background(), args, &out, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	var preview struct {
		Payload nearbyStartInput `json:"payload"`
	}
	if json.Unmarshal(out.Bytes(), &preview) != nil || preview.Payload.AllowHost || preview.Payload.RelayPort != 0 {
		t.Fatal(out.String())
	}
	err := runWith(context.Background(), append(args, "--relay-port", "54547"), io.Discard, nil, nearbyNoCalls(t))
	if nearbyErrorCode(t, err) != "setup_invalid" {
		t.Fatal(err)
	}
}
func TestNearbyVerificationFileRoundTripIsNotApproval(t *testing.T) {
	s := nearbyTestPlanned()
	path := filepath.Join(t.TempDir(), "verification.txt")
	calls := []webui.Command{}
	client := nearbyFixtureClient(t, &calls, []nearbyScope{}, s)
	args := []string{"nearby", "verification", "--session", s.SessionID, "--output", path, "--json"}
	if err := runWith(context.Background(), append([]string{"--dry-run"}, args...), io.Discard, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run wrote a verification file")
	}
	var outputs []string
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		actual := args
		if locale == "ja" {
			actual = []string{"nearby", "verification", "--session", s.SessionID, "--json"}
		}
		if err := runWith(context.Background(), append([]string{"--locale", locale}, actual...), &out, nil, client); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if json.Unmarshal(out.Bytes(), &result) != nil || result["verificationText"] != s.Plan.VerificationText || result["revision"] != float64(1) {
			t.Fatal(out.String())
		}
		delete(result, "written")
		encoded, _ := json.Marshal(result)
		outputs = append(outputs, string(encoded))
	}
	if outputs[0] != outputs[1] {
		t.Fatal("localized verification export")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != s.Plan.VerificationText+"\n" {
		t.Fatal(err, string(content))
	}
	if len(calls) != 2 || calls[0].Name != "setup.status" || calls[1].Name != "setup.status" {
		t.Fatal("export granted authority", calls)
	}
	approve := []string{"--dry-run", "nearby", "approve", "--session", s.SessionID, "--revision", "1", "--file", path}
	if err := runWith(context.Background(), approve, io.Discard, nil, nearbyNoCalls(t)); nearbyErrorCode(t, err) != "setup_verification_required" {
		t.Fatal(err)
	}
	if err := runWith(context.Background(), append(approve, "--verified"), io.Discard, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
}
func TestNearbyDecodeAllowsProcessRestartWithoutSession(t *testing.T) {
	s := nearbyTestSnapshot("idle")
	s.State = "needs_restart"
	s.Process.State = "needs_restart"
	raw, _ := json.Marshal(s)
	if _, err := nearbyDecodeSnapshot(raw, "", false); err != nil {
		t.Fatal(err)
	}
	s = nearbyTestSnapshot("discovering")
	s.ContactText = "unsafe\x1b[2J"
	raw, _ = json.Marshal(s)
	if _, err := nearbyDecodeSnapshot(raw, s.SessionID, false); err == nil {
		t.Fatal("unvalidated contact text accepted")
	}
}
func TestNearbyLegacySetupFlagsRemainUnchanged(t *testing.T) {
	var out bytes.Buffer
	if err := runWith(context.Background(), []string{"--dry-run", "setup", "--network", "tailnet"}, &out, nil, nearbyNoCalls(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"command": "network.configure"`) {
		t.Fatal(out.String())
	}
}

func TestNearbyScopeReviewUsesExactBracketedIPv6Endpoints(t *testing.T) {
	scope := nearbyTestScope()
	scope.LocalAddress = "fd12:3456::10"
	scope.Prefix = "fd12:3456::/64"
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		nearbyWritePermission(&out, ja, "Device A", scope, 54546, 54547, true)
		if !strings.Contains(out.String(), "[fd12:3456::10]:54546") || !strings.Contains(out.String(), "[fd12:3456::10]:54547") {
			t.Fatal(out.String())
		}
	}
}

func TestNearbyCancelWaitsForPhysicalCleanupWithoutRepeatingCancel(t *testing.T) {
	for _, machine := range []bool{false, true} {
		pending := nearbyTestSnapshot("cancelling")
		pending.NextAction = "wait_for_cleanup"
		terminal := pending
		terminal.State = "cancelled"
		terminal.NextAction = "done"
		calls := []webui.Command{}
		client := func(_ context.Context, dir, raw string, target any) error {
			if dir != "/synthetic/profile" {
				t.Fatal("cancel lost profile")
			}
			var command webui.Command
			json.Unmarshal([]byte(raw), &command)
			calls = append(calls, command)
			result := pending
			if command.Name == "setup.status" {
				result = terminal
			}
			data, _ := json.Marshal(result)
			return json.Unmarshal(data, target)
		}
		args := []string{"--state-dir", "/synthetic/profile", "nearby", "cancel", "--session", pending.SessionID}
		if machine {
			args = append(args, "--json")
		}
		var out bytes.Buffer
		if err := runWith(context.Background(), args, &out, nil, client); err != nil {
			t.Fatal(err)
		}
		if machine {
			var result nearbySnapshot
			if json.Unmarshal(out.Bytes(), &result) != nil || result.State != "cancelling" || result.NextAction != "wait_for_cleanup" || len(calls) != 1 {
				t.Fatal(out.String(), calls)
			}
		} else if len(calls) != 2 || calls[0].Name != "setup.cancel" || calls[1].Name != "setup.status" || !strings.Contains(out.String(), "cleanup is not yet confirmed") || !strings.Contains(out.String(), "Nearby setup: Cancelled") {
			t.Fatal(out.String(), calls)
		}
	}
}
func TestNearbyInterruptedCleanupRemainsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := nearbyTestSnapshot("cancelling")
	s.NextAction = "wait_for_cleanup"
	var out bytes.Buffer
	c := nearbyCLI{ctx: ctx, dir: "/synthetic/profile", out: &out, client: nearbyNoCalls(t)}
	got, err := c.awaitCleanup(s, time.Hour)
	if !errors.Is(err, context.Canceled) || got.State != "cancelling" || strings.Contains(out.String(), "Cancelled") {
		t.Fatal(err, got, out.String())
	}
	for _, ja := range []bool{false, true} {
		out.Reset()
		nearbyWriteHuman(&out, "/synthetic/profile", ja, s)
		if !strings.Contains(out.String(), nearbyStateLabel(ja, "cancelling")) || !strings.Contains(out.String(), nearbyNextActionLabel(ja, "wait_for_cleanup")) {
			t.Fatal(out.String())
		}
	}
}
func TestNearbyPlanDisclosesPersistentConnectionAndStopWithoutRevocation(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		nearbyWriteHuman(&out, "/synthetic/profile", ja, nearbyTestPlanned())
		for _, needle := range []string{nearbyTestPlanned().Plan.Fingerprint, "soba --state-dir /synthetic/profile stop"} {
			if !strings.Contains(out.String(), needle) {
				t.Fatal(out.String())
			}
		}
		terms := []string{"not the resulting connection", "continue beyond", "does not revoke saved pairs"}
		if ja {
			terms = []string{"成立後の接続期限ではありません", "明示的に停止するまで継続", "保存済みのペアは解除されません"}
		}
		for _, term := range terms {
			if !strings.Contains(out.String(), term) {
				t.Fatalf("missing %q: %s", term, out.String())
			}
		}
	}
}

func TestNearbyApprovalDoesNotApplyWhenLifetimeDisclosureCannotBeShown(t *testing.T) {
	s := nearbyTestPlanned()
	args := []string{"nearby", "approve", "--session", s.SessionID, "--revision", "1", "--fingerprint", s.Plan.Fingerprint, "--verified"}
	if err := runWith(context.Background(), args, failedQRWriter{}, nil, nearbyNoCalls(t)); err == nil {
		t.Fatal("approved without showing persistent lifetime disclosure")
	}
}
