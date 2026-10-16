package main

// Linux's real PTY and private Unix IPC exercise the CLI entrypoint in another
// process. Only the OS browser opener is intercepted. This does not establish
// installed-binary or actual desktop-browser acceptance on any platform.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestUILaunchCLIProcessHelper(t *testing.T) {
	if os.Getenv("SOBA_TEST_UI_PROCESS") != "1" {
		return
	}
	separator := -1
	for i, argument := range os.Args {
		if argument == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(90)
	}
	capture := os.NewFile(3, "private-ui-opener-pipe")
	if capture == nil {
		os.Exit(91)
	}
	defer capture.Close()
	launchUIBrowser = func(_ context.Context, target string) error {
		if err := json.NewEncoder(capture).Encode(target); err != nil {
			return errors.New("opener capture unavailable")
		}
		if os.Getenv("SOBA_TEST_UI_OPENER") == "fail" {
			// A real opener error can include its arguments. CLI output must redact it.
			return errors.New("opener failed for " + target)
		}
		return nil
	}
	os.Args = append([]string{os.Args[0]}, os.Args[separator+1:]...)
	os.Exit(mainExitCode())
}

type uiProcessResult struct {
	output, target string
	success        bool
}

func runUIProcess(t *testing.T, directory string, terminal, desktop, openerFailure bool, args ...string) uiProcessResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	argv := append([]string{"-test.run=^TestUILaunchCLIProcessHelper$", "--", "--state-dir", directory, "--locale", "en", "ui"}, args...)
	command := exec.CommandContext(ctx, executable, argv...)
	command.WaitDelay = 2 * time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "SOBA_TEST_UI_PROCESS", "SOBA_TEST_UI_OPENER", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "DISPLAY", "WAYLAND_DISPLAY":
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "SOBA_TEST_UI_PROCESS=1")
	if desktop {
		command.Env = append(command.Env, "DISPLAY=:synthetic")
	}
	if openerFailure {
		command.Env = append(command.Env, "SOBA_TEST_UI_OPENER=fail")
	}
	captureRead, captureWrite, err := os.Pipe()
	if err != nil {
		t.Fatal("private opener pipe unavailable")
	}
	defer captureRead.Close()
	defer captureWrite.Close()
	command.ExtraFiles = []*os.File{captureWrite}
	var output bytes.Buffer
	var closeSlave func()
	var terminalMaster *os.File
	done := make(chan struct{})
	if terminal {
		master, slave := openPromptPTY(t)
		terminalMaster = master
		command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
		closeSlave = func() { _ = slave.Close() }
		go func() { _, _ = io.Copy(&output, master); close(done) }()
	} else {
		command.Stdout, command.Stderr = &output, &output
		close(done)
	}
	stopIO := context.AfterFunc(ctx, func() {
		_ = captureRead.Close()
		if terminalMaster != nil {
			_ = terminalMaster.Close()
		}
	})
	defer stopIO()
	if err = command.Start(); err != nil {
		if closeSlave != nil {
			closeSlave()
		}
		t.Fatal("CLI subprocess could not start; private details withheld")
	}
	_ = captureWrite.Close()
	if closeSlave != nil {
		closeSlave()
	}
	err = command.Wait()
	<-done
	if ctx.Err() != nil {
		t.Fatal("CLI subprocess exceeded its deadline")
	}
	captured, readErr := io.ReadAll(io.LimitReader(captureRead, 8193))
	if readErr != nil || len(captured) > 8192 {
		t.Fatal("invalid opener capture")
	}
	result := uiProcessResult{output: output.String(), success: err == nil}
	if len(captured) != 0 && json.Unmarshal(captured, &result.target) != nil {
		t.Fatal("opener was called more than once or produced invalid capture")
	}
	return result
}

