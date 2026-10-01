package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// prepareLocale localizes human presentation only. Wire/JSON contracts and
// command spelling remain unchanged. --lang is a global flag, before COMMAND.
func prepareLocale(args []string, out io.Writer) ([]string, io.Writer, func(error) error, error) {
	cleaned, choice, explicit, err := localeArgs(args)
	if err != nil {
		return nil, out, func(e error) error { return e }, err
	}
	if !explicit {
		choice = os.Getenv("TSNET_BRIDGE_LANG")
		if choice == "" {
			choice = "auto"
		}
	}
	lang, err := chooseLocale(choice, os.Getenv, nativePreferredLanguage)
	if err != nil {
		return nil, out, func(e error) error { return e }, err
	}
	translate := func(err error) error {
		if err == nil || lang != "ja" {
			return err
		}
		message := japaneseText(err.Error())
		if message == err.Error() {
			return err
		}
		return &localizedError{message: message, cause: err}
	}
	if lang != "ja" {
		return cleaned, out, translate, nil
	}
	return cleaned, &localeWriter{out: out, language: lang, machine: machineOutput(cleaned)}, translate, nil
}
func localeArgs(args []string) ([]string, string, bool, error) {
	var cleaned []string
	choice := "auto"
	explicit := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			cleaned = append(cleaned, args[i:]...)
			break
		}
		if a == "--lang" || strings.HasPrefix(a, "--lang=") {
			value := ""
			if a == "--lang" {
				i++
				if i >= len(args) {
					return nil, "", false, errors.New("--lang requires ja, en or auto / --lang には ja、en、auto を指定してください")
				}
				value = args[i]
			} else {
				value = strings.TrimPrefix(a, "--lang=")
			}
			if value != "ja" && value != "en" && value != "auto" {
				return nil, "", false, errors.New("--lang must be ja, en or auto / --lang は ja、en、auto から選んでください")
			}
			choice = value
			explicit = true
			continue
		}
		cleaned = append(cleaned, a)
		if a == "--state-dir" {
			i++
			if i < len(args) {
				cleaned = append(cleaned, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "--state-dir=") {
			continue
		}
		// Never consume a child's --lang or another command's ordinary argument.
		if !strings.HasPrefix(a, "-") {
			cleaned = append(cleaned, args[i+1:]...)
			break
		}
	}
	return cleaned, choice, explicit, nil
}
func chooseLocale(choice string, getenv func(string) string, native func() string) (string, error) {
	switch choice {
	case "ja", "en":
		return choice, nil
	case "auto":
	default:
		return "", errors.New("TSNET_BRIDGE_LANG must be ja, en or auto / TSNET_BRIDGE_LANG は ja、en、auto から選んでください")
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := getenv(key); value != "" {
			return normalizedLocale(value), nil
		}
	}
	return normalizedLocale(native()), nil
}
func normalizedLocale(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if at := strings.IndexAny(value, ".@"); at >= 0 {
		value = value[:at]
	}
	if value == "ja" || strings.HasPrefix(value, "ja_") || strings.HasPrefix(value, "ja-") {
		return "ja"
	}
	return "en"
}
func machineOutput(args []string) bool {
	machine := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" || a == "help" {
			return false
		}
		if a == "--json" || a == "--json=true" {
			machine = true
		}
	}
	return machine
}

type localizedError struct {
	message string
	cause   error
}

func (e *localizedError) Error() string { return e.message }
func (e *localizedError) Unwrap() error { return e.cause }

type localeWriter struct {
	out      io.Writer
	language string
	machine  bool
	mu       sync.Mutex
}

