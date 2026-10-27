package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in real-browser coverage of the production HTTP handler and built assets.
// Fictional state only; no enrollment, peer network, profile, or OS opener.
type uiBrowserBackend struct{}

func (uiBrowserBackend) Snapshot(context.Context) (map[string]any, error) {
	return map[string]any{"self": map[string]any{"name": "Example device", "status": "offline"}, "peers": []any{}, "settings": map[string]any{"network": "none", "locale": "en", "theme": "light"}}, nil
}
func (uiBrowserBackend) Command(context.Context, Command) (any, error) { return nil, nil }
func (uiBrowserBackend) Upload(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusForbidden)
}
func TestUILaunchRealBrowser(t *testing.T) {
	if os.Getenv("SOBA_UI_BROWSER_TEST") != "1" {
		t.Skip("set SOBA_UI_BROWSER_TEST=1 after building web assets; requires Node and Chromium")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	server, err := Start(ctx, os.DirFS(filepath.Join("..", "..", "web", "dist")), uiBrowserBackend{})
	if err != nil {
		t.Fatal("could not start isolated UI server")
	}
	defer server.Close(context.Background())
	command := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "web", "browser", "ui-handoff.acceptance.mjs"))
	command.Stderr = io.Discard // Browser diagnostics can contain private URLs.
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal("fixture pipe unavailable")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal("fixture pipe unavailable")
	}
	if err = command.Start(); err != nil {
		t.Fatal("browser fixture unavailable")
	}
	send := func(target *Server, expired bool) {
		code, e := target.IssueCode()
		if e != nil {
			t.Error("fixture code unavailable")
			return
		}
		if expired {
			target.mu.Lock()
			target.codeUntil = time.Now().Add(-time.Second)
			target.mu.Unlock()
		}
		if e = json.NewEncoder(input).Encode(map[string]string{"url": target.URL(), "code": code}); e != nil {
			t.Error("fixture input failed")
		}
	}
	send(server, false)
	scanner := bufio.NewScanner(output)
	passed := false
	for scanner.Scan() {
		switch strings.TrimSpace(scanner.Text()) {
		case "rotate":
			send(server, false)
		case "other-profile":
			other, e := Start(ctx, os.DirFS(filepath.Join("..", "..", "web", "dist")), uiBrowserBackend{})
			if e != nil {
				t.Error("could not start second isolated UI server")
				cancel()
				continue
			}
			defer other.Close(context.Background())
			send(other, false)
		case "expire":
			send(server, true)
		case "pass":
			passed = true
		case "fail:launch", "fail:context", "fail:initial", "fail:cookie", "fail:history", "fail:reuse", "fail:replay", "fail:expiry", "fail:manual", "fail:profiles":
			t.Error("browser fixture stage:", scanner.Text())
		default:
			t.Error("unexpected browser fixture report; details withheld")
		}
	}
	_ = input.Close()
	if err = command.Wait(); err != nil || !passed || scanner.Err() != nil {
		t.Fatal("local UI browser acceptance failed; private details withheld")
	}
}
