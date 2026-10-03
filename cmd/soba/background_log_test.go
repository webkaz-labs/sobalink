package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBackgroundLogPreservesForegroundWriters(t *testing.T) {
	t.Setenv(backgroundLogEnv, "unused")
	if err := os.Unsetenv(backgroundLogEnv); err != nil {
		t.Fatal(err)
	}
	var out, errorOut bytes.Buffer
	gotOut, gotErrorOut, closeOutput, err := backgroundCommandOutput([]string{"run"}, &out, &errorOut)
	if err != nil || gotOut != &out || gotErrorOut != &errorOut {
		t.Fatal("foreground diagnostics changed", err)
	}
	closeOutput()
}

func TestBackgroundLogMarkerPreservesOrdinaryCommandWriters(t *testing.T) {
	for _, command := range []string{"status", "--help"} {
		t.Run(command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "startup.log")
			if err := os.WriteFile(path, []byte("existing output"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(backgroundLogEnv, path)
			var out, errorOut bytes.Buffer
			gotOut, gotErrorOut, closeOutput, err := backgroundCommandOutput([]string{command}, &out, &errorOut)
			if err != nil || gotOut != &out || gotErrorOut != &errorOut {
				t.Fatal("inherited marker redirected ordinary command", err)
			}
			fmt.Fprintln(gotOut, "foreground output")
			writeCommandError(gotErrorOut, errors.New("foreground error"))
			closeOutput()
			if !strings.Contains(out.String(), "foreground output") || !strings.Contains(errorOut.String(), "foreground error") {
				t.Fatal("ordinary command diagnostics missing")
			}
			if _, inherited := os.LookupEnv(backgroundLogEnv); inherited {
				t.Fatal("inherited marker was not cleared")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "existing output" {
				t.Fatal("ordinary command changed background log", string(data), err)
			}
		})
	}
}

func TestBackgroundLogInvocationScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "startup.log")
	for _, locale := range []string{"auto", "en", "ja"} {
		args := []string{"--state-dir", dir, "--locale", locale, "run"}
		if !backgroundLogInvocation(args, path) || !backgroundLogInvocation(append(args, "--offline"), path) {
			t.Fatal("internally emitted invocation refused", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"run"},
		{"--state-dir", dir, "--locale", "en", "status"},
		{"--state-dir", dir, "--locale", "en", "--help"},
		{"--state-dir", dir, "--locale", "en", "run", "--help"},
		{"--state-dir", dir, "--locale", "en", "run", "--offline", "extra"},
		{"--state-dir", dir, "--locale", "invalid", "run"},
		{"--state-dir", "relative", "--locale", "en", "run"},
		{"--locale", "en", "--state-dir", dir, "run"},
		{"--state-dir", filepath.Join(dir, "other"), "--locale", "en", "run"},
	} {
		if backgroundLogInvocation(args, path) {
			t.Fatal("unexpected invocation enabled private logging", args)
		}
	}
}

func TestBackgroundLogOwnsPrivateOutputAndErrorWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.log")
	file, err := openStartupLog(path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	t.Setenv(backgroundLogEnv, path)
	var foreground bytes.Buffer
	args := []string{"--state-dir", filepath.Dir(path), "--locale", "en", "run"}
	out, errorOut, closeOutput, err := backgroundCommandOutput(args, &foreground, &foreground)
	if err != nil {
		t.Fatal(err)
	}
	if _, inherited := os.LookupEnv(backgroundLogEnv); inherited {
		t.Fatal("background logging mode would leak into launched commands")
	}
	if _, err := io.WriteString(out, "application output\n"); err != nil {
		t.Fatal(err)
	}
	writeCommandError(errorOut, errors.New("application error"))
	closeOutput()
	data, err := os.ReadFile(path)
	if err != nil || foreground.Len() != 0 || !bytes.Contains(data, []byte("application output")) || !bytes.Contains(data, []byte("application error")) {
		t.Fatal(string(data), err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal("log is not private", err)
	}
}

func TestBackgroundLogRefusesNonregularFiles(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(fmt.Sprintf("create-%t", create), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "startup.log")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if log, err := openStartupLog(path, create, false); err == nil {
				log.Close()
				t.Fatal("directory accepted as startup log")
			}
		})
	}
	t.Run("child-symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "preserved")
		if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "startup.log")
		if err := os.Symlink(target, path); err != nil {
			t.Skip("symlink unavailable", err)
		}
		t.Setenv(backgroundLogEnv, path)
		args := []string{"--state-dir", filepath.Dir(path), "--locale", "en", "run"}
		if _, _, _, err := backgroundCommandOutput(args, io.Discard, io.Discard); err == nil {
			t.Fatal("child accepted a symlink log")
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "keep" {
			t.Fatal("symlink target changed", err)
		}
	})
}

