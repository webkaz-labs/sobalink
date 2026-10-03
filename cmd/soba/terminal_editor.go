package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

const maxPromptBytes = 16 * 1024

// Bound the upstream decoder as well as the edited value. This budget includes
// navigation and terminal replies, and never resets during one prompt.
const maxPromptStreamBytes = 64 * 1024

var errPromptStreamLimit = errors.New("terminal input limit reached; retry the command with a shorter answer")
var errPromptCanceled = errors.New("canceled; no changes made")
var errPromptInvalidUTF8 = errors.New("terminal input is not valid UTF-8; retry with a UTF-8 terminal")
var errPromptReplacement = errors.New("terminal input contains U+FFFD, which the paste decoder cannot preserve; retype without the replacement character")

type promptChoice struct{ Value, Label string }

func promptTerminalFiles(in io.Reader, out io.Writer) (*os.File, *os.File, bool) {
	input, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, nil, false
	}
	output, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(output.Fd())) {
		return nil, nil, false
	}
	return input, output, true
}
func (p *prompts) choiceTerminal() bool {
	_, _, ok := promptTerminalFiles(p.in, p.out)
	return ok && !strings.EqualFold(os.Getenv("TERM"), "dumb")
}
func (p *prompts) promptText(value string) string {
	if p.ja {
		if translated, ok := terminalJapanese[value]; ok {
			return translated
		}
	}
	return value
}

// The byte budget sits after UV's platform-specific cancel reader, which can
// replace an *os.File on Windows. Only our owned decoder reads this stream;
// processInput's scanner worker never starts. No Fd is exposed to another reader.
type terminalReadStream struct {
	reader    cancelreader.CancelReader
	remaining int
	pending   []byte
	mu        sync.Mutex
	stopped   bool
	active    chan struct{}
}

func (r *terminalReadStream) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return 0, context.Canceled
	}
	active := make(chan struct{})
	r.active = active
	r.mu.Unlock()
	defer func() { r.mu.Lock(); close(active); r.active = nil; r.mu.Unlock() }()
	if r.remaining <= 0 {
		return 0, errPromptStreamLimit
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for {
		n := copy(p, r.pending)
		r.pending = nil
		available := min(len(p)-n, r.remaining)
		if available <= 0 {
			return 0, errPromptStreamLimit
		}
		count, err := r.reader.Read(p[n : n+available])
		r.remaining -= count
		n += count
		if errors.Is(err, io.EOF) {
			err = errPromptCanceled
		}
		if err != nil {
			return 0, err
		}
		valid := 0
		for valid < n {
			if !utf8.FullRune(p[valid:n]) {
				r.pending = append(r.pending, p[valid:n]...)
				break
			}
			value, size := utf8.DecodeRune(p[valid:n])
			if value == utf8.RuneError && size == 1 {
				return 0, errPromptInvalidUTF8
			}
			if value == utf8.RuneError {
				return 0, errPromptReplacement
			}
			valid += size
		}
		if valid > 0 {
			return valid, nil
		}
		if r.remaining <= 0 {
			return 0, errPromptStreamLimit
		}
	}
}
func (r *terminalReadStream) close() error {
	r.mu.Lock()
	r.stopped = true
	active := r.active
	r.mu.Unlock()
	r.reader.Cancel()
	if active != nil {
		select {
		case <-active:
		case <-time.After(time.Second):
			return errors.New("terminal input could not be stopped; exit and retry the command")
		}
	}
	return r.reader.Close()
}

// Bubble Tea's renderer can write asynchronously and does not propagate every
// write error. Capture short/error writes and cancel instead of awaiting input
// for a question the user could not see. Do not log panic values or input.
type terminalOutput struct {
	*os.File
	mu     sync.Mutex
	err    error
	cancel context.CancelFunc
}

func (w *terminalOutput) Write(p []byte) (n int, err error) {
	defer func() {
		if recover() != nil {
			n = 0
			err = errors.New("terminal output failed")
		}
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.mu.Lock()
			if w.err == nil {
				w.err = err
			}
			w.mu.Unlock()
			w.cancel()
		}
	}()
	// The input wrapper hides Fd, so the renderer cannot infer that MakeRaw
	// disabled output newline conversion. Emit explicit CRLF on every OS.
	var encoded strings.Builder
	for i, b := range p {
		if b == '\n' && (i == 0 || p[i-1] != '\r') {
			encoded.WriteByte('\r')
		}
		encoded.WriteByte(b)
	}
	written, writeErr := w.File.Write([]byte(encoded.String()))
	if writeErr != nil {
		return 0, writeErr
	}
	if written != encoded.Len() {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}
