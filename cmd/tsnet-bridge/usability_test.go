package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/app"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHelpRoutesSucceedWithoutStateOrNetwork(t *testing.T) {
	for _, args := range [][]string{{"help", "connect"}, {"connect", "--help"}, {"group", "--help"}, {"status", "--help"}, {"--state-dir", t.TempDir(), "--help"}, {"login", "--help"}} {
		var out bytes.Buffer
		if e := run(t.Context(), args, strings.NewReader(""), &out); e != nil {
			t.Fatal(args, e)
		}
		if out.Len() == 0 || strings.Contains(out.String(), "Usage of forward") {
			t.Fatal(args, out.String())
		}
	}
	var out bytes.Buffer
	run(t.Context(), []string{"connect", "--help"}, strings.NewReader(""), &out)
	if strings.Contains(out.String(), "--loopback") {
		t.Fatal("share-only option in connect help")
	}
}
func TestWizardRetriesEditsAndCancelsWithoutSideEffects(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	var out bytes.Buffer
	// Invalid peer, valid peer, invalid purpose, default purpose, edited service,
	// default name, edit review, service/listen changes, and final approval.
	input := "0\n1\nwrong\n\n8123\n\ne\n8124\n18124\ny\n"
	if e := configureRule(t.Context(), d, "forward", []string{"--manual", "--save-only"}, strings.NewReader(input), &out); e != nil {
		t.Fatal(e, out.String())
	}
	c, e := config.Load(d)
	if e != nil || len(c.Rules) != 1 || c.Rules[0].TargetPort != 8124 || c.Rules[0].ListenPort != 18124 {
		t.Fatal(c, e)
	}
	if len(fake.actions()) != 1 || fake.actions()[0] != "save" {
		t.Fatal(fake.actions())
	}
	d = saveRules(t)
	fake = newFake(t, d)
	out.Reset()
	if e = configureRule(t.Context(), d, "forward", nil, strings.NewReader("q\n"), &out); e == nil || len(fake.actions()) != 0 {
		t.Fatal("cancel changed state")
	}
}
func TestQRNarrowTerminalOffersLinkInsteadOfWrappedCode(t *testing.T) {
	old := loginTerminalWidth
	defer func() { loginTerminalWidth = old }()
	loginTerminalWidth = func(io.Writer) (int, error) { return 40, nil }
	var out bytes.Buffer
	e := renderLoginQR(&out, sampleAuthURL, "small")
	if e == nil || out.Len() != 0 || !strings.Contains(e.Error(), "Widen") {
		t.Fatal("wrapped QR rendered", e)
	}
	loginTerminalWidth = func(io.Writer) (int, error) { return 80, nil }
	if renderLoginQR(&out, sampleAuthURL, "large") == nil || out.Len() != 0 {
		t.Fatal("wide QR rendered into80 columns")
	}
}
func TestIdenticalActiveShareNeedsNoConfirmationOrMutation(t *testing.T) {
	r := testRule("api")
	r.Direction = "share"
	r.PeerID = ""
	r.TargetHost = "127.0.0.1"
	r.AllowedPeers = []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}
	d := saveRules(t, r)
	status := app.Status{Mode: "rules", State: "ready", Rules: []app.RuleStatus{{Name: r.Name, State: "ready", ScopeDigest: config.RulesDigest([]config.Rule{r}), TTLSeconds: 60, ExpiresAt: time.Now().Add(time.Minute)}}}
	installRequest(t, func(_ context.Context, _, command string, v any) error {
		if command != "status" {
			t.Fatal("idempotent request mutated state", command)
		}
		return assign(v, status)
	})
	var out bytes.Buffer
	if e := namedAction(t.Context(), d, "start", []string{"--ttl", "1m", "api"}, strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatal("reconfirmed unchanged share")
	}
	status.Rules[0].ExpiresAt = time.Now().Add(-time.Second)
	if alreadyActive(status, []config.Rule{r}, "", 60) {
		t.Fatal("expired grant treated active")
	}
}
func TestEmptyListsAndInitKeepSelectedProfile(t *testing.T) {
	d := t.TempDir()
	var out bytes.Buffer
	if e := initRules(d, nil, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), commandPrefix(d)+" login") {
		t.Fatal("profile lost in next action")
	}
	out.Reset()
	if e := initRules(d, nil, &out); e == nil || strings.Contains(e.Error(), "migrate") {
		t.Fatal(e)
	}
	out.Reset()
	if e := listRules(t.Context(), d, nil, &out, false); e != nil || !strings.Contains(out.String(), "No saved connections") {
		t.Fatal(e, out.String())
	}
	out.Reset()
	if e := groupCommand(t.Context(), d, []string{"list"}, strings.NewReader(""), &out); e != nil || !strings.Contains(out.String(), "No saved groups") {
		t.Fatal(e, out.String())
	}
}

