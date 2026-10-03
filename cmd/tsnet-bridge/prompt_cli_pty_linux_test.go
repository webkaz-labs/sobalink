package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

// This subprocess helper exercises real terminal input/output without enrollment,
// network access, saved credentials, or changes to a real profile.
func TestPromptCLIProcessHelper(t *testing.T) {
	mode := os.Getenv("TSNET_BRIDGE_PTY_TEST")
	if mode == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	_, out, _, err := prepareLocale([]string{"--lang", os.Getenv("TSNET_BRIDGE_PTY_LANG")}, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	p := newPrompts(newProcessInput(ctx, os.Stdin), out)
	var answer string
	switch mode {
	case "purpose":
		answer, _, err = choosePurposePrompt([]policy.Peer{}, []config.PeerRef{}, false, "web", p)
	case "text", "sequence":
		answer, err = p.ask("Rule name [sample]: ")
		if err == nil && mode == "sequence" {
			err = p.confirm("Proceed with fixture?", false)
		}
	default:
		t.Fatalf("unknown PTY mode %q", mode)
	}
	result := struct {
		Answer string `json:"answer"`
		Error  string `json:"error"`
	}{Answer: answer}
	if err != nil {
		result.Error = err.Error()
	}
	encoded, _ := json.Marshal(result)
	fmt.Printf("\nPTY_RESULT:%s\n", encoded)
}

type promptCLIResult struct {
	Answer string `json:"answer"`
	Error  string `json:"error"`
}

type promptCLITranscript struct {
	sync.Mutex
	buf bytes.Buffer
}

func (b *promptCLITranscript) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buf.Write(p)
}
func (b *promptCLITranscript) text() string {
	b.Lock()
	defer b.Unlock()
	return b.buf.String()
}

type promptCLIProcess struct {
	master, slave *os.File
	command       *exec.Cmd
	transcript    *promptCLITranscript
	initial       *unix.Termios
	done          chan error
}

func startPromptCLIProcess(t *testing.T, mode, language string, columns int) *promptCLIProcess {
	t.Helper()
	master, slave := openPromptPTY(t)
	initial, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: uint16(columns)}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPromptCLIProcessHelper$")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "TSNET_BRIDGE_PTY_TEST="+mode, "TSNET_BRIDGE_PTY_LANG="+language)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	process := &promptCLIProcess{master: master, slave: slave, command: cmd, transcript: &promptCLITranscript{}, initial: initial, done: make(chan error, 1)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = master.Close()
	})
	go func() { _, _ = io.Copy(process.transcript, master) }()
	go func() { process.done <- cmd.Wait() }()
	marker := "sample"
	if mode == "purpose" {
		marker = "web"
	}
	process.waitFor(t, func() bool {
		state, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
		return err == nil && state.Lflag&unix.ICANON == 0 && strings.Contains(process.transcript.text(), marker)
	}, "interactive prompt")
	return process
}

