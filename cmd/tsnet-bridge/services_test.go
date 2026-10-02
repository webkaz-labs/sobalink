package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

func sharedService() app.DiscoveredService {
	return app.DiscoveredService{PeerID: "node-server", PeerHost: "server.example.ts.net", ID: "00112233445566778899aabbccddeeff", Purpose: "web", Network: "tcp", Port: 8123, ExpiresAt: time.Now().Add(time.Hour), CheckedAt: time.Now(), Application: "unverified"}
}

func installServices(t *testing.T, fake *fakeRules, get func(string) (app.ServiceCatalog, error)) {
	t.Helper()
	installRequest(t, func(ctx context.Context, dir, command string, value any) error {
		if command == "services" || strings.HasPrefix(command, "services:") {
			catalog, err := get(command)
			if err != nil {
				return err
			}
			return assign(value, catalog)
		}
		return fake.call(ctx, dir, command, value)
	})
}

func TestServicePickerFillsRemoteSettingsAndRechecksBeforeSaveStart(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	service := sharedService()
	service.Network = "udp"
	var calls []string
	installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
		calls = append(calls, command)
		service.CheckedAt = time.Now()
		return app.ServiceCatalog{Services: []app.DiscoveredService{service}}, nil
	})
	var out bytes.Buffer
	err := configureRule(t.Context(), d, "forward", []string{"--name", "chosen"}, strings.NewReader("1\ny\n"), &out)
	if err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	r := c.Rules[0]
	if len(c.Rules) != 1 || r.PeerID != service.PeerID || r.TargetHost != service.PeerHost || r.Purpose != service.Purpose || r.Network != "udp" || r.TargetPort != service.Port {
		t.Fatal(c)
	}
	if !reflect.DeepEqual(calls, []string{"services", "services:node-server", "services:node-server"}) || !reflect.DeepEqual(fake.actions(), []string{"save", "start"}) {
		t.Fatal(calls, fake.actions())
	}
	if strings.Contains(out.String(), "Service port [") || !strings.Contains(out.String(), "Application behavior remains unverified") {
		t.Fatal(out.String())
	}
}

func TestServicePickerEmptyRefreshAndCancel(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	count := 0
	installServices(t, fake, func(string) (app.ServiceCatalog, error) {
		count++
		return app.ServiceCatalog{Peers: []app.DiscoveryPeer{{PeerID: "node-server", State: "unavailable"}, {PeerID: "node-old", State: "unsupported"}}}, nil
	})
	var out bytes.Buffer
	err := configureRule(t.Context(), d, "forward", nil, strings.NewReader("r\nq\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "canceled") || count != 2 || len(fake.actions()) != 0 {
		t.Fatal(err, count, fake.actions())
	}
	for _, want := range []string{"No currently confirmed shares", "m manual", "does not establish", "unsupported: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
}

func TestServicePickerUnavailableCanExplicitlyEnterManual(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	installServices(t, fake, func(string) (app.ServiceCatalog, error) { return app.ServiceCatalog{}, errors.New("unavailable") })
	var out bytes.Buffer
	err := configureRule(t.Context(), d, "forward", []string{"--name", "manual", "--save-only"}, strings.NewReader("m\n1\nweb\n8123\ny\n"), &out)
	if err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].TargetPort != 8123 || !strings.Contains(out.String(), "Manual configuration") {
		t.Fatal(c, out.String())
	}
}

func TestServicePickerChangedShareRequiresReselectionAndNewConfirmation(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	old := sharedService()
	next := old
	next.ID, next.Port, next.Purpose = "ffeeddccbbaa99887766554433221100", 9999, "custom"
	lists, checks := 0, 0
	installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
		if command == "services" {
			lists++
			if lists == 1 {
				return app.ServiceCatalog{Services: []app.DiscoveredService{old}}, nil
			}
		} else {
			checks++
		}
		return app.ServiceCatalog{Services: []app.DiscoveredService{next}}, nil
	})
	var out bytes.Buffer
	err := configureRule(t.Context(), d, "forward", []string{"--name", "chosen", "--save-only"}, strings.NewReader("1\ny\n1\ny\n"), &out)
	if err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].TargetPort != 9999 || c.Rules[0].Purpose != "custom" || lists != 2 || checks != 2 {
		t.Fatal(c, lists, checks)
	}
	if !strings.Contains(out.String(), "selected share changed") || strings.Count(out.String(), "Save disabled without connecting?") != 2 {
		t.Fatal(out.String())
	}
}