func (w *terminalOutput) error() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }

func (p *prompts) readTerminalLine(message string, choices []promptChoice, current string) (string, error) {
	input, output, ok := promptTerminalFiles(p.in, p.out)
	if !ok {
		return "", errors.New("interactive terminal unavailable")
	}
	model := newTerminalEditor(p.promptText(message), choices, current, p.promptText)
	return runTerminalEditor(p.ctx, input, output, model)
}

func runTerminalEditor(parentContext context.Context, input, output *os.File, model *terminalEditor) (answer string, err error) {
	if os.Getenv("TEA_TRACE") != "" {
		return "", errors.New("unset TEA_TRACE before interactive input; terminal input logging is disabled")
	}
	if err = parentContext.Err(); err != nil {
		return "", err
	}
	inputState, err := term.GetState(int(input.Fd()))
	if err != nil {
		return "", err
	}
	outputState, err := term.GetState(int(output.Fd()))
	if err != nil {
		return "", err
	}
	// These guards also cover setup errors before Bubble Tea installs its cleanup.
	defer func() {
		if restoreErr := term.Restore(int(output.Fd()), outputState); err == nil && restoreErr != nil {
			err = restoreErr
		}
		if restoreErr := term.Restore(int(input.Fd()), inputState); err == nil && restoreErr != nil {
			err = restoreErr
		}
		if recover() != nil {
			answer = ""
			err = errors.New("terminal editor failed; no changes made")
		}
	}()
	if _, err = term.MakeRaw(int(input.Fd())); err != nil {
		return "", err
	}
	restoreEncoding, err := prepareEditorEncoding()
	if err != nil {
		return "", err
	}
	defer func() {
		if restoreErr := restoreEncoding(); err == nil && restoreErr != nil {
			answer = ""
			err = restoreErr
		}
	}()
	raw, err := uv.NewCancelReader(input)
	if err != nil {
		return "", err
	}
	stream := &terminalReadStream{reader: raw, remaining: maxPromptStreamBytes}
	closeInput := stream.close
	defer func() {
		if closeErr := closeInput(); err == nil && closeErr != nil {
			answer = ""
			err = closeErr
		}
		if err != nil {
			_ = flushEditorInput(input)
		}
	}()
	ctx, cancel := context.WithCancel(parentContext)
	defer cancel()
	writer := &terminalOutput{File: output, cancel: cancel}
	width, height, err := term.GetSize(int(output.Fd()))
	if err != nil {
		return "", err
	}
	model.width, model.height = max(1, width), max(1, height)
	program := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(writer), tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithoutCatchPanics())
	defer program.Kill()
	broker := startTerminalEventBroker(ctx, stream, program)
	closeInput = broker.close
	result, runErr := program.Run()
	if writeErr := writer.error(); writeErr != nil {
		return "", writeErr
	}
	if err = parentContext.Err(); err != nil {
		return "", err
	}
	if runErr != nil {
		// Keep our actionable input errors localizable instead of burying them
		// in the upstream "program was killed" diagnostic wrapper.
		for _, inputErr := range []error{errPromptStreamLimit, errPromptInvalidUTF8, errPromptReplacement, errPromptCanceled} {
			if errors.Is(runErr, inputErr) {
				return "", inputErr
			}
		}
		return "", runErr
	}
	final, ok := result.(*terminalEditor)
	if !ok {
		return "", errors.New("terminal editor failed; no changes made")
	}
	if final.err != nil {
		return "", final.err
	}
	if !final.done {
		return "", errPromptCanceled
	}
	return final.value, nil
}

// Tea stops consuming its private input channel when it exits, while the UV
// scanner flushes decoded events with unconditional sends. Keep that channel
// owned here: Program.Send stops blocking after Tea exits, so the broker can
// drain every pending event and join the scanner before another prompt starts.
// The channel is unbuffered; no unbounded event queue or input history is kept.
type terminalEventBroker struct {
	stream      *terminalReadStream
	stop        context.CancelFunc
	decoderDone chan struct{}
	brokerDone  chan struct{}
}
type terminalReadErrorMsg struct{ err error }

func startTerminalEventBroker(ctx context.Context, stream *terminalReadStream, program *tea.Program) *terminalEventBroker {
	decoderContext, stop := context.WithCancel(ctx)
	events := make(chan uv.Event)
	broker := &terminalEventBroker{stream: stream, stop: stop, decoderDone: make(chan struct{}), brokerDone: make(chan struct{})}
	go func() {
		defer close(broker.brokerDone)
		for event := range events {
			program.Send(event)
		}
	}()
	go func() {
		defer close(broker.decoderDone)
		defer close(events)
		defer func() {
			if recover() != nil {
				program.Send(terminalReadErrorMsg{errors.New("terminal editor failed; no changes made")})
			}
		}()
		decoder := uv.NewTerminalReader(stream, os.Getenv("TERM"))
		if err := decoder.StreamEvents(decoderContext, events); err != nil && decoderContext.Err() == nil {
			program.Send(terminalReadErrorMsg{err})
		}
	}()
	return broker
}

