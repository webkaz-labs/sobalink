package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

func TestChoicePickersKeepPlainInputAndPromptBehavior(t *testing.T) {
	peers := []policy.Peer{{ID: "node-alpha", DNSName: "alpha.example.ts.net"}, {ID: "node-beta", DNSName: "beta.example.ts.net"}}
	var out bytes.Buffer
	selected, err := choosePeersPrompt(peers, "", true, newPrompts(strings.NewReader("1,2\n"), &out))
	if err != nil || len(selected) != 2 || selected[0].ID != peers[0].ID || selected[1].ID != peers[1].ID {
		t.Fatal(selected, err)
	}
	want := "1. alpha.example.ts.net\n2. beta.example.ts.net\nChoose peer (number/name; comma-separated for share, q to cancel): "
	if out.String() != want {
		t.Fatalf("plain peer prompt changed: %q", out.String())
	}

	out.Reset()
	purpose, selected, err := choosePurposePrompt(peers, selected, true, "web", newPrompts(strings.NewReader("back\nbeta\n3\n"), &out))
	if err != nil || purpose != "db" || len(selected) != 1 || selected[0].ID != peers[1].ID {
		t.Fatal(purpose, selected, err)
	}
	if strings.Count(out.String(), "Purpose [web] (back to peers, q to cancel): ") != 2 || strings.ContainsAny(out.String(), "\x1b\r") || strings.Contains(out.String(), "Arrow keys") {
		t.Fatal(out.String())
	}

	out.Reset()
	purpose, _, err = choosePurposePrompt(peers, selected, false, "ssh", newPrompts(strings.NewReader("\n"), &out))
	if err != nil || purpose != "ssh" {
		t.Fatal(purpose, err)
	}
}

func TestChoicePickerCancellationKeepsSelectionUnchanged(t *testing.T) {
	selected := []config.PeerRef{{ID: "node-alpha", Host: "alpha.example.ts.net"}}
	for _, input := range []string{"q\n", "cancel\n", "キャンセル\n"} {
		var out bytes.Buffer
		_, got, err := choosePurposePrompt(nil, selected, false, "web", newPrompts(strings.NewReader(input), &out))
		if err == nil || !strings.Contains(err.Error(), "canceled") || !reflect.DeepEqual(got, selected) {
			t.Fatal(got, err)
		}
	}
}

func TestChoiceValuesAndLocalizedLabels(t *testing.T) {
	purposes := purposePromptChoices()
	var values []string
	for _, choice := range purposes {
		values = append(values, choice.Value)
		if !hasJapanese(japaneseText(choice.Label)) {
			t.Fatal("purpose/action label is not localized", choice)
		}
	}
	if !reflect.DeepEqual(values, []string{"web", "ssh", "db", "ai", "custom", "back", "q"}) {
		t.Fatal(values)
	}
	service := sharedService()
	choices := servicePromptChoices(app.ServiceCatalog{Services: []app.DiscoveredService{service}})
	values = nil
	for _, choice := range choices {
		values = append(values, choice.Value)
	}
	if !reflect.DeepEqual(values, []string{"1", "r", "m", "q"}) || !strings.Contains(choices[0].Label, service.PeerHost) || !strings.Contains(choices[0].Label, "tcp:8123") {
		t.Fatal(choices)
	}
	for _, choice := range choices[1:] {
		if !hasJapanese(japaneseText(choice.Label)) {
			t.Fatal("service action label is not localized", choice)
		}
	}
}

func TestServiceSetupHintKeepsProviderAndLocalProfilesSeparate(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			var out bytes.Buffer
			prefix := commandPrefix(t.TempDir())
			writer := &localeWriter{out: &out, language: lang}
			printServiceSetupHint(writer, prefix)
			text := out.String()
			for _, command := range []string{"  tsnet-bridge init\n", "  tsnet-bridge login\n", "  tsnet-bridge share\n", prefix + " connect --manual", "--discoverable", "--confirm"} {
				if !strings.Contains(text, command) {
					t.Fatal(command, text)
				}
			}
			if strings.Contains(text, prefix+" share") || strings.Contains(text, prefix+" login") || strings.Contains(text, prefix+" init") {
				t.Fatal("local profile leaked into provider setup", text)
			}
			if lang == "ja" {
				for _, want := range []string{"提供側", "期限内", "このノードを許可", "相手側の bridge は不要", "init を省略"} {
					if !strings.Contains(text, want) {
						t.Fatal(want, text)
					}
				}
			} else {
				for _, want := range []string{"application and signed-in bridge running", "active, unexpired share allowing this node", "no remote bridge is required", "skip init"} {
					if !strings.Contains(text, want) {
						t.Fatal(want, text)
					}
				}
			}
		})
	}
}

