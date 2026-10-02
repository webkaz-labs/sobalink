package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testEditor() *terminalEditor {
	return newTerminalEditor("名前: ", nil, "", func(s string) string { return s })
}
func editorKey(m *terminalEditor, key string) {
	k := tea.KeyPressMsg{}
	switch key {
	case "left":
		k.Code = tea.KeyLeft
	case "right":
		k.Code = tea.KeyRight
	case "home":
		k.Code = tea.KeyHome
	case "end":
		k.Code = tea.KeyEnd
	case "delete":
		k.Code = tea.KeyDelete
	case "backspace":
		k.Code = tea.KeyBackspace
	case "up":
		k.Code = tea.KeyUp
	case "down":
		k.Code = tea.KeyDown
	case "enter":
		k.Code = tea.KeyEnter
	case "esc":
		k.Code = tea.KeyEscape
	case "ctrl+c":
		k.Code = 'c'
		k.Mod = tea.ModCtrl
	case "ctrl+d":
		k.Code = 'd'
		k.Mod = tea.ModCtrl
	default:
		k.Text = key
		k.Code = tea.KeyExtended
	}
	m.Update(k)
}
func TestTerminalEditorGraphemeEditing(t *testing.T) {
	for _, value := range []string{"日本語", "e\u0301", "👨‍👩‍👧‍👦", "🇯🇵", "か\u3099"} {
		t.Run(value, func(t *testing.T) {
			m := testEditor()
			m.insert("a" + value + "z")
			editorKey(m, "left")
			before := previousGrapheme(m.value, m.cursor)
			want := m.value[:before] + "z"
			editorKey(m, "backspace")
			if m.value != want {
				t.Fatalf("backspace=%q, want %q", m.value, want)
			}
			m = testEditor()
			m.insert(value)
			editorKey(m, "home")
			editorKey(m, "delete")
			if value != "日本語" && m.value != "" {
				t.Fatalf("delete split grapheme: %q", m.value)
			}
		})
	}
	m := testEditor()
	m.insert("東京")
	editorKey(m, "left")
	m.insert("日本")
	if m.value != "東日本京" {
		t.Fatal(m.value)
	}
	editorKey(m, "home")
	m.insert("前")
	editorKey(m, "end")
	m.insert("後")
	if m.value != "前東日本京後" {
		t.Fatal(m.value)
	}
}
func TestTerminalEditorSeparateCombiningCommit(t *testing.T) {
	m := testEditor()
	m.insert("e")
	m.insert("\u0301")
	editorKey(m, "left")
	if m.cursor != 0 {
		t.Fatalf("cursor split committed grapheme: %d", m.cursor)
	}
	editorKey(m, "delete")
	if m.value != "" {
		t.Fatal(m.value)
	}
}
func TestTerminalEditorChoiceAndTypedFallback(t *testing.T) {
	choices := []promptChoice{{"web", "Web"}, {"ssh", "SSH"}, {"q", "Cancel"}}
	m := newTerminalEditor("Purpose", choices, "", func(s string) string { return s })
	editorKey(m, "enter")
	if m.value != "" || m.selected != -1 {
		t.Fatal("initial Enter selected a choice")
	}
	m = newTerminalEditor("Purpose", choices, "web", func(s string) string { return s })
	editorKey(m, "db")
	if m.value != "db" {
		t.Fatalf("typed answer appended to default: %q", m.value)
	}
	editorKey(m, "down")
	if m.value != "web" {
		t.Fatal(m.value)
	}
	editorKey(m, "down")
	if m.value != "ssh" {
		t.Fatal(m.value)
	}
	editorKey(m, "up")
	if m.value != "web" {
		t.Fatal(m.value)
	}
	editorKey(m, "up")
	if m.value != "q" {
		t.Fatal(m.value)
	}
	editorKey(m, "1,2")
	if m.value != "1,2" {
		t.Fatal(m.value)
	}
	editorKey(m, "left")
	editorKey(m, "x")
	if m.value != "1,x2" {
		t.Fatal(m.value)
	}
}
func TestTerminalEditorPasteIsAtomicAndBounded(t *testing.T) {
	for _, paste := range []string{"a\ny\n", "a\r\ny", "a\tb", "\x1b[31m", "\x03", "\xff", strings.Repeat("x", maxPromptBytes+1)} {
		m := testEditor()
		m.insert("safe")
		m.Update(tea.PasteMsg{Content: paste})
		if m.value != "safe" || m.done || m.notice == "" {
			t.Fatalf("unsafe paste accepted: %q", m.value)
		}
	}
	m := testEditor()
	m.Update(tea.PasteMsg{Content: strings.Repeat("日", maxPromptBytes/3)})
	if len(m.value) != maxPromptBytes/3*3 {
		t.Fatal("valid bounded paste rejected")
	}
}
func TestTerminalEditorCancellationAndEOF(t *testing.T) {
	for _, key := range []string{"esc", "ctrl+c", "ctrl+d"} {
		m := testEditor()
		editorKey(m, key)
		if !m.done || m.err == nil {
			t.Fatalf("%s did not cancel", key)
		}
	}
	m := testEditor()
	m.insert("abc")
	editorKey(m, "home")
	editorKey(m, "ctrl+d")
	if m.done || m.value != "bc" {
		t.Fatal("Ctrl+D did not delete")
	}
	editorKey(m, "end")
	editorKey(m, "ctrl+d")
	if m.done || m.value != "bc" {
		t.Fatal("Ctrl+D at end changed nonempty input")
	}
}
func TestTerminalEditorNarrowCells(t *testing.T) {
	for _, width := range []int{1, 2, 3, 4, 8, 20, 80} {
		for _, height := range []int{1, 2, 3, 8} {
			m := testEditor()
			m.width = width
			m.height = height
			m.insert(strings.Repeat("日e\u0301👨‍👩‍👧‍👦", 4))
			for i := 0; i < 15; i++ {
				view := m.View()
				lines := strings.Split(view.Content, "\n")
				if len(lines) > height {
					t.Fatalf("%dx%d: %d rows", width, height, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > max(1, width-1) {
						t.Fatalf("width %d: %q = %d cells", width, line, ansi.StringWidth(line))
					}
				}
				if view.Cursor.X < 0 || view.Cursor.X >= max(1, width) || view.Cursor.Y < 0 || view.Cursor.Y >= height {
					t.Fatalf("bad cursor: %+v", view.Cursor)
				}
				editorKey(m, "left")
			}
		}
	}
}
func TestTerminalEditorLocalePreservesValues(t *testing.T) {
	p := newPrompts(strings.NewReader(""), &localeWriter{out: io.Discard, language: "ja"})
	m := newTerminalEditor(p.promptText("Purpose [web] (back to peers, q to cancel): "), []promptChoice{{"1", "1. Database (db)"}}, "", p.promptText)
	m.insert("Database (db)")
	if !strings.Contains(m.View().Content, "Database (db)") {
		t.Fatal("user text translated")
	}
	if strings.Contains(m.View().Content, "Left/Right") {
		t.Fatal("hint untranslated")
	}
}
func TestTerminalChoicePipeKeepsPlainScanner(t *testing.T) {
	var out bytes.Buffer
	p := newPrompts(strings.NewReader("2,4\n"), &out)
	answer, err := p.askChoice("Choose: ", []promptChoice{{"1", "One"}}, "1")
	if err != nil || answer != "2,4" || out.String() != "Choose: " {
		t.Fatalf("pipe changed: %q %q %v", answer, out.String(), err)
	}
}

type testCancelReader struct{ io.Reader }

func (testCancelReader) Cancel() bool { return true }
func (testCancelReader) Close() error { return nil }
func TestTerminalReadStreamBoundsAndUTF8(t *testing.T) {
	input := &terminalReadStream{reader: testCancelReader{strings.NewReader(strings.Repeat("x", maxPromptStreamBytes+10))}, remaining: maxPromptStreamBytes}
	got, err := io.ReadAll(input)
	if !errors.Is(err, errPromptStreamLimit) || len(got) != maxPromptStreamBytes {
		t.Fatalf("bound=%d %v", len(got), err)
	}
	bad := &terminalReadStream{reader: testCancelReader{strings.NewReader("a\xffb")}, remaining: 10}
	if _, err := bad.Read(make([]byte, 10)); err == nil {
		t.Fatal("invalid UTF8 accepted before decoder")
	}
	stopped := &terminalReadStream{reader: testCancelReader{strings.NewReader("yes")}, remaining: 10}
	if err := stopped.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stopped.Read(make([]byte, 10)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
func TestTerminalReadStreamSplitUTF8(t *testing.T) {
	text := "日本e\u0301👨‍👩‍👧‍👦\x1b[A"
	stream := &terminalReadStream{reader: testCancelReader{&oneByteReader{[]byte(text)}}, remaining: 1024}
	got, err := io.ReadAll(stream)
	if !errors.Is(err, errPromptCanceled) || string(got) != text {
		t.Fatalf("split UTF8=%q %v", got, err)
	}
}
func TestTerminalChoiceDefaultShowsNavigationHint(t *testing.T) {
	m := newTerminalEditor("Purpose", []promptChoice{{"web", "Web"}}, "web", func(s string) string { return s })
	if !strings.Contains(m.View().Content, "Up/Down select") {
		t.Fatal("selected default hid navigation help")
	}
}

func TestTerminalEditorAcceptedAnswerCannotChange(t *testing.T) {
	m := newTerminalEditor("Answer", []promptChoice{{"other", "Other"}}, "", func(s string) string { return s })
	m.insert("reviewed")
	editorKey(m, "enter")
	editorKey(m, "changed")
	m.Update(tea.PasteMsg{Content: "later"})
	editorKey(m, "down")
	editorKey(m, "up")
	if m.value != "reviewed" {
		t.Fatalf("input after Enter changed accepted value: %q", m.value)
	}
}

func TestTerminalEditorWidthFollowsNegotiatedMode(t *testing.T) {
	m := testEditor()
	m.insert("a👨‍👩‍👧‍👦b")
	view := m.View()
	if view.Cursor.X != 2+ansi.WcWidth.StringWidth(m.value) {
		t.Fatalf("legacy width mismatch: %d", view.Cursor.X)
	}
	m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
	view = m.View()
	if m.widthMethod != ansi.GraphemeWidth || view.Cursor.X != 2+ansi.GraphemeWidth.StringWidth(m.value) {
		t.Fatalf("grapheme width mismatch: %d", view.Cursor.X)
	}
}

func TestTerminalReadStreamReplacementCharacterFailsClosed(t *testing.T) {
	for _, reader := range []io.Reader{strings.NewReader("before\ufffdafter"), &oneByteReader{[]byte("before\ufffdafter")}} {
		stream := &terminalReadStream{reader: testCancelReader{reader}, remaining: 1024}
		_, err := io.ReadAll(stream)
		if !errors.Is(err, errPromptReplacement) {
			t.Fatalf("replacement character was silently altered: %v", err)
		}
		if strings.Contains(err.Error(), "not valid UTF-8") {
			t.Fatal("valid replacement character called invalid UTF-8")
		}
	}
}