// This fixture only writes temporary files. It does not start the application,
// open sockets, or use a network backend.
func TestBackgroundLogProcessFixture(t *testing.T) {
	mode := os.Getenv("SOBALINK_TEST_LOG_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("SOBALINK_TEST_LOG_DIR")
	fail := func(err error) {
		_ = os.WriteFile(filepath.Join(dir, "fixture-error"), []byte(err.Error()), 0600)
		os.Exit(2)
	}
	if mode == "launcher" {
		file, err := openStartupLog(filepath.Join(dir, "startup.log"), true, false)
		if err != nil {
			fail(err)
		}
		executable, err := os.Executable()
		if err != nil {
			fail(err)
		}
		if err := os.Setenv("SOBALINK_TEST_LOG_MODE", "writer"); err != nil {
			fail(err)
		}
		if _, err := launchBackground(executable, []string{"-test.run=^TestBackgroundLogProcessFixture$"}, file); err != nil {
			fail(err)
		}
		file.Close()
		os.Exit(0)
	}
	if mode != "writer" {
		fail(errors.New("unknown fixture mode"))
	}
	// The test runner has its own flags; exercise the application's emitted
	// argument shape when initializing the fixture's private output writer.
	args := []string{"--state-dir", dir, "--locale", "en", "run"}
	out, errorOut, closeOutput, err := backgroundCommandOutput(args, os.Stdout, os.Stderr)
	if err != nil {
		fail(err)
	}
	if _, err := out.Write(append(bytes.Repeat([]byte("x"), 2*startupLogMaxBytes), []byte("first tail\n")...)); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		fail(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "continue")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fail(errors.New("fixture continuation timed out"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	chunk := bytes.Repeat([]byte("recent output\n"), 4096)
	for range 64 {
		if _, err := out.Write(chunk); err != nil {
			fail(err)
		}
	}
	fmt.Fprintln(os.Stdout, "raw stdout should be discarded")
	fmt.Fprintln(os.Stderr, "raw stderr should be discarded")
	writeCommandError(errorOut, errors.New("final application error"))
	closeOutput()
	if err := os.WriteFile(filepath.Join(dir, "done"), []byte("done"), 0600); err != nil {
		fail(err)
	}
	os.Exit(0)
}

func TestBackgroundLogRemainsBoundedAfterLauncherExits(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := exec.Command(executable, "-test.run=^TestBackgroundLogProcessFixture$")
	launcher.Env = append(os.Environ(), "SOBALINK_TEST_LOG_MODE=launcher", "SOBALINK_TEST_LOG_DIR="+dir)
	if output, err := launcher.CombinedOutput(); err != nil {
		t.Fatal("fixture launcher failed", string(output), err)
	}
	waitForFile := func(name string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			if failure, err := os.ReadFile(filepath.Join(dir, "fixture-error")); err == nil {
				t.Fatal("fixture failed:", string(failure))
			}
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("fixture did not reach", name)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Release the writer even if a preceding assertion fails.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "continue"), nil, 0600) })
	waitForFile("ready")
	path := filepath.Join(dir, "startup.log")
	data, err := os.ReadFile(path)
	if err != nil || len(data) != startupLogMaxBytes || !bytes.HasSuffix(data, []byte("first tail\n")) {
		t.Fatal("oversized child output was not bounded", len(data), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "continue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitForFile("done")
	data, err = os.ReadFile(path)
	if err != nil || len(data) > startupLogMaxBytes || len(data) < startupLogMaxBytes/2 || !strings.Contains(string(data), "final application error") || bytes.Contains(data, []byte("raw stdout")) || bytes.Contains(data, []byte("raw stderr")) {
		t.Fatal("continued child output was not bounded and retained", len(data), err)
	}
}
