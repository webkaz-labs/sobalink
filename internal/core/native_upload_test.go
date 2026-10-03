package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type nativeUploadRead struct {
	batch *outgoingBatch
	spool string
}
type nativeUploadBackend struct {
	*Core
	idle          time.Duration
	ready         chan nativeUploadRead
	finished      chan struct{}
	requestCancel chan context.CancelFunc
}

// The observer delegates to the actual net/http body; it never supplies bytes
// or implements the cancellation behavior under test.
type nativeUploadObserver struct {
	io.ReadCloser
	core  *Core
	once  sync.Once
	ready chan nativeUploadRead
}

func (b *nativeUploadObserver) Read(p []byte) (int, error) {
	b.core.mu.RLock()
	var staged nativeUploadRead
	for _, batch := range b.core.outgoing {
		batch.mu.Lock()
		if batch.staging && batch.Spool != "" {
			staged = nativeUploadRead{batch, batch.Spool}
		}
		batch.mu.Unlock()
	}
	b.core.mu.RUnlock()
	if staged.batch != nil {
		b.once.Do(func() { b.ready <- staged })
	}
	return b.ReadCloser.Read(p)
}
func (b *nativeUploadBackend) Upload(w http.ResponseWriter, r *http.Request) {
	defer func() { b.finished <- struct{}{} }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	b.requestCancel <- cancel
	r = r.WithContext(ctx)
	r.Body = &nativeUploadObserver{ReadCloser: r.Body, core: b.Core, ready: b.ready}
	b.Core.uploadWithIdle(w, r, b.Core.transferLimits(), b.idle)
}

type nativeUploadFixture struct {
	pair    corePair
	backend *nativeUploadBackend
	server  *webui.Server
	host    string
	cookie  *http.Cookie
	csrf    string
}

func newNativeUploadFixture(t *testing.T, idle time.Duration, staging capacity.Choice) nativeUploadFixture {
	t.Helper()
	pair := newCorePair(t)
	trustPair(t, pair)
	pair.a.mu.Lock()
	pair.a.capacity.Logical["stagingSeconds"] = staging
	pair.a.mu.Unlock()
	backend := &nativeUploadBackend{Core: pair.a, idle: idle, ready: make(chan nativeUploadRead, 1), finished: make(chan struct{}, 4), requestCancel: make(chan context.CancelFunc, 4)}
	server, err := webui.Start(context.Background(), fstest.MapFS{"index.html": {Data: []byte("UI")}}, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	code, err := server.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", server.URL()+"/api/session", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Origin", server.URL())
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var login struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || login.CSRF == "" || len(resp.Cookies()) != 1 {
		t.Fatalf("login status %d %+v", resp.StatusCode, login)
	}
	return nativeUploadFixture{pair, backend, server, strings.TrimPrefix(server.URL(), "http://"), resp.Cookies()[0], login.CSRF}
}

func (f nativeUploadFixture) openUpload(t *testing.T, body string, length int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", f.host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	framing := fmt.Sprintf("Content-Length: %d\r\n", length)
	if length < 0 {
		framing = "Transfer-Encoding: chunked\r\n"
		if body != "" {
			body = fmt.Sprintf("%x\r\n%s\r\n", len(body), body)
		}
	}
	_, err = fmt.Fprintf(conn, "POST /api/upload HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\nX-CSRF-Token: %s\r\nContent-Type: multipart/form-data; boundary=native-boundary\r\n%s\r\n%s", f.host, f.server.URL(), f.cookie.String(), f.csrf, framing, body)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
func nativeUploadField(name, value string) string {
	return "--native-boundary\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
}
func nativeUploadPrefix(id string, size int, filename string) string {
	return nativeUploadField("peerId", "peer-b") + nativeUploadField("requestId", id) + nativeUploadField("manifest", fmt.Sprintf(`[{"path":"note.txt","kind":"file","size":%d}]`, size)) + "--native-boundary\r\nContent-Disposition: form-data; name=\"files\"; filename=\"" + filename + "\"\r\nContent-Type: application/octet-stream\r\n\r\n"
}

const nativeUploadSuffix = "\r\n--native-boundary--\r\n"

func awaitNativeUpload(t *testing.T, f nativeUploadFixture) {
	t.Helper()
	select {
	case <-f.backend.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("native upload retained its handler")
	}
}
func nativeUploadResponse(t *testing.T, reader *bufio.Reader) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}
func requireNativeCleanup(t *testing.T, f nativeUploadFixture, staged nativeUploadRead) {
	t.Helper()
	staged.batch.mu.Lock()
	reserved, spool := staged.batch.reserved, staged.batch.Spool
	staged.batch.mu.Unlock()
	f.pair.a.mu.RLock()
	remaining := len(f.pair.a.outgoing)
	f.pair.a.mu.RUnlock()
	if reserved != 0 || spool != "" || remaining != 0 {
		t.Fatalf("retained staging: bytes=%d spool=%q entries=%d", reserved, spool, remaining)
	}
	if _, err := os.Stat(staged.spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool remains: %v", err)
	}
	if len(f.pair.b.transfers.List()) != 0 {
		t.Fatal("incomplete native upload offered to remote")
	}
}