func (w *localeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	output := p
	if w.language == "ja" && !w.machine && !json.Valid(p) && !strings.Contains(string(p), "\x1b[") {
		output = []byte(japaneseText(string(p)))
	}
	n, err := w.out.Write(output)
	if err != nil {
		return 0, err
	}
	if n != len(output) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

// UnwrapWriter preserves access to the real terminal, and provides a bypass for
// arbitrary child-process output. Its content must never be translated.
func (w *localeWriter) UnwrapWriter() io.Writer { return w.out }
func unwrapLocaleWriter(out io.Writer) io.Writer {
	for depth := 0; depth < 32; depth++ {
		wrapped, ok := out.(interface{ UnwrapWriter() io.Writer })
		if !ok {
			return out
		}
		next := wrapped.UnwrapWriter()
		if next == nil {
			return out
		}
		out = next
	}
	return out
}

type translationPattern struct {
	pattern *regexp.Regexp
	target  string
	verbs   []byte
}

var japanesePatterns = compileTranslations(japaneseCatalog)
var formatVerb = regexp.MustCompile(`%[sdqwv]`)

func compileTranslations(catalog map[string]string) []translationPattern {
	var patterns []translationPattern
	for source, target := range catalog {
		if !formatVerb.MatchString(source) {
			continue
		}
		pieces := formatVerb.FindAllStringIndex(source, -1)
		var re strings.Builder
		re.WriteString("(?s)^")
		end := 0
		var verbs []byte
		for _, loc := range pieces {
			re.WriteString(regexp.QuoteMeta(source[end:loc[0]]))
			verb := source[loc[0]+1]
			verbs = append(verbs, verb)
			if verb == 'd' {
				re.WriteString(`(-?[0-9]+)`)
			} else {
				re.WriteString(`(.*?)`)
			}
			end = loc[1]
		}
		re.WriteString(regexp.QuoteMeta(source[end:]))
		re.WriteString("$")
		patterns = append(patterns, translationPattern{regexp.MustCompile(re.String()), target, verbs})
	}
	sort.Slice(patterns, func(i, j int) bool {
		a, b := patterns[i].pattern.String(), patterns[j].pattern.String()
		if len(a) == len(b) {
			return a < b
		}
		return len(a) > len(b)
	})
	return patterns
}
func japaneseText(text string) string { return japaneseTextDepth(text, 0) }
func japaneseTextDepth(text string, depth int) string {
	if depth > 6 || text == "" || json.Valid([]byte(text)) {
		return text
	}
	if exact, ok := japaneseCatalog[text]; ok {
		return exact
	}
	if strings.HasSuffix(text, "\n") {
		body := strings.TrimSuffix(text, "\n")
		translated := japaneseTextDepth(body, depth+1)
		if translated != body {
			return translated + "\n"
		}
	}
	if rendered, ok := translateStatus(text); ok {
		return rendered
	}
	if strings.HasPrefix(text, "usage: ") {
		return "使い方: " + strings.TrimPrefix(text, "usage: ")
	}
	if strings.HasPrefix(text, "Usage of ") {
		return "使い方: " + strings.TrimPrefix(text, "Usage of ")
	}
	for _, entry := range japanesePatterns {
		match := entry.pattern.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		values := match[1:]
		for i, verb := range entry.verbs {
			if verb == 'w' {
				values[i] = japaneseTextDepth(values[i], depth+1)
			}
		}
		next := 0
		return formatVerb.ReplaceAllStringFunc(entry.target, func(_ string) string {
			if next >= len(values) {
				return ""
			}
			v := values[next]
			next++
			return v
		})
	}
	// Help is a fixed presentation with explicit commands, never arbitrary user
	// content. Translate full known lines only; preserve command/flag examples.
	if strings.HasPrefix(text, "tsnet-bridge: experimental application-scoped tailnet bridge\n") || strings.HasPrefix(text, "Usage: tsnet-bridge ") || strings.HasPrefix(text, "Usage of ") {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if v, ok := japaneseCatalog[line]; ok {
				lines[i] = v
			} else if strings.HasPrefix(line, "Usage: tsnet-bridge ") {
				lines[i] = "使い方: tsnet-bridge " + strings.TrimPrefix(line, "Usage: tsnet-bridge ")
			} else if strings.HasPrefix(line, "Example: ") {
				lines[i] = "例: " + strings.TrimPrefix(line, "Example: ")
			} else if strings.HasPrefix(line, "Optional: ") {
				lines[i] = "追加の指定: " + strings.TrimPrefix(line, "Optional: ")
			}
		}
		return strings.Join(lines, "\n")
	}
	// flag.PrintDefaults writes a full definition at a time. Only its usage text
	// is translated; spellings, type names and defaults are preserved verbatim.
	if strings.HasPrefix(text, "  -") {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			trim := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trim, "-") {
				continue
			}
			prefix := line[:len(line)-len(trim)]
			body, suffix := trim, ""
			if p := strings.LastIndex(body, " (default "); p >= 0 {
				body, suffix = body[:p], body[p:]
			}
			if v, ok := japaneseCatalog[body]; ok {
				lines[i] = prefix + v + suffix
			}
		}
		return strings.Join(lines, "\n")
	}
	return text
}

