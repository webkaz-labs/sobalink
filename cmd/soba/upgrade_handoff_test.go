package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

// All tests in this file are inert handler/fake-callback tests. No listener,
// application, profile, child process, native exit observer or login is opened.
func handoffFixture() upgradeHandoffBootstrap {
	deadline := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	in := upgradeHandoffBootstrap{Origin: "http://127.0.0.1:12345", Locale: "en", Old: upgradeIdentity{ProcessID: 42, Instance: "synthetic-instance"}, Intent: core.UpgradeIntent{PeerID: "synthetic-peer", Deadline: deadline, ExpectedRevision: "synthetic-revision"}, Review: core.UpgradeReview{PeerID: "synthetic-peer", Deadline: deadline, Revision: "synthetic-revision", RestartRequired: true}}
	in.Authorization = "synthetic-old-session-authorization"
	in.AuthorizationPayload, _ = json.Marshal(webUpgradeRequest{Intent: in.Intent, Locale: in.Locale})
	return in
}
func newInertHandoff() (*handoffState, *int, *int) {
	starts, cancels := new(int), new(int)
	in := handoffFixture()
	until, _ := time.Parse(time.RFC3339, in.Intent.Deadline)
	h := &handoffState{in: in, until: until, transfer: strings.Repeat("a", 64), control: strings.Repeat("b", 64), phase: "waiting", start: func() { *starts++ }, cancel: func() { *cancels++ }}
	return h, starts, cancels
}
func handoffRequest(h *handoffState, path, token string, change func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:54321"+path, strings.NewReader(`{}`))
	r.RemoteAddr = "127.0.0.1:34567"
	r.Header.Set("Origin", "http://127.0.0.1:54321")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Handoff-Token", token)
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	h.handler("http://127.0.0.1:54321", "127.0.0.1:54321").ServeHTTP(w, r)
	return w
}
func claimInert(t *testing.T, h *handoffState) {
	t.Helper()
	if w := handoffRequest(h, "/claim", strings.Repeat("a", 64), nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestWebHandoffBootstrapPreservesExactBoundedReview(t *testing.T) {
	in := handoffFixture()
	if !validHandoffBootstrap(in, time.Now()) {
		t.Fatal("valid review rejected")
	}
	for _, mutate := range []func(*upgradeHandoffBootstrap){func(b *upgradeHandoffBootstrap) { b.Intent.PeerID = "other" }, func(b *upgradeHandoffBootstrap) { b.Intent.ExpectedRevision = "different" }, func(b *upgradeHandoffBootstrap) { b.Intent.Deadline = time.Now().Add(time.Hour).Format(time.RFC3339) }, func(b *upgradeHandoffBootstrap) { b.Review.RestartRequired = false }, func(b *upgradeHandoffBootstrap) { b.Origin = "https://example.invalid" }, func(b *upgradeHandoffBootstrap) { b.Old.Instance = "" }} {
		bad := in
		mutate(&bad)
		if validHandoffBootstrap(bad, time.Now()) {
			t.Fatal("mismatched review accepted")
		}
	}
}
func TestWebHandoffRequiresClaimCommitAndAcknowledgement(t *testing.T) {
	h, starts, _ := newInertHandoff()
	token := h.control
	if w := handoffRequest(h, "/ack", token, nil); w.Code == 200 {
		t.Fatal("unclaimed ack accepted")
	}
	claimInert(t, h)
	if w := handoffRequest(h, "/ack", token, nil); w.Code == 200 {
		t.Fatal("unreviewed ack accepted")
	}
	if w := handoffRequest(h, "/commit", token, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if *starts != 0 {
		t.Fatal("started before acknowledgement")
	}
	if w := handoffRequest(h, "/ack", token, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if *starts != 1 {
		t.Fatal(*starts)
	}
	for _, path := range []string{"/claim", "/commit", "/ack"} {
		if w := handoffRequest(h, path, token, nil); w.Code == 200 {
			t.Fatal("replay accepted", path)
		}
	}
	if *starts != 1 {
		t.Fatal("duplicate start")
	}
}
func TestWebHandoffClaimIsOneUseAndDoesNotAuthenticateControl(t *testing.T) {
	h, _, _ := newInertHandoff()
	transfer := h.transfer
	claimInert(t, h)
	if h.transfer != "" {
		t.Fatal("transfer retained")
	}
	for _, path := range []string{"/claim", "/commit", "/status", "/cancel"} {
		if w := handoffRequest(h, path, transfer, nil); w.Code == 200 {
			t.Fatal("transfer authorized", path)
		}
	}
}
func TestWebHandoffRejectsOriginHostRemoteAndTokenSubstitution(t *testing.T) {
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Host = "localhost:54321" }, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:12345") }, func(r *http.Request) { r.Header.Del("Origin") }, func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1234" }, func(r *http.Request) { r.Header.Set("X-Handoff-Token", "wrong") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, func(r *http.Request) { r.URL.RawQuery = "token=wrong" }} {
		h, starts, _ := newInertHandoff()
		if w := handoffRequest(h, "/claim", h.transfer, change); w.Code == 200 {
			t.Fatal("unbound request accepted")
		}
		if *starts != 0 || h.phase != "waiting" {
			t.Fatal("state mutated")
		}
	}
}
func TestWebHandoffCancelBeforeAckNeverStarts(t *testing.T) {
	for _, commit := range []bool{false, true} {
		h, starts, cancels := newInertHandoff()
		token := h.control
		claimInert(t, h)
		if commit {
			handoffRequest(h, "/commit", token, nil)
		}
		handoffRequest(h, "/cancel", token, nil)
		if w := handoffRequest(h, "/ack", token, nil); w.Code == 200 {
			t.Fatal("cancelled ack accepted")
		}
		if *starts != 0 || *cancels == 0 {
			t.Fatal(*starts, *cancels)
		}
	}
}
func TestWebHandoffExpiryAndLostPageLeaseDenyStart(t *testing.T) {
	for _, deadline := range []bool{true, false} {
		h, starts, _ := newInertHandoff()
		token := h.control
		claimInert(t, h)
		handoffRequest(h, "/commit", token, nil)
		if deadline {
			h.until = time.Now().Add(-time.Second)
		} else {
			h.lastSeen = time.Now().Add(-11 * time.Second)
		}
		if w := handoffRequest(h, "/ack", token, nil); w.Code != 410 {
			t.Fatal(w.Code)
		}
		if *starts != 0 {
			t.Fatal("expired start")
		}
	}
}
func TestWebHandoffDeliversPrivateResultOnlyOnce(t *testing.T) {
	h, _, _ := newInertHandoff()
	claimInert(t, h)
	h.phase = "ready"
	h.result = map[string]any{"state": "ready", "code": "synthetic-code", "url": "http://127.0.0.1:45678"}
	first := handoffRequest(h, "/status", h.control, nil)
	second := handoffRequest(h, "/status", h.control, nil)
	if !strings.Contains(first.Body.String(), "synthetic-code") || strings.Contains(second.Body.String(), "synthetic-code") || h.result != nil {
		t.Fatal("private result delivery not one-use")
	}
	if first.Header().Get("Cache-Control") != "no-store" || first.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("private headers missing")
	}
}
func TestSharedUpgradeControllerNeverRefreshesIntent(t *testing.T) {
	in := handoffFixture().Intent
	var seen []core.UpgradeIntent
	restarts := 0
	query := func(name string, payload any, result any) error {
		if name != "direct-lan.upgrade.run" {
			t.Fatal(name)
		}
		intent := payload.(core.UpgradeIntent)
		seen = append(seen, intent)
		*result.(*core.UpgradeProgress) = core.UpgradeProgress{PeerID: in.PeerID, Deadline: in.Deadline, RestartRequired: len(seen) == 1}
		return nil
	}
	_, err := applyManagedUpgradeIntent(in, query, func() error { restarts++; return nil })
	if err != nil || restarts != 1 || !reflect.DeepEqual(seen, []core.UpgradeIntent{in, in}) {
		t.Fatal(err, restarts, seen)
	}
}
func TestSharedUpgradeControllerStopsAfterFailedRestart(t *testing.T) {
	in := handoffFixture().Intent
	calls := 0
	failure := errors.New("unconfirmed shutdown")
	_, err := applyManagedUpgradeIntent(in, func(_ string, _ any, result any) error {
		calls++
		*result.(*core.UpgradeProgress) = core.UpgradeProgress{PeerID: in.PeerID, Deadline: in.Deadline, RestartRequired: true}
		return nil
	}, func() error { return failure })
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatal(err, calls)
	}
}
func TestWebHandoffPublicAssetsNeverContainCapabilityOrCode(t *testing.T) {
	h, _, _ := newInertHandoff()
	h.result = map[string]any{"code": "synthetic-private-code"}
	for _, path := range []string{"/", "/bridge.js"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:54321"+path, nil)
		r.RemoteAddr = "127.0.0.1:34567"
		w := httptest.NewRecorder()
		h.handler("http://127.0.0.1:54321", "127.0.0.1:54321").ServeHTTP(w, r)
		for _, secret := range []string{h.transfer, h.control, "synthetic-private-code"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret in public asset")
			}
		}
	}
	raw, _ := json.Marshal(upgradeHandoffDescriptor{URL: "http://127.0.0.1:54321", Token: h.transfer, Deadline: h.in.Intent.Deadline})
	if strings.Contains(string(raw), "?token") {
		t.Fatal("URL capability")
	}
}

