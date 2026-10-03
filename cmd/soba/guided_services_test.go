package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type guideHarness struct {
	snapshot   guidedSnapshot
	calls      []webui.Command
	failStarts int
	starts     int
}

func TestGuidedReviewUsesEffectiveSamePortAndIPv6Mappings(t *testing.T) {
	for _, tc := range []struct {
		spec  core.ServiceSpec
		share bool
		want  string
	}{
		{core.ServiceSpec{Ports: "8080", LoopbackHost: "127.0.0.1"}, true, "127.0.0.1:8080"},
		{core.ServiceSpec{Ports: "22", LocalPort: 2222, LoopbackHost: "::1"}, false, "[::1]:2222"},
		{core.ServiceSpec{Ports: "8000-8002", ExcludePorts: "8001", LocalPort: 18000, LoopbackHost: "::1"}, false, "[::1]:18000-18001"},
		{core.ServiceSpec{Ports: "54542-54546", LoopbackHost: "127.0.0.1"}, true, "127.0.0.1:54542,54546"},
	} {
		if got := plannedLocalMapping(tc.spec, tc.share); got != tc.want {
			t.Fatalf("mapping %q, want %q", got, tc.want)
		}
		for _, ja := range []bool{false, true} {
			var out bytes.Buffer
			g := serviceGuide{spec: tc.spec, ja: ja, p: newPrompts(strings.NewReader(""), &out)}
			if tc.share {
				g.command = "share"
			}
			g.review()
			if !strings.Contains(out.String(), tc.want) {
				t.Fatal(out.String())
			}
		}
	}
}

