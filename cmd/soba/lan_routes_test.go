package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRouteCLIInputAndExplicitApproval(t *testing.T) {
	peer := strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	raw := `{"sender":"synthetic","box":"private-fixture"}`
	envelope, _ := json.Marshal(map[string]string{"update": raw})
	queries, requests := 0, 0
	var sent map[string]any
	expiry := time.Now().UTC().Add(time.Hour)
	query := func(name string, payload any, out any) error {
		queries++
		if name != "lan.routes.inspect" {
			t.Fatal(name)
		}
		b, _ := json.Marshal(map[string]any{"digest": "reviewed", "expires": expiry, "candidates": []map[string]string{{"candidateId": id}}})
		return json.Unmarshal(b, out)
	}
	request := func(name string, payload any) error {
		requests++
		if name != "lan.routes.apply" {
			t.Fatal(name)
		}
		sent = payload.(map[string]any)
		return nil
	}
	err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--candidates", id, "--ttl", "168h"}, false, false, io.Discard, bytes.NewReader(envelope), query, request)
	if err != nil || queries != 1 || requests != 1 {
		t.Fatal(err, queries, requests)
	}
	if sent["digest"] != "reviewed" || sent["update"] != raw || sent["lifetime"] != "finite" || !sent["expires"].(time.Time).Equal(expiry) {
		t.Fatal("exact review or expiry not preserved")
	}
}
func TestRouteCLIRejectsImplicitGrantAndArgumentSecrets(t *testing.T) {
	peer := strings.Repeat("a", 64)
	calls := 0
	request := func(string, any) error { calls++; return nil }
	for _, args := range [][]string{{"approve", peer, "--stdin"}, {"inspect", peer, `{"box":"private"}`}, {"approve", peer, "--current", "--candidates", "bad"}, {"add", "--relay", "192.0.2.20:443", "--certificate", strings.Repeat("a", 64), "--scope", "local"}} {
		if err := lanRoutesCommand(context.Background(), args, false, false, io.Discard, strings.NewReader(`{"box":"private"}`), nil, request); err == nil {
			t.Fatal("bad input accepted", args[0])
		}
	}
	if calls != 0 {
		t.Fatal("invalid CLI mutated state")
	}
	if _, err := commandPayload(context.Background(), []string{"lan.routes.apply", `{"update":"private"}`}, strings.NewReader(""), false); err == nil {
		t.Fatal("generic route secret argument accepted")
	}
	preview := previewPayload("lan.routes.apply", json.RawMessage(`{"peerId":"synthetic","update":"private-fixture"}`))
	if strings.Contains(string(preview), "private-fixture") {
		t.Fatal("private update in dry-run output")
	}
}
func TestRouteCLIDryRunNeverQueriesAndRevocationIsExplicit(t *testing.T) {
	peer, id := strings.Repeat("a", 64), strings.Repeat("b", 64)
	query := func(string, any, any) error { t.Fatal("dry-run queried server"); return nil }
	var name string
	var payload any
	request := func(n string, p any) error { name, payload = n, p; return nil }
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--current", "--candidates", id, "--until-revoked"}, true, true, io.Discard, strings.NewReader(""), query, request); err != nil {
		t.Fatal(err)
	}
	if name != "lan.routes.approve" || payload.(map[string]any)["lifetime"] != "until-revoked" || payload.(map[string]any)["expires"] != nil {
		t.Fatal(name, payload)
	}
	if err := lanRoutesCommand(context.Background(), []string{"revoke", peer}, false, false, io.Discard, nil, nil, request); err != nil {
		t.Fatal(err)
	}
	if name != "lan.routes.revoke" || payload.(map[string]any)["peerId"] != peer {
		t.Fatal("revocation changed peer")
	}
}
func TestRouteCLIBilingualHelpAndInputChoices(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := lanRoutesCommand(context.Background(), []string{"--help"}, ja, false, &out, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--json-file") || !strings.Contains(out.String(), "--candidates") || !strings.Contains(out.String(), "--until-revoked") || !strings.Contains(out.String(), "--ttl 168h") {
			t.Fatal("missing equivalent workflow")
		}
		if strings.Contains(out.String(), "720") || strings.Contains(out.String(), "24h") || strings.Contains(out.String(), "既定24") {
			t.Fatal("help retained a forced expiry or artificial lifetime ceiling")
		}
	}
	for _, tc := range []struct {
		path string
		pipe bool
	}{{"", false}, {"both", true}} {
		if _, err := readRouteInput(context.Background(), tc.path, tc.pipe, strings.NewReader("{}"), false); err == nil {
			t.Fatal("ambiguous private input")
		}
	}
}