func TestNativeMultipartPreambleAndMalformedDrain(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"before-boundary", ""},
		{"inside-boundary", "--native-"},
		{"headers", "--native-boundary\r\nContent-Disposition: form-data; name=\"peerId\""},
		{"peer-field", "--native-boundary\r\nContent-Disposition: form-data; name=\"peerId\"\r\n\r\npeer-"},
		{"manifest", nativeUploadField("peerId", "peer-b") + nativeUploadField("requestId", "preamble") + "--native-boundary\r\nContent-Disposition: form-data; name=\"manifest\"\r\n\r\n[{\"path\":\""},
		{"malformed-field", "--native-boundary\r\nContent-Disposition: form-data; name=\"wrong\"\r\n\r\nunfinished"},
		{"malformed-file", nativeUploadPrefix("malformed", 4, "other.txt") + "d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeUploadFixture(t, 100*time.Millisecond, capacity.Choice{Mode: "unlimited"})
			conn := f.openUpload(t, tc.body, len(tc.body)+64)
			awaitNativeUpload(t, f)
			resp := nativeUploadResponse(t, bufio.NewReader(conn))
			if resp.StatusCode < 400 || !resp.Close {
				t.Fatalf("unbounded malformed response: %+v", resp)
			}
			f.pair.a.mu.RLock()
			remaining := len(f.pair.a.outgoing)
			f.pair.a.mu.RUnlock()
			if remaining != 0 || len(f.pair.b.transfers.List()) != 0 {
				t.Fatal("preamble failure retained or offered staging")
			}
		})
	}
}