func (h *guideHarness) call(_ context.Context, _ string, raw string, out any) error {
	if raw == "status" {
		data, _ := json.Marshal(h.snapshot)
		return json.Unmarshal(data, out)
	}
	var cmd webui.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		return err
	}
	if cmd.Name == "discovery.refresh" {
		data, _ := json.Marshal(map[string]any{"services": h.snapshot.AvailableServices, "partial": false})
		return json.Unmarshal(data, out)
	}
	if cmd.Name == "client.settings" {
		var payload map[string]any
		if len(h.calls) > 0 {
			_ = json.Unmarshal(h.calls[len(h.calls)-1].Payload, &payload)
		}
		name, _ := payload["name"].(string)
		data, _ := json.Marshal(core.ClientSettingsView{Services: []core.ClientServiceSettings{{Name: name, LocalEndpoint: "127.0.0.1:8080", HTTPCandidate: "http://127.0.0.1:8080/", Status: "active", Application: "unverified"}}})
		return json.Unmarshal(data, out)
	}
	h.calls = append(h.calls, cmd)
	if cmd.Name == "service.connect" || cmd.Name == "service.share" {
		h.starts++
		if h.starts <= h.failStarts {
			return &control.RemoteError{Code: "service_changed", Message: "reviewed service changed"}
		}
	}
	return json.Unmarshal([]byte(`{"status":"active","application":"unverified"}`), out)
}
func guideFixture() *guideHarness {
	return &guideHarness{snapshot: guidedSnapshot{Peers: []guidedPeer{{ID: "peer-one", Name: "日本語端末", Verified: true, Online: true}, {ID: "peer-two", Name: "Second", Verified: true}}, AvailableServices: []guidedOffer{{ID: "grant-one", Revision: "opaque-review", Name: "Test API", PeerID: "peer-one", Network: "tcp", Ports: "8080", Purpose: "web", Lifetime: "finite", ExpiresAt: time.Now().Add(time.Hour), CheckedAt: time.Now(), Application: "unverified"}}}}
}
func runGuide(t *testing.T, h *guideHarness, command, locale, input string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runWith(context.Background(), []string{"--state-dir", t.TempDir(), "--locale", locale, command, "--interactive"}, &out, strings.NewReader(input), h.call)
	return out.String(), err
}
func TestGuidedAdvertisedServiceReviewEditAndBinding(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		t.Run(locale, func(t *testing.T) {
			h := guideFixture()
			output, err := runGuide(t, h, "connect", locale, "1\n\n\n\n\n n\n日本語API\ny\n")
			if err != nil {
				t.Fatal(err, output)
			}
			if h.starts != 1 {
				t.Fatal(h.starts)
			}
			var got map[string]any
			json.Unmarshal(h.calls[0].Payload, &got)
			if got["serviceId"] != "grant-one" || got["serviceRevision"] != "opaque-review" || got["name"] != "日本語API" || got["peerId"] != "peer-one" || got["purpose"] != "web" {
				t.Fatalf("lost reviewed scope: %v", got)
			}
			if strings.Contains(output, "opaque-review") {
				t.Fatal("opaque revision exposed in guidance")
			}
			if !strings.Contains(output, "http://127.0.0.1:8080/") {
				t.Fatal(output)
			}
		})
	}
}
func TestGuidedManualShareRetriesFieldsAndCancelsWithoutMutation(t *testing.T) {
	h := guideFixture()
	output, err := runGuide(t, h, "share", "ja", "99\n1,2\nssh\ntcp\nbad\n22\n\n0\n\nfinite\n500ms\n2h\ny\n共有名\nq\n")
	if !errors.Is(err, context.Canceled) || h.starts != 0 {
		t.Fatalf("%v starts=%d output=%s", err, h.starts, output)
	}
	if !strings.Contains(output, "値が正しくありません") || !strings.Contains(output, "共有名") {
		t.Fatal(output)
	}
}
func TestGuidedBackDoesNotSilentlyKeepAdvertisedBinding(t *testing.T) {
	h := guideFixture()
	output, err := runGuide(t, h, "connect", "en", "1\n\n\n\n\ns\nm\n2\ny\n")
	if err != nil {
		t.Fatal(err, output)
	}
	var got map[string]any
	json.Unmarshal(h.calls[0].Payload, &got)
	if _, ok := got["serviceId"]; ok {
		t.Fatal("manual selection retained grant")
	}
	if got["peerId"] != "peer-one" {
		t.Fatal(got)
	}
}
func TestGuidedStartFailureRequiresReviewedRetry(t *testing.T) {
	h := guideFixture()
	h.failStarts = 1
	output, err := runGuide(t, h, "connect", "en", "1\n\n\n\n\ny\nr\ny\n")
	if err != nil || h.starts != 2 {
		t.Fatal(err, h.starts, output)
	}
	if !strings.Contains(output, "no successful start has been confirmed") {
		t.Fatal(output)
	}
}
func TestGuidedStaleOfferIsNotSelectable(t *testing.T) {
	h := guideFixture()
	h.snapshot.AvailableServices[0].CheckedAt = time.Now().Add(-time.Minute)
	output, err := runGuide(t, h, "connect", "en", "1\nq\n")
	if !errors.Is(err, context.Canceled) || h.starts != 0 || strings.Contains(output, "1. 日本語端末 | Test API") {
		t.Fatal(err, output)
	}
}
func TestGuidedPrivateTraceAndMachinePathsNeverReadInput(t *testing.T) {
	h := guideFixture()
	t.Setenv("TEA_TRACE", "fixture")
	for _, args := range [][]string{{"connect", "--interactive"}, {"connect", "--interactive", "--json"}, {"connect", "--json"}} {
		var out bytes.Buffer
		err := runWith(context.Background(), args, &out, panicReader{}, h.call)
		if err == nil || h.starts != 0 {
			t.Fatal(args, err)
		}
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("noninteractive command read input") }
func TestGuidedSelectionRejectsAmbiguousNamesAndDuplicatePeers(t *testing.T) {
	peers := []guidedPeer{{ID: "one", Name: "same"}, {ID: "two", Name: "same"}}
	for _, input := range []string{"same", "1,1", "1,two", "3"} {
		_, err := chooseGuidedPeers(peers, input, false)
		if err == nil {
			t.Fatal(input)
		}
	}
	got, err := chooseGuidedPeers(peers, "1,2", true)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
}
func TestAdvertisedServiceFlagsPairAndMachinePayload(t *testing.T) {
	for _, args := range [][]string{{"--service-id", "grant"}, {"--service-revision", "review"}} {
		_, err := servicePayload("connect", append([]string{"--name", "example", "--peer", "peer", "--ports", "8080"}, args...), false, io.Discard)
		if err == nil {
			t.Fatal(args)
		}
	}
	got, err := servicePayload("connect", []string{"--json", "--name", "example", "--peer", "peer", "--ports", "8080", "--service-id", "grant", "--service-revision", "review"}, false, io.Discard)
	if err != nil || got["serviceRevision"] != "review" {
		t.Fatal(got, err)
	}
}
func TestStoppedStatusAndStopRemainIdempotent(t *testing.T) {
	for _, command := range []string{"status", "stop"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{command, "--json"}, &out, panicReader{}, func(context.Context, string, string, any) error { return os.ErrNotExist })
		var compact bytes.Buffer
		_ = json.Compact(&compact, out.Bytes())
		if err != nil || !strings.Contains(compact.String(), `"state":"stopped"`) {
			t.Fatal(command, err, out.String())
		}
	}
	var out bytes.Buffer
	denied := os.ErrPermission
	err := runWith(context.Background(), []string{"stop"}, &out, panicReader{}, func(context.Context, string, string, any) error { return denied })
	if !errors.Is(err, denied) {
		t.Fatal("permission failure concealed", err)
	}
}
func TestSavedRulesAndSettingsAreOfflineAndPreserveMachineValues(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"--offline", "service", "save", "connect", "--backend", "tailnet", "--name", "日本語SSH", "--preset", "ssh", "--peer", "peer-example"}, {"--offline", "rules", "--json"}, {"--offline", "settings"}} {
		var out bytes.Buffer
		err := runWith(context.Background(), append([]string{"--state-dir", dir}, args...), &out, panicReader{}, func(context.Context, string, string, any) error {
			t.Fatal("offline command contacted agent")
			return nil
		})
		if err != nil {
			t.Fatal(args, err)
		}
		if args[1] == "rules" && !strings.Contains(out.String(), "日本語SSH") {
			t.Fatal(out.String())
		}
		if args[1] == "settings" && !strings.Contains(out.String(), "HostKeyAlias=") {
			t.Fatal(out.String())
		}
	}
}
func TestServiceHintsUseSharedCoreResultWithoutChangingValues(t *testing.T) {
	var out bytes.Buffer
	writeClientServiceHints(&out, false, core.ClientServiceSettings{Name: "example", Status: "saved", SSH: &core.SSHClientHint{Command: "ssh -o HostKeyAlias=fixture -p 2222 <user>@127.0.0.1"}, HTTPCandidate: "http://127.0.0.1:8080/", Notices: []core.ClientNotice{{Message: "Keep original TLS name", MessageJA: "元のTLS名を維持"}}})
	got := out.String()
	if !strings.Contains(got, "original TLS name") || !strings.Contains(got, "HostKeyAlias=fixture") || !strings.Contains(got, "http://127.0.0.1:8080/") {
		t.Fatal(got)
	}
	out.Reset()
	writeClientServiceHints(&out, true, core.ClientServiceSettings{Name: "日本語の名前", SSH: &core.SSHClientHint{Command: "ssh fixture"}, Notices: []core.ClientNotice{{Message: "English notice", MessageJA: "日本語の案内"}}})
	if strings.Contains(out.String(), "English notice") || !strings.Contains(out.String(), "日本語の案内") || !strings.Contains(out.String(), "ssh fixture") {
		t.Fatal(out.String())
	}
}
func TestProfileCommandExamplesDoNotUseShellExpansion(t *testing.T) {
	for _, dir := range []string{"/tmp/example path", "/tmp/$(fixture)", "/tmp/`fixture`", "/tmp/$value"} {
		got := cliCommandExample(dir, "status")
		if !strings.HasPrefix(got, "argv: [") {
			t.Fatal(got)
		}
	}
	if got := cliCommandExample("/tmp/example", "status"); got != "soba --state-dir /tmp/example status" {
		t.Fatal(got)
	}
}