func TestRouteCLIExplicitEmptyWithdrawal(t *testing.T) {
	peer := strings.Repeat("a", 64)
	calls := 0
	var sent map[string]any
	query := func(_ string, _ any, out any) error {
		b, _ := json.Marshal(map[string]any{"digest": "empty-proof", "expires": time.Now().Add(time.Hour), "candidates": []any{}})
		return json.Unmarshal(b, out)
	}
	request := func(name string, payload any) error {
		calls++
		sent = payload.(map[string]any)
		if name != "lan.routes.apply" && name != "lan.routes.export" {
			t.Fatal(name)
		}
		return nil
	}
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--withdrawal"}, false, false, io.Discard, strings.NewReader(`{"box":"synthetic"}`), query, request); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(sent["candidateIds"].([]string)) != 0 || sent["expires"] != nil || sent["lifetime"] != "finite" {
		t.Fatal("withdrawal granted permission")
	}
	if err := lanRoutesCommand(context.Background(), []string{"withdraw", peer, "--until-revoked"}, false, false, io.Discard, nil, nil, request); err != nil {
		t.Fatal(err)
	}
	if sent["withdraw"] != true || sent["lifetime"] != "until-revoked" {
		t.Fatal("withdrawal export not explicit")
	}
	query = func(_ string, _ any, out any) error {
		b, _ := json.Marshal(map[string]any{"digest": "not-empty", "expires": time.Now().Add(time.Hour), "candidates": []map[string]string{{"candidateId": strings.Repeat("b", 64)}}})
		return json.Unmarshal(b, out)
	}
	before := calls
	if err := lanRoutesCommand(context.Background(), []string{"approve", peer, "--stdin", "--withdrawal"}, false, false, io.Discard, strings.NewReader(`{"box":"synthetic"}`), query, request); err == nil || calls != before {
		t.Fatal("nonemptyoffer accepted as withdrawal")
	}
}

func TestRouteCLIExportExplicitLifetimes(t *testing.T) {
	peer := strings.Repeat("a", 64)
	for _, operation := range []string{"offer", "withdraw"} {
		for _, tc := range []struct {
			name    string
			flags   []string
			seconds int64
		}{
			{"until-revoked", []string{"--until-revoked"}, 0},
			{"finite", []string{"--ttl", "1s"}, 1},
			{"beyond-former-limit", []string{"--ttl", "721h"}, 721 * 3600},
			{"maximum-whole-duration", []string{"--ttl", "2562047h47m16s"}, 9223372036},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				calls := 0
				request := func(name string, input any) error {
					calls++
					p := input.(map[string]any)
					if name != "lan.routes.export" || p["peerId"] != peer || p["withdraw"] != (operation == "withdraw") {
						t.Fatal("wrong export", name, p)
					}
					if tc.seconds == 0 {
						if p["lifetime"] != "until-revoked" || p["ttlSeconds"] != nil {
							t.Fatal("permanent export has implicit expiry", p)
						}
					} else if p["lifetime"] != "finite" || p["ttlSeconds"] != tc.seconds {
						t.Fatal("finite export lost precise seconds", p)
					}
					return nil
				}
				args := append([]string{operation, peer}, tc.flags...)
				if err := lanRoutesCommand(context.Background(), args, false, false, io.Discard, nil, nil, request); err != nil || calls != 1 {
					t.Fatal(err, calls)
				}
			})
		}
	}
}

func TestRouteCLIRequiresUnambiguousExplicitLifetime(t *testing.T) {
	peer, id := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, ja := range []bool{false, true} {
		for _, base := range [][]string{{"offer", peer}, {"withdraw", peer}, {"approve", peer, "--current", "--candidates", id}} {
			for _, flags := range [][]string{nil, {"--until-revoked=false"}, {"--ttl", "0s"}, {"--ttl", "-1s"}, {"--ttl", "500ms"}, {"--ttl", "1.5s"}, {"--ttl", "1h", "--until-revoked"}, {"--ttl", "2562047h47m17s"}} {
				if base[0] == "approve" && len(flags) == 2 && (flags[1] == "500ms" || flags[1] == "1.5s") {
					continue // Approval expiries retain duration precision; export uses whole seconds.
				}
				args := append(append([]string(nil), base...), flags...)
				query := func(string, any, any) error { t.Fatal("invalid lifetime queried server"); return nil }
				request := func(string, any) error { t.Fatal("invalid lifetime sent an action"); return nil }
				err := lanRoutesCommand(context.Background(), args, ja, false, io.Discard, nil, query, request)
				if err == nil {
					t.Fatal("missing or conflicting lifetime accepted", args)
				}
				if len(flags) == 0 && (!strings.Contains(err.Error(), "--until-revoked") || !strings.Contains(err.Error(), "--ttl 168h")) {
					t.Fatal("missing lifetime error has no concrete next step", err)
				}
				if ja && len(flags) == 0 && !strings.Contains(err.Error(), "選んでください") {
					t.Fatal("missing Japanese lifetime guidance", err)
				}
			}
		}
	}
}

