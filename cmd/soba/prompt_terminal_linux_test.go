package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openPromptPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

type promptReadyWriter struct{ ready chan struct{} }

func (w promptReadyWriter) Write(p []byte) (int, error) {
	select {
	case w.ready <- struct{}{}:
	default:
	}
	return len(p), nil
}

func TestPromptPTYBackspaceKeepsUTF8AndRestoresTerminal(t *testing.T) {
	master, slave := openPromptPTY(t)
	fd := int(slave.Fd())
	initial, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	initial.Iflag &^= unix.IUTF8
	initial.Lflag |= unix.ICANON | unix.ECHO | unix.ECHOE
	initial.Cc[unix.VERASE] = 127
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, initial); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := newPrompts(slave, promptReadyWriter{ready}).ask("名前: ")
		done <- result{answer, err}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not become ready")
	}
	active, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || active.Iflag&unix.IUTF8 == 0 {
		t.Fatalf("UTF-8 erase not enabled: %v", err)
	}
	if _, err := master.Write([]byte("日本\x7f\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.answer != "日" {
			t.Fatalf("Japanese backspace corrupted input: %q, %v", got.answer, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not return")
	}
	final, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || !reflect.DeepEqual(initial, final) {
		t.Fatalf("terminal mode was not restored: %v", err)
	}
}

func TestPromptPTYRestoresTerminalWhenOutputFails(t *testing.T) {
	_, slave := openPromptPTY(t)
	initial, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newPrompts(slave, shortLocaleWriter{}).ask("名前: "); err == nil {
		t.Fatal("missing output error")
	}
	final, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || !reflect.DeepEqual(initial, final) {
		t.Fatalf("terminal mode was not restored after error: %v", err)
	}
}

func TestPromptPTYCancellationRestoresTerminal(t *testing.T) {
	master, slave := openPromptPTY(t)
	fd := int(slave.Fd())
	initial, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	initial.Iflag &^= unix.IUTF8
	initial.Lflag |= unix.ICANON
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := slave
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		p := newPrompts(input, promptReadyWriter{ready})
		p.ctx = ctx
		_, err := p.ask("名前: ")
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not become ready")
	}
	active, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || active.Iflag&unix.IUTF8 == 0 {
		t.Fatalf("wrapped terminal was not configured: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal prompt did not cancel")
	}
	final, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || !reflect.DeepEqual(initial, final) {
		t.Fatalf("terminal mode was not restored after cancellation: %v", err)
	}
	// Closing the master releases any process-owned OS read that was already
	// in flight. In the actual CLI this last read ends with process exit.
	master.Close()
}