func (p *promptCLIProcess) waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		select {
		case err := <-p.done:
			t.Fatalf("process exited before %s: %v\n%q", description, err, p.transcript.text())
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s\n%q", description, p.transcript.text())
}
func (p *promptCLIProcess) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := io.WriteString(p.master, keys); err != nil {
		t.Fatal(err)
	}
}
func (p *promptCLIProcess) result(t *testing.T) promptCLIResult {
	t.Helper()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("prompt helper failed: %v\n%q", err, p.transcript.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("prompt did not finish\n%q", p.transcript.text())
	}
	// The process may exit just before the PTY reader publishes the last bytes.
	deadline := time.Now().Add(2 * time.Second)
	var result promptCLIResult
	for time.Now().Before(deadline) {
		output := p.transcript.text()
		if at := strings.Index(output, "PTY_RESULT:"); at >= 0 {
			if err := json.NewDecoder(strings.NewReader(output[at+len("PTY_RESULT:"):])).Decode(&result); err == nil {
				state, err := unix.IoctlGetTermios(int(p.slave.Fd()), unix.TCGETS)
				if err != nil || !reflect.DeepEqual(p.initial, state) {
					t.Fatalf("terminal modes not restored: %v\ninitial=%+v\nfinal=%+v", err, p.initial, state)
				}
				for i := range output {
					if output[i] == '\n' && (i == 0 || output[i-1] != '\r') {
						t.Fatalf("bare LF staircases the raw terminal display\n%q", output)
					}
				}
				for _, echo := range []string{"^[[A", "^[[B", "^[[C", "^[[D"} {
					if strings.Contains(output, echo) {
						t.Fatalf("literal terminal key echoed: %s\n%q", echo, output)
					}
				}
				return result
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing result\n%q", p.transcript.text())
	return result
}

func TestPromptCLIRealPTYEditingAndRestoration(t *testing.T) {
	cases := []struct{ name, keys, want string }{
		{"left_right", "ac\x1b[Db\x1b[C!", "abc!"},
		{"home_end_delete", "middle\x1b[Hpre-\x1b[F-post\x1b[H\x1b[3~", "re-middle-post"},
		{"japanese", "日本\x1b[D語", "日語本"},
		{"combining_backspace", "Ae\u0301B\x1b[D\x7f", "AB"},
		{"emoji_backspace", "A👨‍👩‍👧‍👦B\x1b[D\x7f", "AB"},
		{"ctrl_d_deletes", "abc\x1b[H\x04", "bc"},
	}
	for _, language := range []string{"en", "ja"} {
		for _, columns := range []int{30, 40, 80} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/%d/%s", language, columns, tc.name), func(t *testing.T) {
					process := startPromptCLIProcess(t, "text", language, columns)
					process.send(t, tc.keys+"\r")
					result := process.result(t)
					if result.Error != "" || result.Answer != tc.want {
						t.Fatalf("answer=%q error=%q want=%q\n%q", result.Answer, result.Error, tc.want, process.transcript.text())
					}
				})
			}
		}
	}
}

func TestPromptCLIRealPTYCancellationAndRestoration(t *testing.T) {
	for name, key := range map[string]string{"escape": "\x1b", "ctrl_c": "\x03", "ctrl_d": "\x04"} {
		t.Run(name, func(t *testing.T) {
			process := startPromptCLIProcess(t, "text", "ja", 30)
			process.send(t, key)
			result := process.result(t)
			if result.Error == "" || result.Answer != "" {
				t.Fatalf("cancellation accepted input: %+v", result)
			}
		})
	}
	t.Run("external_interrupt", func(t *testing.T) {
		process := startPromptCLIProcess(t, "text", "en", 40)
		if err := process.command.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		result := process.result(t)
		if result.Error == "" || result.Answer != "" {
			t.Fatalf("interrupt accepted input: %+v", result)
		}
	})
}

func TestPromptCLIRealPTYPurposeArrows(t *testing.T) {
	for _, language := range []string{"en", "ja"} {
		for _, columns := range []int{30, 40, 80} {
			t.Run(fmt.Sprintf("%s/%d", language, columns), func(t *testing.T) {
				process := startPromptCLIProcess(t, "purpose", language, columns)
				process.send(t, "\x1b[B\r")
				result := process.result(t)
				if result.Error != "" || result.Answer != "ssh" {
					t.Fatalf("arrow choice answer=%q error=%q\n%q", result.Answer, result.Error, process.transcript.text())
				}
			})
		}
	}
}