func TestJapaneseConfirmationAndCancellationInput(t *testing.T) {
	if !affirmativeInput("はい") || affirmativeInput("いいえ") {
		t.Fatal("Japanese consent input")
	}
	if !cancelInput("キャンセル") || !cancelInput("取消") {
		t.Fatal("Japanese cancellation input")
	}
	var out bytes.Buffer
	if e := newPrompts(strings.NewReader("はい\n"), &out).confirm("Confirm?", false); e != nil {
		t.Fatal(e)
	}
}

func allowWizardPorts(t *testing.T, fn func(int) error) {
	t.Helper()
	old := checkPort
	checkPort = func(_ context.Context, _ string, port int) error {
		if fn != nil {
			return fn(port)
		}
		return nil
	}
	t.Cleanup(func() { checkPort = old })
}

func TestWizardRetriesServiceNameAndReviewInput(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t, testRule("existing"))
	fake := newFake(t, d)
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "custom", "--listen-port", "18080", "--save-only"}
	input := "bad\n0\n65536\n8123\nbad name\nexisting\nnew-name\nwrong\ny\n"
	if err := configureRule(t.Context(), d, "forward", args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, err := config.Load(d)
	if err != nil || len(c.Rules) != 2 || c.Rules[1].Name != "new-name" || c.Rules[1].TargetPort != 8123 || c.Rules[1].ListenPort != 18080 {
		t.Fatal(c, err)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save"}) {
		t.Fatal(fake.actions())
	}
}

func TestWizardRetriesOccupiedAlternativePort(t *testing.T) {
	allowWizardPorts(t, func(port int) error {
		if port == 2223 {
			return errors.New("in use")
		}
		return nil
	})
	d := saveRules(t)
	fake := newFake(t, d)
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "ssh", "--name", "shell", "--confirm", "--save-only"}
	if err := configureRule(t.Context(), d, "forward", args, strings.NewReader("bad\n22\n2223\n2222\n"), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].ListenPort != 2222 || c.Rules[0].TargetPort != 22 {
		t.Fatal(c)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save"}) {
		t.Fatal(fake.actions())
	}
}

func TestWizardRetriesLifetimeAndEditedPorts(t *testing.T) {
	d := saveRules(t)
	fake := newFake(t, d)
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "web", "--name", "api", "--port", "8123"}
	input := "-1s\n1.5s\nbad\n2h\ne\nwrong\n8124\n0\n18124\n999h\n30m\ny\n"
	if err := configureRule(t.Context(), d, "share", args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].ListenPort != 18124 || c.Rules[0].TargetPort != 8124 {
		t.Fatal(c)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save", "start"}) || fake.commands[1].TTLSeconds != 1800 {
		t.Fatal(fake.commands)
	}
}

func TestWizardReviewEditsPeerPurposeAndNameBeforeSave(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	fake.peers = append(fake.peers, policy.Peer{ID: "node-zulu", DNSName: "zulu.example.ts.net"})
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "web", "--name", "initial", "--port", "8080", "--save-only"}
	input := "p\n2\nu\nssh\n22\nr\nnew-name\ne\n\n2222\ny\n"
	if err := configureRule(t.Context(), d, "forward", args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 {
		t.Fatal(c)
	}
	r := c.Rules[0]
	if r.Name != "new-name" || r.Purpose != "ssh" || r.PeerID != "node-zulu" || r.TargetHost != "zulu.example.ts.net" || r.TargetPort != 22 || r.ListenPort != 2222 {
		t.Fatal(r)
	}
	if !reflect.DeepEqual(fake.actions(), []string{"save"}) {
		t.Fatal(fake.actions())
	}
	if !strings.Contains(out.String(), "Name: new-name\nPurpose: ssh") || !strings.Contains(out.String(), "Pinned peer: node-zulu") {
		t.Fatal(out.String())
	}
}

func TestWizardReviewAndPurposeBackPreserveSelectedIdentity(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	fake.peers = append(fake.peers, policy.Peer{ID: "node-zulu", DNSName: "zulu.example.ts.net"})
	var out bytes.Buffer
	args := []string{"--peer", "server", "--purpose", "web", "--name", "initial", "--port", "8080", "--save-only"}
	input := "back\n2\nu\n戻る\n1\nai\n11434\ny\n"
	if err := configureRule(t.Context(), d, "forward", args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].PeerID != "node-server" || c.Rules[0].Purpose != "ai" || c.Rules[0].TargetPort != 11434 || c.Rules[0].ListenPort != 8080 {
		t.Fatal(c)
	}
}