func TestServiceProviderRequirementsAreInBilingualHelp(t *testing.T) {
	for _, topic := range []string{"connect", "share"} {
		for _, lang := range []string{"en", "ja"} {
			var out bytes.Buffer
			if err := commandHelp(topic, &localeWriter{out: &out, language: lang}); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"tsnet-bridge init", "tsnet-bridge login", "tsnet-bridge share"} {
				if !strings.Contains(out.String(), command) {
					t.Fatal(topic, lang, command, out.String())
				}
			}
			if lang == "ja" && (!strings.Contains(out.String(), "提供側") || !strings.Contains(out.String(), "期限内")) {
				t.Fatal(topic, out.String())
			}
			if topic == "connect" {
				if lang == "en" && (!strings.Contains(out.String(), "do not need a remote bridge") || !strings.Contains(out.String(), "does not prove that a peer is offline or lacks a bridge")) {
					t.Fatal(out.String())
				}
				if lang == "ja" && (!strings.Contains(out.String(), "相手側の bridge は不要") || !strings.Contains(out.String(), "オフライン・bridge 未導入とは限りません")) {
					t.Fatal(out.String())
				}
			}
		}
	}
}

func TestPurposeExplainsEveryPresetBeforeSelection(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		for _, protocol := range []string{"tcp", "udp"} {
			var out bytes.Buffer
			p := newPrompts(strings.NewReader("ai\n"), &localeWriter{out: &out, language: lang})
			purpose, _, err := choosePurposePrompt(nil, nil, false, "web", p, protocol)
			if err != nil || purpose != "ai" {
				t.Fatal(purpose, err)
			}
			text := out.String()
			for _, want := range []string{"8080", "22", "5432", "11434", "HTTP", "SSH/SFTP", "PostgreSQL", "custom", protocol, "--network tcp|udp"} {
				if !strings.Contains(text, want) {
					t.Fatal(lang, protocol, want, text)
				}
			}
			if strings.ContainsAny(text, "\x1b\r") {
				t.Fatal("terminal controls in piped output", text)
			}
			if lang == "en" && (!strings.Contains(text, "local AI API example") || !strings.Contains(text, "editable ports") || !strings.Contains(text, "do not configure applications")) {
				t.Fatal(text)
			}
			if lang == "ja" && (!strings.Contains(text, "ローカル AI API の例") || !strings.Contains(text, "変更できるポート") || !strings.Contains(text, "アプリの設定や通信方式の選択は行いません")) {
				t.Fatal(text)
			}
		}
	}
}

func TestServiceChoiceBlankInputDoesNotSelectOrConfirm(t *testing.T) {
	dir := saveRules(t)
	fake := newFake(t, dir)
	installServices(t, fake, func(string) (app.ServiceCatalog, error) {
		return app.ServiceCatalog{Services: []app.DiscoveredService{sharedService()}}, nil
	})
	var out bytes.Buffer
	err := configureRule(t.Context(), dir, "forward", nil, strings.NewReader("\nq\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "canceled") || len(fake.actions()) != 0 {
		t.Fatal(err, fake.actions())
	}
	if !strings.Contains(out.String(), "Choose a displayed service number") || strings.ContainsAny(out.String(), "\x1b\r") {
		t.Fatal(out.String())
	}
}

func TestEmptyServiceChoiceShowsProviderHintOnceAcrossRefresh(t *testing.T) {
	dir := saveRules(t)
	fake := newFake(t, dir)
	calls := 0
	installServices(t, fake, func(string) (app.ServiceCatalog, error) {
		calls++
		return app.ServiceCatalog{}, nil
	})
	var out bytes.Buffer
	err := configureRule(t.Context(), dir, "forward", nil, strings.NewReader("r\nq\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "canceled") || calls != 2 || len(fake.actions()) != 0 {
		t.Fatal(err, calls, fake.actions())
	}
	if strings.Count(out.String(), "Provider setup (skip init if a profile exists):") != 1 || !strings.Contains(out.String(), commandPrefix(dir)+" connect --manual") {
		t.Fatal(out.String())
	}
}

// Delay the first answer without wall-clock sleeps so the observation ages while
// the user reads the menu. Later answers stay separate from Scanner buffering.
type delayedChoiceReader struct {
	first bool
	input io.Reader
}

func (r *delayedChoiceReader) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		time.Sleep(app.DiscoveryTTL + time.Second)
		return copy(p, "1\n"), nil
	}
	return r.input.Read(p)
}