func TestDiscoveryCommandPreservesAuthenticatedMachineResponse(t *testing.T) {
	for _, locale := range []string{"en", "ja"} {
		var out bytes.Buffer
		err := runWith(context.Background(), []string{"--locale", locale, "discover", "--peer", "peer-example", "--json"}, &out, panicReader{}, func(_ context.Context, _ string, raw string, result any) error {
			var request webui.Command
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				return err
			}
			if request.Name != "discovery.refresh" || string(request.Payload) != `{"peerId":"peer-example"}` {
				t.Fatal(request)
			}
			return json.Unmarshal([]byte(`{"services":[],"observations":[{"peerId":"peer-example","state":"unavailable"}],"partial":true}`), result)
		})
		if err != nil || !strings.Contains(out.String(), `"partial": true`) || !strings.Contains(out.String(), `"state": "unavailable"`) {
			t.Fatal(err, out.String())
		}
	}
}
func TestGuidedPlainInputCancellationAndNoControlEcho(t *testing.T) {
	var out bytes.Buffer
	p := newPrompts(strings.NewReader("bad\x1b[31m\n日本語\n"), &out)
	p.ja = true
	got, err := p.ask("名前: ")
	if err != nil || got != "日本語" || strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "制御文字") {
		t.Fatal(got, err, out.String())
	}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p = newPrompts(r, io.Discard)
	p.ctx = ctx
	done := make(chan error, 1)
	go func() { _, err := p.ask("Name: "); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}

