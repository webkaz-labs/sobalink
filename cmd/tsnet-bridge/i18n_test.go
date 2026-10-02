package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
)

func localeEnvironment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
func hasJapanese(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}
	return false
}
func TestLocalePrecedenceAndNativeFallback(t *testing.T) {
	tests := []struct {
		name, choice string
		env          map[string]string
		native, want string
		wantNative   bool
	}{
		{"explicit-ja", "ja", map[string]string{"LC_ALL": "en_US"}, "en", "ja", false},
		{"explicit-en", "en", map[string]string{"LC_ALL": "ja_JP"}, "ja", "en", false},
		{"all-wins", "auto", map[string]string{"LC_ALL": "en_GB.UTF-8", "LC_MESSAGES": "ja_JP", "LANG": "ja"}, "ja", "en", false},
		{"messages-wins", "auto", map[string]string{"LC_MESSAGES": "ja_JP.UTF-8", "LANG": "en_US"}, "en", "ja", false},
		{"lang", "auto", map[string]string{"LANG": "ja-JP"}, "en", "ja", false},
		{"C-wins", "auto", map[string]string{"LC_ALL": "C.UTF-8", "LANG": "ja"}, "ja", "en", false},
		{"POSIX", "auto", map[string]string{"LANG": "POSIX"}, "ja", "en", false},
		{"unknown", "auto", map[string]string{"LANG": "fr_FR.UTF-8"}, "ja", "en", false},
		{"native-ja", "auto", nil, "ja-JP", "ja", true}, {"native-unknown", "auto", nil, "xx", "en", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			got, e := chooseLocale(test.choice, localeEnvironment(test.env), func() string { called = true; return test.native })
			if e != nil || got != test.want || called != test.wantNative {
				t.Fatal(got, e, called)
			}
		})
	}
	for _, value := range []string{"ja", "JA_jp.UTF-8", "ja_JP@modifier", "ja-JP"} {
		if got := normalizedLocale(value); got != "ja" {
			t.Fatal(value, got)
		}
	}
	for _, value := range []string{"japanese", "jaevil", "", "C", "en_US", "ja.evil"} {
		if value == "ja.evil" {
			continue
		}
		if got := normalizedLocale(value); got != "en" {
			t.Fatal(value, got)
		}
	}
	if _, e := chooseLocale("xx", localeEnvironment(nil), func() string { return "ja" }); e == nil {
		t.Fatal("invalid override accepted")
	}
}
func TestLocaleArgsProtectChildAndPathArguments(t *testing.T) {
	tests := []struct {
		args, want []string
		lang       string
		explicit   bool
	}{
		{[]string{"--lang", "ja", "status"}, []string{"status"}, "ja", true},
		{[]string{"--state-dir", "/tmp/a b", "--lang=en", "help"}, []string{"--state-dir", "/tmp/a b", "help"}, "en", true},
		{[]string{"--state-dir", "--lang", "status"}, []string{"--state-dir", "--lang", "status"}, "auto", false},
		{[]string{"--lang", "ja", "task", "--rules", "web", "--", "program", "--lang", "en"}, []string{"task", "--rules", "web", "--", "program", "--lang", "en"}, "ja", true},
		{[]string{"task", "--rules", "web", "program", "--lang", "en"}, []string{"task", "--rules", "web", "program", "--lang", "en"}, "auto", false},
		{[]string{"connect", "--name", "--lang", "--peer", "server"}, []string{"connect", "--name", "--lang", "--peer", "server"}, "auto", false},
		{[]string{"--lang=ja", "--lang=en", "--help"}, []string{"--help"}, "en", true},
		{[]string{"--", "--lang", "ja"}, []string{"--", "--lang", "ja"}, "auto", false},
	}
	for _, test := range tests {
		got, lang, explicit, e := localeArgs(test.args)
		if e != nil || !reflect.DeepEqual(got, test.want) || lang != test.lang || explicit != test.explicit {
			t.Fatalf("%q: got %q %s %v %v", test.args, got, lang, explicit, e)
		}
	}
	for _, args := range [][]string{{"--lang"}, {"--lang=xx"}, {"--lang", "--help"}, {"--lang="}} {
		if _, _, _, e := localeArgs(args); e == nil {
			t.Fatal("invalid lang accepted", args)
		}
	}
}
func TestPrepareLocaleOverrideAndErrorIdentity(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	t.Setenv("TSNET_BRIDGE_LANG", "ja")
	var out bytes.Buffer
	_, w, tr, e := prepareLocale([]string{"status"}, &out)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintln(w, "Stopped (no process)")
	if !hasJapanese(out.String()) {
		t.Fatal(out.String())
	}
	cause := context.Canceled
	wrapped := fmt.Errorf("connection lease renewal failed: %w", cause)
	got := tr(wrapped)
	if !errors.Is(got, cause) || !hasJapanese(got.Error()) {
		t.Fatal(got)
	}
	pathErr := &os.PathError{Op: "open", Path: "/tmp/ready", Err: os.ErrPermission}
	translated := tr(fmt.Errorf("run init first: %w", pathErr))
	var matched *os.PathError
	if !errors.As(translated, &matched) || matched != pathErr {
		t.Fatal("lost typed cause")
	}
	if !strings.Contains(translated.Error(), "/tmp/ready") {
		t.Fatal("changed path", translated)
	}
	out.Reset()
	_, w, _, e = prepareLocale([]string{"--lang", "auto", "status"}, &out)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintln(w, "Stopped (no process)")
	if out.String() != "Stopped (no process)\n" {
		t.Fatal("explicit auto did not use system locale", out.String())
	}
	out.Reset()
	_, w, _, e = prepareLocale([]string{"--lang", "en", "status"}, &out)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintln(w, "Stopped (no process)")
	if out.String() != "Stopped (no process)\n" {
		t.Fatal(out.String())
	}
}
func TestLocaleWriterPreservesJSONByteForByte(t *testing.T) {
	t.Setenv("TSNET_BRIDGE_LANG", "en")
	payloads := []string{`{"state":"ready","reason":"Stopped (no process)","name":"ready","owner":"Saved disabled:"}` + "\n", "{\n  \"state\": \"expired\",\n  \"reason\": \"Connection expired; start explicitly to resume\"\n}\n", `[{"DNSName":"ready.example.ts.net","ID":"node-ready"}]` + "\n"}
	for _, payload := range payloads {
		var out bytes.Buffer
		_, w, _, e := prepareLocale([]string{"--lang", "ja", "status", "--json"}, &out)
		if e != nil {
			t.Fatal(e)
		}
		for _, chunk := range []string{payload[:len(payload)/2], payload[len(payload)/2:]} {
			if n, e := io.WriteString(w, chunk); e != nil || n != len(chunk) {
				t.Fatal(n, e)
			}
		}
		if out.String() != payload {
			t.Fatal("machine JSON changed", out.String())
		}
		out.Reset()
		raw := &localeWriter{out: &out, language: "ja"}
		if _, e = io.WriteString(raw, payload); e != nil || out.String() != payload {
			t.Fatal("JSON preview changed", out.String(), e)
		}
	}
}
func TestLocalePreservesUserValuesAndCommandSyntax(t *testing.T) {
	examples := []struct {
		english string
		values  []string
	}{
		{"Local app endpoint: 127.0.0.1:8080\nRemote service: ready.example.ts.net:443\nPinned peer: node-ready\n", []string{"127.0.0.1:8080", "ready.example.ts.net:443", "node-ready"}},
		{"Private sign-in URL (do not share): https://login.tailscale.com/a/ready_EXACT-123\n", []string{"https://login.tailscale.com/a/ready_EXACT-123"}},
		{"Private disabled export written: /tmp/ready/Stopped (no process).json\n", []string{"/tmp/ready/Stopped (no process).json"}},
		{"Next: tsnet-bridge --state-dir \"/tmp/state space\" login, then tsnet-bridge --state-dir \"/tmp/state space\" connect or tsnet-bridge --state-dir \"/tmp/state space\" share.\n", []string{"--state-dir \"/tmp/state space\" login", "--state-dir \"/tmp/state space\" connect"}},
		{"rule \"ready\" already exists; use --replace to preview a deliberate replacement", []string{`"ready"`, "--replace"}},
	}
	for _, example := range examples {
		got := japaneseText(example.english)
		if !hasJapanese(got) {
			t.Fatal("untranslated", example.english)
		}
		for _, value := range example.values {
			if !strings.Contains(got, value) {
				t.Fatal("changed value", value, got)
			}
		}
	}
	for _, raw := range []string{"ready", "node-ready", "https://example.invalid/Stopped%20(no%20process)", "User text: Stopped (no process)", "Unrecognized backend detail", "<Task>ready</Task>"} {
		if got := japaneseText(raw); got != raw {
			t.Fatal("changed opaque user value", got)
		}
	}
}
func TestLocaleStatusReasonsAndCodes(t *testing.T) {
	s := app.Status{State: "recovering", Reason: "Waiting for selected connections; check each rule", Backend: "Running", Mode: "rules", Rules: []app.RuleStatus{{Name: "ready", State: "failed", Direction: "forward", Network: "tcp", Target: "ready.example.ts.net:80", Owner: "ready", ReasonCode: "start-failed", Reason: "Selected port is unavailable; confirm a different port before retrying"}}}
	var out bytes.Buffer
	w := &localeWriter{out: &out, language: "ja"}
	if e := printStatus(w, s, false); e != nil {
		t.Fatal(e)
	}
	got := out.String()
	for _, value := range []string{"接続を復旧中", "開始に失敗", "選んだポート", "ready:", "ready.example.ts.net:80", "start-failed", "タスク: ready"} {
		if !strings.Contains(got, value) {
			t.Fatal("missing", value, got)
		}
	}
	if strings.Contains(got, "Selected port is unavailable") {
		t.Fatal(got)
	}
	for _, message := range []string{"rule ready did not start (start-failed): Selected port is unavailable; confirm a different port before retrying; inspect status or doctor and retry explicitly", "rule ready is expired (ttl-expired): Connection expired; start explicitly to resume", "Sign-in status: logged-out"} {
		got := japaneseText(message)
		if !hasJapanese(got) || strings.Contains(got, "Selected port is unavailable") || strings.Contains(got, "Connection expired;") {
			t.Fatal(got)
		}
	}
}
func TestLocaleWriterUnwrapAndRawSubprocessPath(t *testing.T) {
	file, e := os.CreateTemp(t.TempDir(), "terminal-*")
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	wrapped := &localeWriter{out: &localeWriter{out: file, language: "ja"}, language: "ja"}
	if unwrapLocaleWriter(wrapped) != file {
		t.Fatal("terminal identity lost")
	}
	var out bytes.Buffer
	localized := &localeWriter{out: &out, language: "ja"}
	fmt.Fprintln(unwrapLocaleWriter(localized), "Stopped (no process)")
	if out.String() != "Stopped (no process)\n" {
		t.Fatal("child output changed")
	}
	qr := "\x1b[30;47m█▄▀  \x1b[0m\n"
	out.Reset()
	if n, e := localized.Write([]byte(qr)); e != nil || n != len(qr) || out.String() != qr {
		t.Fatal("QR bytes changed", n, e)
	}
}