func TestCLIUpgradeCallsStayPinnedAcrossVerifiedReplacement(t *testing.T) {
	pinned := upgradeIdentity{ProcessID: 42, Instance: "synthetic-old"}
	var seen []upgradeBound
	client := boundUpgradeClient(func(_ context.Context, _ string, raw string, _ any) error {
		if !strings.HasPrefix(raw, lifecycleBoundPrefix) {
			t.Fatal("unbound request")
		}
		var in upgradeBound
		if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleBoundPrefix)), &in) != nil {
			t.Fatal("invalid request")
		}
		seen = append(seen, in)
		return nil
	}, &pinned)
	for _, raw := range []string{`{"name":"direct-lan.upgrade.run"}`, `{"name":"direct-lan.upgrade.status"}`, "ui"} {
		if err := client(context.Background(), "synthetic-profile", raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	pinned = upgradeIdentity{ProcessID: 43, Instance: "synthetic-new"}
	for _, raw := range []string{`{"name":"direct-lan.upgrade.run"}`, "ui"} {
		if err := client(context.Background(), "synthetic-profile", raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i, in := range seen {
		expected := 42
		instance := "synthetic-old"
		if i >= 3 {
			expected = 43
			instance = "synthetic-new"
		}
		if in.ProcessID != expected || in.Instance != instance {
			t.Fatal("binding changed", in)
		}
	}
}

func TestWebSuccessorOpenIsExplicitOneUseAndNoCredentials(t *testing.T) {
	h, _, _ := newInertHandoff()
	claimInert(t, h)
	h.phase = "ready"
	opened := 0
	h.open = func() error { opened++; return nil }
	if w := handoffRequest(h, "/open", h.control, nil); w.Code == 200 || opened != 0 {
		t.Fatal("opened before private delivery")
	}
	h.delivered = true
	if w := handoffRequest(h, "/open", h.control, nil); w.Code != 200 || opened != 1 {
		t.Fatal("explicit open failed")
	}
	if w := handoffRequest(h, "/open", h.control, nil); w.Code == 200 || opened != 1 {
		t.Fatal("open replayed")
	}
}

func TestWebSuccessorOpenRechecksIdentityAndBareAddress(t *testing.T) {
	previous := launchLoginBrowser
	defer func() { launchLoginBrowser = previous }()
	pinned := upgradeIdentity{ProcessID: 43, Instance: "synthetic-new", Executable: "synthetic-executable"}
	address := "http://127.0.0.1:54321"
	for _, variant := range []string{"valid", "identity", "address", "url-secret"} {
		opened := ""
		launchLoginBrowser = func(_ context.Context, url string) error { opened = url; return nil }
		client := func(_ context.Context, _ string, raw string, result any) error {
			if raw == lifecycleIdentityCommand {
				*result.(*upgradeIdentity) = pinned
				if variant == "identity" {
					result.(*upgradeIdentity).Instance = "other"
				}
				return nil
			}
			var in upgradeBound
			if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleBoundPrefix)), &in) != nil || in.Command != "ui.url" || in.ProcessID != pinned.ProcessID || in.Instance != pinned.Instance {
				t.Fatal("unbound UI address")
			}
			*result.(*string) = address
			if variant == "address" {
				*result.(*string) = "http://127.0.0.1:54322"
			}
			return nil
		}
		url := address
		if variant == "url-secret" {
			url += "?code=synthetic"
		}
		err := openVerifiedUpgradeUI(context.Background(), "synthetic-profile", pinned, url, client)
		if variant == "valid" {
			if err != nil || opened != address {
				t.Fatal(err, opened)
			}
		} else if err == nil || opened != "" {
			t.Fatal("unverified browser launch", variant)
		}
	}
}

func TestWebLifecycleCancelledRequestCannotAdmitShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &upgradeLifecycle{identity: upgradeIdentity{ProcessID: 42, Instance: "synthetic"}, stop: make(chan upgradeStop, 1)}
	raw, _ := json.Marshal(upgradeStop{ProcessID: 42, Instance: "synthetic"})
	if _, err := l.handler(nil)(ctx, lifecycleStopPrefix+string(raw)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if l.stopping.Load() || len(l.stop) != 0 {
		t.Fatal("cancelled shutdown admitted")
	}
}

func TestWebHandoffExpiredUnclaimedLeaseCannotBeRevived(t *testing.T) {
	h, starts, cancels := newInertHandoff()
	h.lastSeen = time.Now().Add(-11 * time.Second)
	token := h.transfer
	if w := handoffRequest(h, "/claim", token, nil); w.Code != 410 {
		t.Fatal(w.Code)
	}
	if h.phase != "waiting" || h.transfer != token || *starts != 0 || *cancels != 1 {
		t.Fatal("expired unclaimed capability revived")
	}
}