func TestUILaunchCLIProcess(t *testing.T) {
	directory := privateStateDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	application, err := core.Open(ctx, core.Options{Directory: directory, LifecycleLock: cliFixtureLifecycleOwner(t, directory), SkipNetworkStart: true})
	if err != nil {
		t.Fatal("isolated offline profile unavailable")
	}
	defer application.Close()
	assets := fstest.MapFS{"index.html": {Data: []byte("isolated UI process fixture")}}
	localURL, _, err := application.StartWeb(assets)
	if err != nil {
		t.Fatal("isolated production Web handler unavailable")
	}
	var uiRequests atomic.Int32
	ipc, err := control.Serve(ctx, directory, func(ctx context.Context, raw string) (any, error) {
		if raw == "ui" {
			uiRequests.Add(1)
		}
		return application.IPC(ctx, raw)
	})
	if err != nil {
		t.Fatal("production profile IPC unavailable")
	}
	defer ipc.Close()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	exchange := func(t *testing.T, code string, cookie *http.Cookie) (int, []*http.Cookie, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"code": code})
		request, err := http.NewRequestWithContext(ctx, "POST", localURL+"/api/session", bytes.NewReader(body))
		if err != nil {
			t.Fatal("session request unavailable")
		}
		request.Header.Set("Origin", localURL)
		request.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("native session exchange failed; private details withheld")
		}
		defer response.Body.Close()
		var result struct {
			CSRF string `json:"csrfToken"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result) != nil {
			t.Fatal("invalid session response")
		}
		return response.StatusCode, response.Cookies(), result.CSRF
	}
	parseCapture := func(t *testing.T, result uiProcessResult) string {
		t.Helper()
		target, err := url.Parse(result.target)
		if err != nil || target.Scheme+"://"+target.Host != localURL || target.Path != "/" || target.RawQuery != "" || !strings.HasPrefix(target.Fragment, "handoff=") {
			t.Fatal("CLI did not open its selected profile's exact UI")
		}
		code := strings.TrimPrefix(target.Fragment, "handoff=")
		if _, err := uiHandoffURL(localURL, code); err != nil {
			t.Fatal("invalid captured handoff")
		}
		if strings.Contains(result.output, code) || strings.Contains(result.output, result.target) {
			t.Fatal("CLI leaked its private handoff")
		}
		return code
	}
	t.Run("automatic-and-existing-session", func(t *testing.T) {
		first := runUIProcess(t, directory, true, true, false)
		if !first.success {
			t.Fatal("automatic CLI launch failed")
		}
		code := parseCapture(t, first)
		status, cookies, csrf := exchange(t, code, nil)
		if status != http.StatusOK || len(cookies) != 1 || csrf == "" {
			t.Fatal("captured CLI handoff did not authorize a session")
		}
		if status, _, _ := exchange(t, code, nil); status != http.StatusUnauthorized {
			t.Fatal("captured handoff allowed replay")
		}
		second := runUIProcess(t, directory, true, true, false)
		if !second.success {
			t.Fatal("repeated CLI launch failed")
		}
		status, replacements, repeatedCSRF := exchange(t, parseCapture(t, second), cookies[0])
		if status != http.StatusOK || len(replacements) != 0 || repeatedCSRF != csrf {
			t.Fatal("repeated CLI launch replaced session authority")
		}
		request, _ := http.NewRequestWithContext(ctx, "GET", localURL+"/api/state", nil)
		request.AddCookie(cookies[0])
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("native authenticated state lookup failed")
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("CLI handoff session could not read production state")
		}
	})
	t.Run("opener-failure", func(t *testing.T) {
		result := runUIProcess(t, directory, true, true, true)
		if result.success {
			t.Fatal("failed opener reported success")
		}
		_ = parseCapture(t, result)
		if !strings.Contains(result.output, cliCommandExample(directory, "ui", "--no-browser")) {
			t.Fatal("opener failure omitted selected-profile recovery")
		}
	})
	for _, mode := range []string{"--no-browser", "--json"} {
		t.Run(mode, func(t *testing.T) {
			result := runUIProcess(t, directory, mode != "--json", false, false, mode)
			if !result.success || result.target != "" {
				t.Fatal("explicit manual mode failed or opened a browser")
			}
			var access struct {
				URL  string `json:"url"`
				Code string `json:"code"`
			}
			if mode == "--json" {
				if json.Unmarshal([]byte(result.output), &access) != nil || access.URL != localURL {
					t.Fatal("explicit JSON contract changed")
				}
			} else {
				for _, line := range strings.Split(result.output, "\n") {
					if strings.HasPrefix(line, "One-time code: ") {
						access.Code = strings.TrimSpace(strings.TrimPrefix(line, "One-time code: "))
					}
				}
			}
			if status, _, _ := exchange(t, access.Code, nil); status != http.StatusOK {
				t.Fatal("manual process output did not authorize a session")
			}
		})
	}
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "headless", false: "redirected"}[terminal], func(t *testing.T) {
			before := uiRequests.Load()
			result := runUIProcess(t, directory, terminal, false, false)
			if result.success || result.target != "" || uiRequests.Load() != before {
				t.Fatal("implicit headless or redirected launch requested a private code")
			}
		})
	}
}