func TestNativeMultipartCancelAndDeadline(t *testing.T) {
	for _, action := range []string{"idle", "chunked-eof", "staging-timeout", "explicit-cancel", "core-close", "disconnect", "request-cancel", "unlimited-cancel", "cleanup-failure", "pause", "revoke"} {
		t.Run(action, func(t *testing.T) {
			idle := 2 * time.Second
			staging := capacity.Choice{Mode: "unlimited"}
			if action == "idle" || action == "chunked-eof" {
				idle = 100 * time.Millisecond
			}
			if action == "staging-timeout" {
				staging = capacity.Limited(1)
			} else if action == "explicit-cancel" {
				staging = capacity.Limited(10)
			}
			f := newNativeUploadFixture(t, idle, staging)
			prefix := nativeUploadPrefix("slow-native", 4, "note.txt") + "d"
			length := len(prefix) + len(nativeUploadSuffix) + 3
			if action == "chunked-eof" {
				prefix = nativeUploadPrefix("slow-native", 4, "note.txt") + "data" + nativeUploadSuffix
				length = -1 // Complete multipart, but no HTTP zero chunk/trailers.
			}
			conn := f.openUpload(t, prefix, length)
			var staged nativeUploadRead
			select {
			case staged = <-f.backend.ready:
			case <-f.backend.finished:
				t.Fatal("upload failed before payload read")
			case <-time.After(time.Second):
				t.Fatal("native payload read not entered")
			}
			staged.batch.mu.Lock()
			reserved := staged.batch.reserved
			staged.batch.mu.Unlock()
			if reserved != 4 {
				t.Fatalf("reservation before payload = %d", reserved)
			}
			started := time.Now()
			closeResult := make(chan error, 1)
			spoolRoot := filepath.Dir(staged.spool)
			if action == "cleanup-failure" {
				if err := os.Rename(spoolRoot, spoolRoot+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(spoolRoot, []byte("block removal"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(spoolRoot); _ = os.Rename(spoolRoot+"-saved", spoolRoot) })
			}
			switch action {
			case "explicit-cancel", "unlimited-cancel", "cleanup-failure":
				req, _ := http.NewRequest("POST", f.server.URL()+"/api/command", strings.NewReader(`{"requestId":"cancel-slow-native","name":"transfer.cancel","payload":{"transferId":"slow-native"}}`))
				req.Header.Set("Origin", f.server.URL())
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-CSRF-Token", f.csrf)
				req.AddCookie(f.cookie)
				resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("cancel command: %d", resp.StatusCode)
				}
			case "core-close":
				go func() { closeResult <- f.pair.a.Close() }()
			case "disconnect":
				_ = conn.Close()
			case "request-cancel":
				cancel := <-f.backend.requestCancel
				cancel()
			case "pause":
				mustCommand(t, f.pair.a, "peer.autosave", map[string]any{"peerId": "peer-b", "paused": true})
			case "revoke":
				mustCommand(t, f.pair.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
			}
			awaitNativeUpload(t, f)
			if action != "idle" && action != "staging-timeout" && time.Since(started) > time.Second {
				t.Fatalf("cancellation waited for idle deadline: %v", time.Since(started))
			}
			if action == "core-close" {
				select {
				case err := <-closeResult:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("Core.Close did not join native upload")
				}
			}
			if action != "disconnect" {
				resp := nativeUploadResponse(t, bufio.NewReader(conn))
				if resp.StatusCode < 400 {
					t.Fatalf("interrupted upload accepted: %d", resp.StatusCode)
				}
			}
			if action == "cleanup-failure" {
				staged.batch.mu.Lock()
				retained, spool := staged.batch.reserved, staged.batch.Spool
				staged.batch.mu.Unlock()
				f.pair.a.mu.RLock()
				batch := f.pair.a.outgoing["slow-native"]
				f.pair.a.mu.RUnlock()
				if retained != 4 || spool == "" || batch != staged.batch {
					t.Fatal("failed cleanup released quota")
				}
				if err := os.Remove(spoolRoot); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(spoolRoot+"-saved", spoolRoot); err != nil {
					t.Fatal(err)
				}
				f.pair.a.discardOutgoing(staged.batch)
			}
			requireNativeCleanup(t, f, staged)
		})
	}
}

func TestNativeSlowUploadProgressAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		name     string
		staging  capacity.Choice
		duration time.Duration
		want     int
	}{
		{"progress-longer-than-json", capacity.Limited(10), 5200 * time.Millisecond, 200},
		{"total-expired", capacity.Limited(1), 1300 * time.Millisecond, 400},
		{"unlimited-progress", capacity.Choice{Mode: "unlimited"}, 350 * time.Millisecond, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeUploadFixture(t, 200*time.Millisecond, tc.staging)
			mustCommand(t, f.pair.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
			chunks := int(tc.duration / (50 * time.Millisecond))
			data := strings.Repeat("d", 64*chunks)
			prefix := nativeUploadPrefix("progress-native", len(data), "note.txt")
			conn := f.openUpload(t, prefix, len(prefix)+len(data)+len(nativeUploadSuffix))
			reader := bufio.NewReader(conn)
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			writeFailed := false
			for range chunks {
				<-ticker.C
				if _, err := io.WriteString(conn, strings.Repeat("d", 64)); err != nil {
					writeFailed = true
					break
				}
			}
			if !writeFailed {
				_, _ = io.WriteString(conn, nativeUploadSuffix)
			}
			awaitNativeUpload(t, f)
			resp := nativeUploadResponse(t, reader)
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("upload status=%d want=%d body=%s", resp.StatusCode, tc.want, body)
			}
			if tc.want == 200 {
				if resp.Close {
					t.Fatal("complete upload disabled keep-alive")
				}
				batch := waitOutgoingIdle(t, f.pair.a, "progress-native")
				batch.mu.Lock()
				state, detail := batch.State, batch.Error
				batch.mu.Unlock()
				if state != "completed" {
					t.Fatalf("delivery: %s %s", state, detail)
				}
				// Cancel the old batch after its body guard has finished. Its
				// callback must never expire a reused request's connection.
				batch.stop()
				if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", f.host); err != nil {
					t.Fatal(err)
				}
				next := nativeUploadResponse(t, reader)
				if next.StatusCode != 200 {
					t.Fatalf("next request contaminated: %d", next.StatusCode)
				}
			} else if len(f.pair.b.transfers.List()) != 0 {
				t.Fatal("total-expired upload offered remote batch")
			}
		})
	}
}