func TestRouteCLIApprovalLifetimeMatchesReviewedOffer(t *testing.T) {
	peer, id := strings.Repeat("a", 64), strings.Repeat("b", 64)
	finiteExpiry := time.Now().UTC().Add(time.Hour)
	for _, tc := range []struct {
		name     string
		offer    string
		expires  any
		flags    []string
		want     string
		wantFail bool
	}{
		{"permanent", "until-revoked", nil, []string{"--until-revoked"}, "until-revoked", false},
		{"finite-on-permanent", "until-revoked", nil, []string{"--ttl", "900h"}, "finite", false},
		{"fractional-finite", "until-revoked", nil, []string{"--ttl", "1500ms"}, "finite", false},
		{"finite-on-finite", "finite", finiteExpiry, []string{"--ttl", "900h"}, "finite", false},
		{"legacy-finite", "", finiteExpiry, []string{"--ttl", "900h"}, "finite", false},
		{"permanent-on-finite", "finite", finiteExpiry, []string{"--until-revoked"}, "", true},
		{"permanent-on-legacy", "", finiteExpiry, []string{"--until-revoked"}, "", true},
		{"missing-legacy-expiry", "", nil, []string{"--until-revoked"}, "", true},
		{"missing-finite-expiry", "finite", nil, []string{"--ttl", "1h"}, "", true},
		{"contradictory-permanent", "until-revoked", finiteExpiry, []string{"--until-revoked"}, "", true},
		{"unknown-offer-lifetime", "forever", nil, []string{"--until-revoked"}, "", true},
		{"expired-offer", "finite", time.Now().Add(-time.Second), []string{"--ttl", "1h"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries, actions := 0, 0
			query := func(name string, _ any, out any) error {
				queries++
				if name != "lan.routes.review" {
					t.Fatal(name)
				}
				raw, _ := json.Marshal(map[string]any{"digest": "exact-review", "lifetime": tc.offer, "expires": tc.expires, "candidates": []map[string]string{{"candidateId": id}}})
				return json.Unmarshal(raw, out)
			}
			started := time.Now().UTC()
			request := func(name string, input any) error {
				actions++
				p := input.(map[string]any)
				if name != "lan.routes.approve" || p["digest"] != "exact-review" || p["lifetime"] != tc.want {
					t.Fatal(name, p)
				}
				if tc.want == "until-revoked" {
					if p["expires"] != nil {
						t.Fatal("permanent approval includes an expiry")
					}
					encoded, _ := json.Marshal(p)
					if !bytes.Contains(encoded, []byte(`"expires":null`)) {
						t.Fatal("permanent expiry is not JSON null")
					}
				} else if tc.offer == "until-revoked" {
					expiry := p["expires"].(time.Time)
					ttl, err := time.ParseDuration(tc.flags[1])
					if err != nil {
						t.Fatal(err)
					}
					if expiry.Before(started.Add(ttl)) || expiry.After(time.Now().UTC().Add(ttl)) {
						t.Fatal("permanent offer capped finite approval")
					}
				} else if !p["expires"].(time.Time).Equal(finiteExpiry) {
					t.Fatal("finite offer did not cap approval")
				}
				return nil
			}
			args := append([]string{"approve", peer, "--current", "--candidates", id}, tc.flags...)
			err := lanRoutesCommand(context.Background(), args, false, false, io.Discard, nil, query, request)
			if (err != nil) != tc.wantFail || queries != 1 || (actions != 0) == tc.wantFail {
				t.Fatal("unexpected lifetime result", err, queries, actions)
			}
			if strings.HasPrefix(tc.name, "permanent-on-") && !strings.Contains(err.Error(), "--ttl 168h") {
				t.Fatal("finite offer rejection lacks next step", err)
			}
		})
	}
}

func TestRouteCLIUntilRevokedWithdrawalHasNoGrantExpiry(t *testing.T) {
	peer := strings.Repeat("a", 64)
	query := func(_ string, _ any, out any) error {
		raw, _ := json.Marshal(map[string]any{"digest": "withdrawal-review", "lifetime": "until-revoked", "expires": nil, "candidates": []any{}})
		return json.Unmarshal(raw, out)
	}
	calls := 0
	request := func(name string, input any) error {
		calls++
		p := input.(map[string]any)
		if name != "lan.routes.apply" || p["lifetime"] != "until-revoked" || p["expires"] != nil || len(p["candidateIds"].([]string)) != 0 {
			t.Fatal("withdrawal created a grant", p)
		}
		return nil
	}
	args := []string{"approve", peer, "--stdin", "--withdrawal"}
	if err := lanRoutesCommand(context.Background(), args, false, false, io.Discard, strings.NewReader(`{"box":"synthetic"}`), query, request); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	for _, flags := range [][]string{{"--ttl", "1h"}, {"--until-revoked"}} {
		bad := append(append([]string(nil), args...), flags...)
		if err := lanRoutesCommand(context.Background(), bad, false, false, io.Discard, nil, nil, request); err == nil || calls != 1 {
			t.Fatal("withdrawal silently ignored lifetime flags", err)
		}
	}
}