func TestServicePickerRevalidationFailureNeverSilentlyStarts(t *testing.T) {
	for _, afterSave := range []bool{false, true} {
		t.Run(fmt.Sprint(afterSave), func(t *testing.T) {
			allowWizardPorts(t, nil)
			d := saveRules(t)
			fake := newFake(t, d)
			service := sharedService()
			checks := 0
			installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
				if command != "services" {
					checks++
					if !afterSave || checks == 2 {
						return app.ServiceCatalog{}, nil
					}
				}
				return app.ServiceCatalog{Services: []app.DiscoveredService{service}}, nil
			})
			var out bytes.Buffer
			err := configureRule(t.Context(), d, "forward", []string{"--name", "chosen", "--confirm"}, strings.NewReader("1\n"), &out)
			if err == nil || !strings.Contains(err.Error(), "selected share changed") {
				t.Fatal(err)
			}
			if afterSave {
				if !reflect.DeepEqual(fake.actions(), []string{"save"}) || !strings.Contains(err.Error(), "saved disabled") {
					t.Fatal(err, fake.actions())
				}
			} else if len(fake.actions()) != 0 {
				t.Fatal(fake.actions())
			}
		})
	}
}

func TestServicePickerEditsLocalPortOrExplicitlyChangesToManual(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			allowWizardPorts(t, nil)
			d := saveRules(t)
			fake := newFake(t, d)
			fake.peers = append(fake.peers, policy.Peer{ID: "node-other", DNSName: "other.example.ts.net"})
			service := sharedService()
			checks := 0
			installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
				if command != "services" {
					checks++
				}
				return app.ServiceCatalog{Services: []app.DiscoveredService{service}}, nil
			})
			input := "1\nu\ne\n18123\ny\n"
			if manual {
				input = "1\nm\n1\ncustom\n9999\nu\ncustom\n8888\ny\n"
			}
			var out bytes.Buffer
			if err := configureRule(t.Context(), d, "forward", []string{"--name", "chosen", "--save-only"}, strings.NewReader(input), &out); err != nil {
				t.Fatal(err, out.String())
			}
			c, _ := config.Load(d)
			r := c.Rules[0]
			if manual {
				if r.PeerID != "node-other" || r.TargetPort != 8888 || checks != 0 {
					t.Fatal(r, checks)
				}
			} else if r.TargetPort != service.Port || r.ListenPort != 18123 || checks != 1 {
				t.Fatal(r, checks)
			}
		})
	}
}

func TestServicePickerRejectsStaleExpiredOrMalformedObservations(t *testing.T) {
	now := time.Now()
	base := sharedService()
	if !selectableService(base, now) {
		t.Fatal("fresh selection rejected")
	}
	for _, change := range []func(*app.DiscoveredService){
		func(s *app.DiscoveredService) { s.CheckedAt = now.Add(-app.DiscoveryTTL - time.Second) },
		func(s *app.DiscoveredService) { s.CheckedAt = now.Add(time.Minute) },
		func(s *app.DiscoveredService) { s.ExpiresAt = now },
		func(s *app.DiscoveredService) { s.PeerID = "" },
		func(s *app.DiscoveredService) { s.ID = "short" },
		func(s *app.DiscoveredService) { s.ID = strings.ToUpper(s.ID) },
		func(s *app.DiscoveredService) { s.PeerHost = "bad host" },
		func(s *app.DiscoveredService) { s.Port = 0 },
		func(s *app.DiscoveredService) { s.Purpose = "arbitrary metadata" },
		func(s *app.DiscoveredService) { s.Network = "icmp" },
		func(s *app.DiscoveredService) { s.Application = "verified" },
	} {
		s := base
		change(&s)
		if selectableService(s, now) {
			t.Fatal(s)
		}
	}
}

