package main

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTerminalEditorRefusesTraceWithoutCreatingFile(t *testing.T) {
	_, slave := openPromptPTY(t)
	path := filepath.Join(t.TempDir(), "never-log-input")
	t.Setenv("TEA_TRACE", path)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newPrompts(slave, slave).readTerminalLine("Private input: ", nil, "")
	if err == nil || !strings.Contains(err.Error(), "unset TEA_TRACE") {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("trace file created: %v", err)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("trace refusal changed terminal")
	}
}

func TestTerminalEditorPanicRestoresWithoutLogging(t *testing.T) {
	_, slave := openPromptPTY(t)
	t.Setenv("TEA_TRACE", "")
	t.Setenv("TEA_DEBUG", "true")
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	model := newTerminalEditor("Value: ", nil, "", func(string) string { panic("do-not-print-this-panic-value") })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = runTerminalEditor(ctx, slave, slave, model)
	if err == nil || err.Error() != "terminal editor failed; no changes made" {
		t.Fatalf("panic not sanitized: %v", err)
	}
	after, readErr := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("panic left terminal modified")
	}
}