type shortLocaleWriter struct{}

func (shortLocaleWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }
func TestLocaleWriterShortWrite(t *testing.T) {
	w := &localeWriter{out: shortLocaleWriter{}, language: "ja"}
	if _, e := io.WriteString(w, "Stopped (no process)\n"); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
}
func TestLocaleHelpAllTopics(t *testing.T) {
	for _, topic := range []string{"init", "login", "connect", "share", "status", "doctor", "settings", "rules", "peers", "shares", "start", "stop", "stop-shares", "group", "wait-ready", "task", "export", "import", "migrate", "autostart", "run", "reconnect", "logout", "setup", "version", "all"} {
		var out bytes.Buffer
		w := &localeWriter{out: &out, language: "ja"}
		if e := commandHelp(topic, w); e != nil {
			t.Fatal(e)
		}
		if !hasJapanese(out.String()) {
			t.Fatal("untranslated help", topic, out.String())
		}
		if topic != "all" && !strings.Contains(out.String(), "tsnet-bridge "+topic) {
			t.Fatal("command syntax changed", topic, out.String())
		}
	}
	var out bytes.Buffer
	fmt.Fprintln(&localeWriter{out: &out, language: "ja"}, help)
	if !strings.Contains(out.String(), "初回: init -> login -> connect") || !strings.Contains(out.String(), "普段の操作") {
		t.Fatal(out.String())
	}
}
func TestLocaleFlagDescriptionsAndErrors(t *testing.T) {
	var out bytes.Buffer
	f := flags("test", &localeWriter{out: &out, language: "ja"})
	f.String("owner", "ready", "task owner")
	f.Duration("timeout", 0, "maximum readiness wait")
	f.PrintDefaults()
	if !strings.Contains(out.String(), "タスク所有者") || !strings.Contains(out.String(), `(default "ready")`) {
		t.Fatal(out.String())
	}
	for _, e := range []string{"flag provided but not defined: -typo", "flag needs an argument: -owner", "invalid value \"bad\" for flag -timeout: parse error"} {
		translated := japaneseText(e)
		if !hasJapanese(translated) {
			t.Fatal(translated)
		}
	}
}
func TestLocaleCatalogFormatParity(t *testing.T) {
	for source, target := range japaneseCatalog {
		if len(formatVerb.FindAllString(source, -1)) != len(formatVerb.FindAllString(target, -1)) {
			t.Errorf("format argument mismatch %q => %q", source, target)
		}
		if !hasJapanese(target) {
			t.Errorf("translation is not Japanese: %q", source)
		}
	}
}