func TestShareDiscoveryIsDisclosedAndScriptsRequireOptIn(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
		input string
		want  bool
	}{
		{"interactive", nil, "y\n", true},
		{"interactive-disabled", []string{"--no-discovery"}, "y\n", false},
		{"script-default", []string{"--confirm"}, "", false},
		{"script-opt-in", []string{"--confirm", "--discoverable"}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := saveRules(t)
			fake := newFake(t, d)
			var out bytes.Buffer
			args := append([]string{"--peer", "server", "--purpose", "web", "--name", "share", "--port", "8123", "--save-only"}, test.flags...)
			if err := configureRule(t.Context(), d, "share", args, strings.NewReader(test.input), &out); err != nil {
				t.Fatal(err)
			}
			c, _ := config.Load(d)
			if len(c.Rules) != 1 || c.Rules[0].Discoverable != test.want || !reflect.DeepEqual(fake.actions(), []string{"save"}) {
				t.Fatal(c, fake.actions())
			}
			if test.want && !strings.Contains(out.String(), "only allowed peers may read purpose, protocol, shared port and expiry") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestDiscoveryJapaneseOutputPreservesServiceValues(t *testing.T) {
	var out bytes.Buffer
	service := sharedService()
	printServiceCatalog(&localeWriter{out: &out, language: "ja"}, app.ServiceCatalog{Services: []app.DiscoveredService{service}})
	for _, want := range []string{"このノード向け", "確認時刻", "server.example.ts.net", "web", "tcp:8123", "アプリの動作は未確認"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
	for _, source := range []string{"selected share changed, expired or is no longer available to this node; refresh and select again", "Choose service number, r refresh, m manual, or q cancel: "} {
		if !hasJapanese(japaneseText(source)) {
			t.Fatal(source)
		}
	}
}

func TestServiceRevalidationRejectsAnyChangedScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*app.DiscoveredService)
	}{
		{"token", func(s *app.DiscoveredService) { s.ID = "ffeeddccbbaa99887766554433221100" }},
		{"peer", func(s *app.DiscoveredService) { s.PeerID = "node-other" }},
		{"host", func(s *app.DiscoveredService) { s.PeerHost = "other.example.ts.net" }},
		{"purpose", func(s *app.DiscoveredService) { s.Purpose = "custom" }},
		{"network", func(s *app.DiscoveredService) { s.Network = "udp" }},
		{"port", func(s *app.DiscoveredService) { s.Port++ }},
		{"shortened expiry", func(s *app.DiscoveredService) { s.ExpiresAt = s.ExpiresAt.Add(-time.Minute) }},
		{"expired", func(s *app.DiscoveredService) { s.ExpiresAt = time.Now().Add(-time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := sharedService()
			current := selected
			test.change(&current)
			installRequest(t, func(_ context.Context, _, command string, value any) error {
				if command != "services:node-server" {
					t.Fatal(command)
				}
				return assign(value, app.ServiceCatalog{Services: []app.DiscoveredService{current}})
			})
			if err := revalidateService(t.Context(), "unused", &selected); err == nil {
				t.Fatal("changed scope accepted")
			}
		})
	}
}

func TestDiscoveryJapaneseInteractiveFlowAndManualCompatibility(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	service := sharedService()
	installServices(t, fake, func(string) (app.ServiceCatalog, error) {
		return app.ServiceCatalog{Services: []app.DiscoveredService{service}}, nil
	})
	var out bytes.Buffer
	args := []string{"--lang", "ja", "--state-dir", d, "connect", "--name", "共有接続", "--save-only"}
	if err := run(t.Context(), args, strings.NewReader("1\n編集\n18123\nはい\n"), &out); err != nil {
		t.Fatal(err, out.String())
	}
	for _, value := range []string{"共有接続", "サービスの番号", "ローカルポート", "127.0.0.1:18123", "server.example.ts.net:8123"} {
		if !strings.Contains(out.String(), value) {
			t.Fatal(value, out.String())
		}
	}
	c, _ := config.Load(d)
	if c.Rules[0].TargetPort != 8123 || c.Rules[0].ListenPort != 18123 {
		t.Fatal(c.Rules)
	}
}

func TestShareDiscoveryFlagsConflictAndDoNotChangeSavedStarts(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	for _, args := range [][]string{{"--discoverable", "--no-discovery"}, {"--discoverable", "saved"}, {"--no-discovery", "saved"}} {
		var out bytes.Buffer
		if err := configureRule(t.Context(), d, "share", args, strings.NewReader(""), &out); err == nil {
			t.Fatal(args)
		}
	}
	if len(fake.actions()) != 0 {
		t.Fatal(fake.actions())
	}
}

func TestDiscoveryUnavailableWarningIsLocalizedAndJSONStable(t *testing.T) {
	status := app.Status{State: "ready", Mode: "rules", Discovery: "unavailable"}
	var english, japanese, machine bytes.Buffer
	if err := printStatus(&english, status, false); err != nil {
		t.Fatal(err)
	}
	if err := printStatus(&localeWriter{out: &japanese, language: "ja"}, status, false); err != nil {
		t.Fatal(err)
	}
	if err := printStatus(&localeWriter{out: &machine, language: "ja", machine: true}, status, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(english.String(), "Sharing may still work with manual configuration") || !strings.Contains(japanese.String(), "手動設定なら共有") || !strings.Contains(machine.String(), `"discovery":"unavailable"`) {
		t.Fatal(english.String(), japanese.String(), machine.String())
	}
}

func TestServiceRevalidationAcceptsSameGrantLeaseRenewal(t *testing.T) {
	selected := sharedService()
	renewed := selected
	renewed.ExpiresAt = selected.ExpiresAt.Add(time.Minute)
	renewed.CheckedAt = time.Now()
	installRequest(t, func(_ context.Context, _, command string, value any) error {
		if command != "services:node-server" {
			t.Fatal(command)
		}
		return assign(value, app.ServiceCatalog{Services: []app.DiscoveredService{renewed}})
	})
	if err := revalidateService(t.Context(), "unused", &selected); err != nil {
		t.Fatal(err)
	}
	if !selected.ExpiresAt.Equal(renewed.ExpiresAt) || !selected.CheckedAt.Equal(renewed.CheckedAt) {
		t.Fatal("renewed observation not retained", selected)
	}
}
