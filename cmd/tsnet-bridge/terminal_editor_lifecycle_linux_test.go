package main

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func blockedPromptEventSenders() int {
	data := make([]byte, 2<<20)
	n := runtime.Stack(data, true)
	return strings.Count(string(data[:n]), "ultraviolet.(*TerminalReader).sendEvents")
}

func TestTerminalEditorQueuedInputLifecycle(t *testing.T) {
	t.Setenv("TEA_TRACE", "")
	initial := blockedPromptEventSenders()
	for i := 0; i < 12; i++ {
		master, slave := openPromptPTY(t)
		if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
			t.Fatal(err)
		}
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, master); close(drained) }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		type promptResult struct {
			answer string
			err    error
		}
		result := make(chan promptResult, 1)
		go func() {
			answer, err := runTerminalEditor(ctx, slave, slave, testEditor())
			result <- promptResult{answer, err}
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			state, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if state.Lflag&unix.ICANON == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("editor did not enter raw mode")
			}
			time.Sleep(time.Millisecond)
		}
		if _, err := master.Write([]byte("ok\r" + strings.Repeat("queued", 500))); err != nil {
			t.Fatal(err)
		}
		if got := <-result; got.err != nil || got.answer != "ok" {
			t.Fatalf("queued input altered completed prompt: %q, %v", got.answer, got.err)
		}
		cancel()
		master.Close()
		slave.Close()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Fatal("transcript drain did not stop")
		}
	}
	time.Sleep(50 * time.Millisecond)
	remaining := blockedPromptEventSenders() - initial
	if remaining != 0 {
		t.Fatalf("%d terminal input scanners remain blocked after 12 completed prompts", remaining)
	}
}