func (b *terminalEventBroker) close() error {
	b.stop()
	err := b.stream.close()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for _, done := range []<-chan struct{}{b.decoderDone, b.brokerDone} {
		select {
		case <-done:
		case <-deadline.C:
			return errors.New("terminal event reader could not be stopped; exit and retry the command")
		}
	}
	return err
}

type terminalEditor struct {
	message       string
	choices       []promptChoice
	value         string
	cursor        int // UTF-8 byte offset, always at a grapheme boundary
	selected      int
	replace       bool
	width, height int
	widthMethod   ansi.Method
	notice        string
	done          bool
	err           error
	localize      func(string) string
}

func newTerminalEditor(message string, choices []promptChoice, current string, localize func(string) string) *terminalEditor {
	m := &terminalEditor{message: message, choices: choices, selected: -1, width: 80, height: 24, localize: localize}
	if len(current) <= maxPromptBytes && utf8.ValidString(current) && !strings.ContainsFunc(current, unicode.IsControl) {
		m.value = current
		m.cursor = len(current)
		m.replace = current != ""
	}
	for i, c := range choices {
		if current != "" && current == c.Value {
			m.selected = i
			break
		}
	}
	return m
}
func (m *terminalEditor) Init() tea.Cmd {
	// We own the UV reader, so Tea's WithInput(nil) suppresses its automatic
	// queries. Ask only for the display capabilities we consume and restore.
	return tea.Raw(ansi.RequestModeSynchronizedOutput + ansi.RequestModeUnicodeCore)
}
func (m *terminalEditor) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := message.(type) {
	case terminalReadErrorMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset || msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) {
			m.widthMethod = ansi.GraphemeWidth
		}
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.PasteMsg:
		m.insert(msg.Content)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.done = true
			m.err = errPromptCanceled
			return m, tea.Quit
		case "enter", "ctrl+j", "kpenter":
			m.done = true
			return m, tea.Quit
		case "up":
			m.selectChoice(-1)
		case "down":
			m.selectChoice(1)
		case "left", "ctrl+b":
			m.replace = false
			m.cursor = previousGrapheme(m.value, m.cursor)
		case "right", "ctrl+f":
			m.replace = false
			m.cursor = nextGrapheme(m.value, m.cursor)
		case "home", "ctrl+a":
			m.replace = false
			m.cursor = 0
		case "end", "ctrl+e":
			m.replace = false
			m.cursor = len(m.value)
		case "backspace", "ctrl+h":
			m.replace = false
			if m.cursor > 0 {
				before := previousGrapheme(m.value, m.cursor)
				m.value = m.value[:before] + m.value[m.cursor:]
				m.cursor = before
				m.selected = -1
			}
		case "delete", "ctrl+d":
			m.replace = false
			if msg.String() == "ctrl+d" && m.value == "" {
				m.done = true
				m.err = errPromptCanceled
				return m, tea.Quit
			}
			if m.cursor < len(m.value) {
				m.value = m.value[:m.cursor] + m.value[nextGrapheme(m.value, m.cursor):]
				m.selected = -1
			}
		case "ctrl+u":
			m.value = m.value[m.cursor:]
			m.cursor = 0
			m.replace = false
			m.selected = -1
		case "ctrl+k":
			m.value = m.value[:m.cursor]
			m.replace = false
			m.selected = -1
		default:
			if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0 {
				m.insert(msg.Text)
			}
		}
	}
	return m, nil
}
func (m *terminalEditor) selectChoice(direction int) {
	if len(m.choices) == 0 {
		return
	}
	if m.selected < 0 {
		if direction > 0 {
			m.selected = 0
		} else {
			m.selected = len(m.choices) - 1
		}
	} else {
		m.selected = (m.selected + direction + len(m.choices)) % len(m.choices)
	}
	value := m.choices[m.selected].Value
	if !utf8.ValidString(value) || len(value) > maxPromptBytes || strings.ContainsFunc(value, unicode.IsControl) {
		m.selected = -1
		return
	}
	m.value = value
	m.cursor = len(value)
	m.replace = true
	m.notice = ""
}
func (m *terminalEditor) insert(value string) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		m.notice = m.localize("Paste plain single-line text; control characters were not inserted.")
		return
	}
	base, cursor := m.value, m.cursor
	if m.replace {
		base = ""
		cursor = 0
	}
	if len(value) > maxPromptBytes-len(base) {
		m.notice = m.localize("Input is too long (maximum 16 KiB); shorten it and retry.")
		return
	}
	m.value = base[:cursor] + value + base[cursor:]
	// Combining marks and IME commits may join an existing neighboring cluster.
	m.cursor = graphemeAtOrAfter(m.value, cursor+len(value))
	m.replace = false
	m.selected = -1
	m.notice = ""
}
func previousGrapheme(s string, pos int) int {
	previous := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end >= pos {
			return previous
		}
		previous = end
	}
	return previous
}
func nextGrapheme(s string, pos int) int {
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end > pos {
			return end
		}
	}
	return len(s)
}
func graphemeAtOrAfter(s string, pos int) int {
	if pos == 0 {
		return 0
	}
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end >= pos {
			return end
		}
	}
	return len(s)
}
func safeTerminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// Keep the cursor visible without splitting graphemes. A too-wide cluster is
// hidden at a one-cell viewport rather than rendered across the next row.
func terminalViewport(value string, cursor, width int, method ansi.Method) (string, int) {
	width = max(1, width)
	start := cursor
	used := 0
	for start > 0 {
		previous := previousGrapheme(value, start)
		w := method.StringWidth(value[previous:start])
		if used+w >= width {
			break
		}
		used += w
		start = previous
	}
	end := start
	total := 0
	for end < len(value) {
		next := nextGrapheme(value, end)
		w := method.StringWidth(value[end:next])
		if total+w > width {
			break
		}
		total += w
		end = next
	}
	return value[start:end], used
}
func (m *terminalEditor) View() tea.View {
	width := max(1, m.width-1)

	question := m.widthMethod.Hardwrap(safeTerminalText(m.message), width, true)
	lines := strings.Split(question, "\n")
	for i := range lines {
		lines[i] = m.widthMethod.Truncate(lines[i], width, "…")
	}
	reserve := 2
	if len(m.choices) > 0 {
		reserve++
	}
	if m.notice != "" {
		reserve++
	}
	available := max(0, m.height-reserve)
	if len(lines) > available {
		if available == 0 {
			lines = nil
		} else {
			lines = lines[:available]
			lines[len(lines)-1] = m.widthMethod.Truncate(lines[len(lines)-1], max(1, width-1), "…")
		}
	}
	prefix := "> "
	if width < 3 {
		prefix = ""
	}
	input, cursor := terminalViewport(m.value, m.cursor, max(1, width-len(prefix)), m.widthMethod)
	inputRow := len(lines)
	lines = append(lines, prefix+input)
	if !m.done && len(m.choices) > 0 && len(lines) < m.height {
		selection := m.localize("Up/Down selects one option; you can also type an answer.")
		if m.selected >= 0 {
			selection = fmt.Sprintf("%d/%d  %s", m.selected+1, len(m.choices), safeTerminalText(m.localize(m.choices[m.selected].Label)))
			selection = "\x1b[7m" + m.widthMethod.Truncate(selection, width, "…") + "\x1b[0m"
		}
		lines = append(lines, m.widthMethod.Truncate(selection, width, "…"))
	}
	if !m.done && len(lines) < m.height {
		hint := m.localize("Left/Right edit · Enter accepts · Esc cancels")
		if len(m.choices) > 0 {
			hint = m.localize("Up/Down select · Left/Right edit · Enter accepts · Esc cancels")
		}
		if m.widthMethod.StringWidth(hint) > width {
			candidates := []string{"←→ edit Enter=OK Esc=cancel", "Enter=OK Esc=cancel", "Enter/Esc"}
			if len(m.choices) > 0 {
				candidates = []string{"↑↓ select ←→ edit Enter/Esc", "↑↓ Enter=OK Esc=cancel", "↑↓ Enter/Esc"}
			}
			for _, candidate := range candidates {
				hint = m.localize(candidate)
				if m.widthMethod.StringWidth(hint) <= width {
					break
				}
			}
		}
		if m.notice != "" && len(lines)+1 < m.height {
			lines = append(lines, m.widthMethod.Truncate(m.notice, width, "…"))
		}
		lines = append(lines, m.widthMethod.Truncate(hint, width, "…"))
	}
	content := strings.Join(lines, "\n")
	if m.done {
		content += "\n"
	}
	view := tea.NewView(content)
	if !m.done {
		view.Cursor = tea.NewCursor(min(width-1, len(prefix)+cursor), inputRow)
	}
	return view
}