func TestGuidedLongReviewRefreshesOnlyTheSameUnshortenedGrant(t *testing.T) {
	original := guideFixture().snapshot.AvailableServices[0]
	original.CheckedAt = time.Now().Add(-time.Minute)
	original.Revision = "older-reviewed-revision"
	for _, change := range []string{"unchanged", "renewed", "grant", "purpose", "protocol", "ports", "shortened", "lifetime", "peer", "stale"} {
		t.Run(change, func(t *testing.T) {
			reviewed := original
			current := original
			current.CheckedAt = time.Now()
			current.Revision = "fresh-reviewed-revision"
			switch change {
			case "renewed":
				current.ExpiresAt = current.ExpiresAt.Add(time.Hour)
			case "grant":
				current.ID = "replacement"
			case "purpose":
				current.Purpose = "ssh"
			case "protocol":
				current.Network = "udp"
			case "ports":
				current.Ports = "9090"
			case "shortened":
				current.ExpiresAt = current.ExpiresAt.Add(-time.Minute)
			case "lifetime":
				current.Lifetime = "until-revoked"
			case "peer":
				current.PeerID = "other"
			case "stale":
				current.CheckedAt = time.Now().Add(-time.Minute)
			}
			g := &serviceGuide{selected: &reviewed, queryAction: func(name string, payload, result any) error {
				if name != "discovery.refresh" || payload.(map[string]string)["peerId"] != original.PeerID {
					t.Fatal(name, payload)
				}
				data, _ := json.Marshal(map[string]any{"services": []guidedOffer{current}})
				return json.Unmarshal(data, result)
			}}
			err := g.refreshSelected()
			valid := change == "unchanged" || change == "renewed"
			if (err == nil) != valid {
				t.Fatal(change, err)
			}
			if valid && g.selected.Revision != "fresh-reviewed-revision" {
				t.Fatal("long review did not regain freshness")
			}
			if !valid && g.selected.Revision != original.Revision {
				t.Fatal("changed grant silently replaced review")
			}
		})
	}
}