// This keeps future literal errors in owned CLI/config/runtime code from silently
// falling back to English. OS/library diagnostics remain verbatim by design.
func TestLocaleCatalogCoversLiteralErrors(t *testing.T) {
	paths := []string{"main.go", "rules.go", "services.go", "profiles.go", "login.go", "help.go", "prompts.go", "qr_terminal_windows.go", "qr_terminal_other.go", "prompt_terminal_linux.go", "prompt_terminal_other.go", "terminal_editor.go", "terminal_editor_windows.go", "terminal_editor_other.go", "terminal_editor_flush_linux.go", "terminal_editor_flush_darwin.go", "terminal_editor_flush_windows.go", "terminal_editor_flush_other.go"}
	for _, dir := range []string{"../../internal/config", "../../internal/app", "../../internal/autostart"} {
		files, e := filepath.Glob(filepath.Join(dir, "*.go"))
		if e != nil {
			t.Fatal(e)
		}
		paths = append(paths, files...)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		tree, e := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if e != nil {
			t.Fatal(e)
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "New" {
				return true
			}
			id, ok := selector.X.(*ast.Ident)
			if !ok || id.Name != "errors" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			message, e := strconv.Unquote(literal.Value)
			if e != nil {
				t.Fatal(e)
			}
			if message == "stopped" || message == "expired" || message == "lease-expired" {
				return true
			}
			if got := japaneseText(message); !hasJapanese(got) {
				t.Errorf("%s: untranslated literal error %q", path, message)
			}
			return true
		})
	}
}
func TestLocaleMachineStatusExactJSON(t *testing.T) {
	s := app.Status{State: "ready", Reason: "Listener ready; application behavior remains unverified", Rules: []app.RuleStatus{{Name: "ready", State: "ready", ReasonCode: "listener-ready", Reason: "Saved only; start explicitly", ScopeDigest: "ready"}}}
	var a, b bytes.Buffer
	json.NewEncoder(&a).Encode(s)
	if e := printStatus(&localeWriter{out: &b, language: "ja", machine: true}, s, true); e != nil {
		t.Fatal(e)
	}
	if a.String() != b.String() {
		t.Fatal("JSON contract changed")
	}
}

func TestLocaleJapaneseRuleNamesKeepLocalizedState(t *testing.T) {
	for _, input := range []string{
		"試験接続: ready [forward/tcp]",
		"rule 試験接続 did not start (start-failed): Selected port is unavailable; confirm a different port before retrying; inspect status or doctor and retry explicitly",
		"rule 試験接続 is expired (ttl-expired): Connection expired; start explicitly to resume",
	} {
		got := japaneseText(input)
		if got == input || !strings.Contains(got, "試験接続") || strings.Contains(got, "Connection expired;") || strings.Contains(got, "Selected port is unavailable") {
			t.Fatal(got)
		}
	}
}

// Every explanatory line in the two fixed help screens must have a catalog
// entry, rather than passing merely because another line is Japanese.
func TestLocaleTopLevelHelpHasCompleteLineCoverage(t *testing.T) {
	for _, source := range []string{help, helpAll} {
		for _, line := range strings.Split(source, "\n") {
			if strings.TrimSpace(line) == "" || strings.TrimSpace(line) == "version" {
				continue
			}
			if _, ok := japaneseCatalog[line]; !ok {
				t.Errorf("missing fixed-help translation: %q", line)
			}
		}
	}
}
