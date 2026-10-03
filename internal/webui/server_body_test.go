package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func nativeSignIn(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", s.URL()+"/api/session", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Origin", s.URL())
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || result.CSRF == "" || len(resp.Cookies()) != 1 {
		t.Fatalf("sign in: %d %+v", resp.StatusCode, result)
	}
	return resp.Cookies()[0], result.CSRF
}

func waitNativeSlots(t *testing.T, s *Server, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(s.slots) != want {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("slots = %d, want %d", len(s.slots), want)
		}
	}
}

func TestNativePartialBodyRecoversSlots(t *testing.T) {
	for _, tc := range []struct {
		name, path, payload, framing string
		authenticated                bool
	}{
		{"session", "/api/session", `{"code":"`, "Content-Length: 64\r\n", false},
		{"command", "/api/command", `{"requestId":"`, "Content-Length: 64\r\n", true},
		{"chunked", "/api/session", "9\r\n{\"code\":\"\r\n", "Transfer-Encoding: chunked\r\n", false},
		{"trailing-eof", "/api/session", `{"code":"wrong"}`, "Content-Length: 64\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := new(testBackend)
			s, err := Start(context.Background(), fstest.MapFS{"index.html": {Data: []byte("UI")}}, b)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			var auth string
			if tc.authenticated {
				cookie, csrf := nativeSignIn(t, s)
				auth = "Cookie: " + cookie.String() + "\r\nX-CSRF-Token: " + csrf + "\r\n"
			}
			for range 16 {
				conn, err := net.DialTimeout("tcp4", s.host, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Type: application/json\r\n%s%s\r\n%s", tc.path, s.host, s.url, auth, tc.framing, tc.payload)
				if err != nil {
					t.Fatal(err)
				}
			}
			waitNativeSlots(t, s, 16, time.Second)
			client := &http.Client{Timeout: time.Second}
			resp, err := client.Get(s.URL())
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != 503 {
				t.Fatalf("saturated status = %d", resp.StatusCode)
			}
			waitNativeSlots(t, s, 0, 6*time.Second)
			resp, err = client.Get(s.URL())
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("recovered status = %d", resp.StatusCode)
			}
			if b.commands.Load() != 0 {
				t.Fatal("incomplete command executed")
			}
		})
	}
}

func TestNativeJSONKeepAliveAfterSuccessfulBody(t *testing.T) {
	b := new(testBackend)
	s, err := Start(context.Background(), fstest.MapFS{"index.html": {Data: []byte("UI")}}, b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	cookie, csrf := nativeSignIn(t, s)
	conn, err := net.DialTimeout("tcp4", s.host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	for i := range 2 {
		body := fmt.Sprintf(`{"requestId":"native-%d","name":"test","payload":{}}`, i)
		fmt.Fprintf(conn, "POST /api/command HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\nX-CSRF-Token: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", s.host, s.url, cookie.String(), csrf, len(body), body)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || resp.Close {
			t.Fatalf("reused response: %+v %v", resp, err)
		}
	}
	if b.commands.Load() != 2 {
		t.Fatal("keep-alive command lost")
	}
}

type nativeDeadlineFailureBackend struct {
	lifecycleBackend
	fail     string
	entered  chan context.CancelFunc
	finished chan error
}

type nativeDeadlineFailureWriter struct {
	http.ResponseWriter
	fail  string
	calls int
}

func (w *nativeDeadlineFailureWriter) SetReadDeadline(deadline time.Time) error {
	w.calls++
	if w.fail == "unsupported" && w.calls == 1 {
		return http.ErrNotSupported
	}
	if (w.fail == "initial" && w.calls == 1) || (w.fail == "progress" && w.calls == 3) || (w.fail == "cancel" && !deadline.IsZero() && !deadline.After(time.Now())) || (w.fail == "clear" && deadline.IsZero()) {
		return errors.New("injected deadline setter failure")
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}
func (b *nativeDeadlineFailureBackend) Upload(w http.ResponseWriter, r *http.Request) {
	wrapped := &nativeDeadlineFailureWriter{ResponseWriter: w, fail: b.fail}
	body, err := BoundBody(wrapped, r, time.Second, true)
	if err != nil {
		b.finished <- err
		return
	}
	defer body.Finish(false)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if b.fail == "cancel" {
		if err := body.Watch(ctx); err != nil {
			b.finished <- err
			return
		}
	}
	b.entered <- cancel
	_, err = io.Copy(io.Discard, r.Body)
	if err == nil {
		err = body.Finish(true)
	}
	b.finished <- err
}

func TestNativeBodyDeadlineSetterErrorsAbortConnection(t *testing.T) {
	for _, failure := range []string{"initial", "unsupported", "progress", "cancel", "clear"} {
		t.Run(failure, func(t *testing.T) {
			backend := &nativeDeadlineFailureBackend{fail: failure, entered: make(chan context.CancelFunc, 1), finished: make(chan error, 1)}
			s, err := Start(context.Background(), fstest.MapFS{}, backend)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			cookie, csrf := nativeSignIn(t, s)
			conn, err := net.DialTimeout("tcp4", s.host, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			length, payload := 4, "d"
			if failure == "clear" {
				length, payload = 1, "d"
			}
			if failure == "cancel" {
				payload = ""
			}
			fmt.Fprintf(conn, "POST /api/upload HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\nX-CSRF-Token: %s\r\nContent-Length: %d\r\n\r\n%s", s.host, s.url, cookie.String(), csrf, length, payload)
			if failure == "cancel" {
				select {
				case cancel := <-backend.entered:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("native body not entered")
				}
			}
			select {
			case err := <-backend.finished:
				if err == nil {
					t.Fatal("deadline setter failure silently ignored")
				}
			case <-time.After(time.Second):
				t.Fatal("setter failure did not unblock native body")
			}
			var byte [1]byte
			_, err = conn.Read(byte[:])
			if err == nil {
				t.Fatal("deadline failure kept connection open")
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("connection was not aborted: %v", err)
			}
		})
	}
}

type joiningLibraryBody struct {
	io.Reader
	entered, release chan struct{}
}

func (b *joiningLibraryBody) Close() error {
	close(b.entered)
	<-b.release
	return nil
}

func TestBodyFinishJoinsRunningCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &joiningLibraryBody{Reader: strings.NewReader("complete"), entered: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/upload", nil).WithContext(ctx)
	r.Body = source
	body, err := BoundBody(httptest.NewRecorder(), r, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("cancellation not started")
	}
	finished := make(chan error, 1)
	go func() { finished <- body.Finish(true) }()
	select {
	case <-finished:
		t.Fatal("body guard returned before cancellation completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(source.release)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled body reused: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("body guard did not join cancellation")
	}
}