var stateLine = regexp.MustCompile(`(?s)^([^\n:]+): ([^\n]*)\nTailnet: ([^\n]*)\n?$`)
var ruleStateLine = regexp.MustCompile(`(?s)^([\p{L}\p{N}_-]+) \[(forward|share)/(tcp|udp)\] ([a-z-]+) \(([^\n)]*)\): ([^\n]*)\n  Listen: ([^\n]*)  Target: ([^\n]*)  Peer: ([^\n]*)  Owner: ([^\n]*)\n  Application: ([^\n]*)\n?$`)
var enumsJA = map[string]string{"starting": "起動中", "idle": "待機中", "ready": "接続準備完了", "partial": "一部のみ利用可能", "blocked": "接続を停止中", "failed": "開始に失敗", "stopped": "停止済み", "expired": "期限切れ", "needs-login": "サインインが必要", "approval-required": "参加の承認待ち", "waiting": "待機中", "unverified": "未確認", "forward": "相手のサービスを使う", "share": "この端末のサービスを渡す", "Running": "接続済み", "NeedsLogin": "サインインが必要", "NeedsMachineAuth": "参加の承認待ち", "Stopped": "停止済み", "Starting": "起動中", "NoState": "状態不明", "recovering": "接続を復旧中", "logged-out": "ログアウト済み", "web": "Web（web）", "ssh": "SSH・ファイル転送（ssh）", "db": "データベース（db）", "ai": "AI API（ai）", "custom": "その他（custom）"}

func enumJA(value string) string {
	if ja, ok := enumsJA[value]; ok {
		return ja
	}
	return value
}

var shortRuleLine = regexp.MustCompile(`^([\p{L}\p{N}_-]+): ([a-z-]+) \[(forward|share)/(tcp|udp)\]$`)
var shortReasonLine = regexp.MustCompile(`^  (.*) \(([a-z0-9-]+)\)$`)
var previewNameLines = regexp.MustCompile(`(?s)^Name: ([^\n]+)\nPurpose: ([^\n]+)\nDirection: ([^\n]+)\nNetwork: ([^\n]+)$`)
var failedRuleError = regexp.MustCompile(`^rule ([\p{L}\p{N}_-]+) did not start \(([a-z0-9-]+)\): (.*); inspect status or doctor and retry explicitly$`)
var waitingRuleError = regexp.MustCompile(`^rule ([\p{L}\p{N}_-]+) is ([a-z-]+) \(([a-z0-9-]+)\): (.*)$`)

var autostartPreview = regexp.MustCompile(`(?s)^Autostart (enable|disable) preview\nFile: ([^\n]+)\n(.*)$`)

func translateStatus(text string) (string, bool) {
	if m := autostartPreview.FindStringSubmatch(text); m != nil {
		action := "有効化"
		if m[1] == "disable" {
			action = "解除"
		}
		return fmt.Sprintf("自動起動の%sの確認\nファイル: %s\n%s", action, m[2], japaneseTextDepth(m[3], 1)), true
	}
	if m := shortRuleLine.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("%s: %s [%s/%s]", m[1], enumJA(m[2]), enumJA(m[3]), m[4]), true
	}
	if m := shortReasonLine.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("  %s (%s)", japaneseTextDepth(m[1], 1), m[2]), true
	}
	if m := previewNameLines.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("名前: %s\n用途: %s\n方向: %s\n通信方式: %s", m[1], enumJA(m[2]), enumJA(m[3]), m[4]), true
	}
	if m := failedRuleError.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("接続 %s を開始できませんでした（%s）: %s。status または doctor で確認してから、明示的に再開始してください", m[1], m[2], japaneseTextDepth(m[3], 1)), true
	}
	if m := waitingRuleError.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("接続 %s は %s（%s）: %s", m[1], enumJA(m[2]), m[3], japaneseTextDepth(m[4], 1)), true
	}
	for _, entry := range []struct{ source, target string }{{"Sign-in status: ", "サインインの状態: "}, {"RustDesk screen/control: ", "RustDesk の画面表示・操作: "}} {
		if strings.HasPrefix(text, entry.source) {
			return entry.target + enumJA(strings.TrimPrefix(text, entry.source)), true
		}
	}
	if m := stateLine.FindStringSubmatch(text); m != nil {
		suffix := ""
		if strings.HasSuffix(text, "\n") {
			suffix = "\n"
		}
		return fmt.Sprintf("%s: %s\nTailnet: %s%s", enumJA(m[1]), japaneseTextDepth(m[2], 1), enumJA(m[3]), suffix), true
	}
	if m := ruleStateLine.FindStringSubmatch(text); m != nil {
		suffix := ""
		if strings.HasSuffix(text, "\n") {
			suffix = "\n"
		}
		return fmt.Sprintf("%s [%s/%s] %s (%s): %s\n  待受: %s  接続先: %s  相手: %s  所有者: %s\n  アプリ動作: %s%s", m[1], enumJA(m[2]), m[3], enumJA(m[4]), m[5], japaneseTextDepth(m[6], 1), m[7], m[8], m[9], m[10], enumJA(m[11]), suffix), true
	}
	return "", false
}