func TestWizardCancellationAtEveryPromptNeverSaves(t *testing.T) {
	for _, test := range []struct {
		name, direction, input string
		args                   []string
	}{
		{"service", "forward", "q\n", []string{"--peer", "server", "--purpose", "custom", "--name", "api", "--save-only", "--confirm"}},
		{"name-with-confirm", "forward", "q\n", []string{"--peer", "server", "--purpose", "web", "--save-only", "--confirm"}},
		{"name-japanese", "forward", "キャンセル\n", []string{"--peer", "server", "--purpose", "web", "--save-only", "--confirm"}},
		{"local-port", "forward", "cancel\n", []string{"--peer", "server", "--purpose", "ssh", "--name", "api", "--save-only", "--confirm"}},
		{"lifetime", "share", "取消\n", []string{"--peer", "server", "--purpose", "web", "--name", "api", "--confirm"}},
		{"review", "forward", "q\n", []string{"--peer", "server", "--purpose", "web", "--name", "api", "--port", "8080", "--save-only"}},
		{"edit-port", "forward", "e\nq\n", []string{"--peer", "server", "--purpose", "web", "--name", "api", "--port", "8080", "--save-only"}},
		{"edit-name", "forward", "r\nq\n", []string{"--peer", "server", "--purpose", "web", "--name", "api", "--port", "8080", "--save-only"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			allowWizardPorts(t, nil)
			d := saveRules(t)
			fake := newFake(t, d)
			var out bytes.Buffer
			err := configureRule(t.Context(), d, test.direction, test.args, strings.NewReader(test.input), &out)
			if err == nil || !strings.Contains(err.Error(), "canceled") || len(fake.actions()) != 0 {
				t.Fatal(err, fake.actions(), out.String())
			}
			c, _ := config.Load(d)
			if len(c.Rules) != 0 {
				t.Fatal("cancellation saved a rule", c)
			}
		})
	}
}

func TestWizardJapaneseEditsPreserveNamesAndEndpoints(t *testing.T) {
	allowWizardPorts(t, nil)
	d := saveRules(t)
	fake := newFake(t, d)
	fake.peers = append(fake.peers, policy.Peer{ID: "node-zulu", DNSName: "zulu.example.ts.net"})
	var out bytes.Buffer
	args := []string{"--lang", "ja", "--state-dir", d, "connect", "--peer", "server", "--purpose", "web", "--name", "初期", "--port", "8080", "--save-only"}
	input := "相手\n2\n用途\nssh\n22\n名前\n接続_日本語\n編集\n\n2222\nはい\n"
	if err := run(t.Context(), args, strings.NewReader(input), &out); err != nil {
		t.Fatal(err, out.String())
	}
	c, _ := config.Load(d)
	if len(c.Rules) != 1 || c.Rules[0].Name != "接続_日本語" || c.Rules[0].TargetHost != "zulu.example.ts.net" || c.Rules[0].PeerID != "node-zulu" || c.Rules[0].TargetPort != 22 || c.Rules[0].ListenPort != 2222 {
		t.Fatal(c)
	}
	for _, value := range []string{"接続_日本語", "zulu.example.ts.net:22", "node-zulu", "127.0.0.1:2222", "保存"} {
		if !strings.Contains(out.String(), value) {
			t.Fatal("missing unchanged value", value, out.String())
		}
	}
}

func TestWizardEditedShareScopeRequiresFinalConfirmation(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			d := saveRules(t)
			fake := newFake(t, d)
			fake.peers = append(fake.peers, policy.Peer{ID: "node-zulu", DNSName: "zulu.example.ts.net"})
			var out bytes.Buffer
			args := []string{"--peer", "server", "--purpose", "web", "--name", "api", "--port", "8080", "--ttl", "30m"}
			input := "p\n1,2\nq\n"
			if approve {
				input = "p\n1,2\ny\n"
			}
			err := configureRule(t.Context(), d, "share", args, strings.NewReader(input), &out)
			c, _ := config.Load(d)
			if !approve {
				if err == nil || len(c.Rules) != 0 || len(fake.actions()) != 0 {
					t.Fatal(err, c, fake.actions())
				}
				return
			}
			if err != nil || len(c.Rules) != 1 || len(c.Rules[0].AllowedPeers) != 2 {
				t.Fatal(err, c)
			}
			if !reflect.DeepEqual(fake.actions(), []string{"save", "start"}) || fake.commands[1].ExpectedRules != config.RulesDigest(c.Rules) {
				t.Fatal(fake.commands)
			}
			if !strings.Contains(out.String(), "Allowed peer: zulu.example.ts.net (node-zulu)") {
				t.Fatal(out.String())
			}
		})
	}
}