func TestAgedServiceChoiceRechecksOnlyTheSameGrant(t *testing.T) {
	for _, scenario := range []string{"unchanged", "renewed", "changed-port", "changed-peer", "changed-host", "changed-purpose", "changed-protocol", "new-grant", "shortened", "stale-response", "denied", "unavailable", "timeout", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := saveRules(t)
				fake := newFake(t, dir)
				original := sharedService()
				if scenario == "renewed" || scenario == "expired" {
					original.ExpiresAt = time.Now().Add(5 * time.Second)
				}
				var commands []string
				installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
					commands = append(commands, command)
					if len(commands) == 1 {
						return app.ServiceCatalog{Services: []app.DiscoveredService{original}}, nil
					}
					fresh := original
					fresh.CheckedAt = time.Now()
					switch scenario {
					case "renewed":
						fresh.ExpiresAt = time.Now().Add(time.Hour)
					case "changed-port":
						fresh.Port++
					case "changed-peer":
						fresh.PeerID = "node-other"
					case "changed-host":
						fresh.PeerHost = "other.example.ts.net"
					case "changed-purpose":
						fresh.Purpose = "custom"
					case "changed-protocol":
						fresh.Network = "udp"
					case "shortened":
						fresh.ExpiresAt = original.ExpiresAt.Add(-time.Minute)
					case "stale-response":
						fresh.CheckedAt = original.CheckedAt
					case "new-grant":
						fresh.ID = "ffeeddccbbaa99887766554433221100"
					case "denied":
						return app.ServiceCatalog{}, nil
					case "unavailable":
						return app.ServiceCatalog{}, errors.New("unavailable")
					case "timeout":
						return app.ServiceCatalog{}, context.DeadlineExceeded
					}
					return app.ServiceCatalog{Services: []app.DiscoveredService{fresh}}, nil
				})
				var out bytes.Buffer
				input := &delayedChoiceReader{input: strings.NewReader("q\n")}
				chosen, err := chooseServicePrompt(t.Context(), dir, newPrompts(input, &out))
				if scenario == "unchanged" || scenario == "renewed" {
					if err != nil || chosen == nil || !sameDiscoveredService(original, *chosen) || !selectableService(*chosen, time.Now()) {
						t.Fatal(chosen, err)
					}
					if !reflect.DeepEqual(commands, []string{"services", "services:node-server"}) || strings.Contains(out.String(), "choose again") {
						t.Fatal(commands, out.String())
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), "canceled") || chosen != nil {
						t.Fatal(chosen, err)
					}
					if !reflect.DeepEqual(commands, []string{"services", "services:node-server", "services"}) || !strings.Contains(out.String(), "choose again") {
						t.Fatal(commands, out.String())
					}
				}
				if len(fake.actions()) != 0 {
					t.Fatal("selection changed stored state", fake.actions())
				}
			})
		})
	}
}

func TestAgedServiceChoiceContextCancellationStopsRecheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := saveRules(t)
		fake := newFake(t, dir)
		service := sharedService()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var commands []string
		installServices(t, fake, func(command string) (app.ServiceCatalog, error) {
			commands = append(commands, command)
			if len(commands) == 1 {
				return app.ServiceCatalog{Services: []app.DiscoveredService{service}}, nil
			}
			cancel()
			return app.ServiceCatalog{}, ctx.Err()
		})
		var out bytes.Buffer
		chosen, err := chooseServicePrompt(ctx, dir, newPrompts(&delayedChoiceReader{input: strings.NewReader("1\ny\n")}, &out))
		if !errors.Is(err, context.Canceled) || chosen != nil || !reflect.DeepEqual(commands, []string{"services", "services:node-server"}) || len(fake.actions()) != 0 {
			t.Fatal(chosen, err, commands, fake.actions())
		}
	})
}