func TestPromptCLIRealPTYBracketedPasteIsBoundedAndAtomic(t *testing.T) {
	cases := []struct{ name, paste, want string }{
		{"valid_unicode", "日本語e\u0301👩‍💻", "safe日本語e\u0301👩‍💻end"},
		{"multiline", "\ny\nq\n", "safeend"},
		{"control", "abc\x03xyz", "safeend"},
		{"oversized", strings.Repeat("x", 17*1024), "safeend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			process := startPromptCLIProcess(t, "text", "en", 30)
			process.send(t, "safe\x1b[200~"+tc.paste+"\x1b[201~end\r")
			result := process.result(t)
			if result.Error != "" || result.Answer != tc.want {
				t.Fatalf("paste answer=%q error=%q want=%q\n%q", result.Answer, result.Error, tc.want, process.transcript.text())
			}
		})
	}
}

func TestPromptCLIRealPTYPurposeTypedOverride(t *testing.T) {
	for _, tc := range []struct {
		name, keys, answer string
		canceled           bool
	}{
		{"typed_name", "ai\r", "ai", false},
		{"typed_number", "2\r", "ssh", false},
		{"typed_cancel", "q\r", "", true},
		{"down_up", "\x1b[B\x1b[B\x1b[A\r", "ssh", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := startPromptCLIProcess(t, "purpose", "ja", 30)
			process.send(t, tc.keys)
			result := process.result(t)
			if tc.canceled {
				if result.Error == "" || result.Answer != "" {
					t.Fatalf("cancel result=%+v", result)
				}
			} else if result.Error != "" || result.Answer != tc.answer {
				t.Fatalf("typed choice answer=%q error=%q want=%q", result.Answer, result.Error, tc.answer)
			}
		})
	}
}

func TestPromptCLIRealPTYMultilinePasteCannotConfirmNextPrompt(t *testing.T) {
	process := startPromptCLIProcess(t, "sequence", "en", 40)
	process.send(t, "\x1b[200~first\ny\n\x1b[201~")
	// Let the complete paste reach the event loop. A multiline paste must neither
	// finish this prompt nor supply an answer to the subsequent confirmation.
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(process.transcript.text(), "Proceed with fixture?") || strings.Contains(process.transcript.text(), "PTY_RESULT:") {
		t.Fatalf("paste advanced a prompt\n%q", process.transcript.text())
	}
	process.send(t, "safe\r")
	process.waitFor(t, func() bool { return strings.Contains(process.transcript.text(), "Proceed with fixture?") }, "separate confirmation")
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(process.transcript.text(), "PTY_RESULT:") {
		t.Fatalf("paste confirmed next prompt\n%q", process.transcript.text())
	}
	process.send(t, "q\r")
	result := process.result(t)
	if result.Answer != "safe" || result.Error == "" {
		t.Fatalf("sequence result=%+v", result)
	}
}

func TestPromptCLIRealPTYInputStreamLimitRestoresTerminal(t *testing.T) {
	process := startPromptCLIProcess(t, "text", "en", 40)
	process.send(t, "\x1b[200~"+strings.Repeat("x", 65*1024)+"\x1b[201~")
	result := process.result(t)
	if result.Answer != "" || result.Error == "" {
		t.Fatalf("oversized terminal stream accepted: %+v", result)
	}
}

func TestPromptCLIRealPTYInvalidUTF8RestoresTerminal(t *testing.T) {
	process := startPromptCLIProcess(t, "text", "ja", 40)
	process.send(t, "\x1b[200~good\xffbad\x1b[201~")
	result := process.result(t)
	if result.Answer != "" || result.Error == "" {
		t.Fatalf("invalid terminal UTF-8 accepted: %+v", result)
	}
}

func TestPromptCLIRealPTYRendersCombiningMarkBeforeEnter(t *testing.T) {
	process := startPromptCLIProcess(t, "text", "en", 30)
	process.send(t, "Ae\u0301B")
	process.waitFor(t, func() bool {
		output := process.transcript.text()
		return strings.Contains(output, "\u0301") || strings.Contains(output, "é")
	}, "visible combining accent before acceptance")
	process.send(t, "\r")
	result := process.result(t)
	if result.Answer != "Ae\u0301B" || result.Error != "" {
		t.Fatalf("combining answer changed: %+v", result)
	}
}
