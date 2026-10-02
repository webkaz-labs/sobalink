package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

type testBackend struct {
	commands     atomic.Int32
	uploads      atomic.Int32
	reads        atomic.Int32
	commandError error
}

func (b *testBackend) Snapshot(context.Context) (map[string]any, error) {
	b.reads.Add(1)
	return map[string]any{"version": "test"}, nil
}
func (b *testBackend) Command(context.Context, Command) (any, error) {
	b.commands.Add(1)
	if b.commandError != nil {
		return nil, b.commandError
	}
	return map[string]bool{"accepted": true}, nil
}

type testCommandError string

func (e testCommandError) Error() string     { return "safe recovery message" }
func (e testCommandError) ErrorCode() string { return string(e) }

func TestCommandErrorCodesSupportBoundedRecovery(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("ordinary failure"), "command_failed"},
		{fmt.Errorf("wrapped: %w", testCommandError("lan_pair_reply_uncertain")), "lan_pair_reply_uncertain"},
		{testCommandError("lan_revoke_not_persisted"), "lan_revoke_not_persisted"},
		{testCommandError("secret?token=value"), "command_failed"},
		{testCommandError(strings.Repeat("a", 65)), "command_failed"},
		{testCommandError(""), "command_failed"},
	} {
		s, backend := testServer(t)
		backend.commandError = tc.err
		cookie, csrf := signIn(t, s)
		response := serve(s, request(s, "POST", "/api/command", `{"requestId":"test","name":"lan.join","payload":{}}`, cookie, csrf))
		var result struct {
			Code string `json:"code"`
		}
		if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Code != tc.want {
			t.Fatalf("wrong command error: %d %s", response.Code, response.Body.String())
		}
	}
}
func (b *testBackend) Upload(w http.ResponseWriter, _ *http.Request) {
	b.uploads.Add(1)
	w.WriteHeader(http.StatusOK)
}

func testServer(t *testing.T) (*Server, *testBackend) {
	t.Helper()
	b := new(testBackend)
	s := &Server{backend: b, assets: fstest.MapFS{"index.html": {Data: []byte("local UI")}}, host: "127.0.0.1:45678", url: "http://127.0.0.1:45678", sessions: map[string]session{}, slots: make(chan struct{}, 16)}
	if _, err := s.IssueCode(); err != nil {
		t.Fatal(err)
	}
	return s, b
}

func request(s *Server, method, path, body string, cookie *http.Cookie, csrf string) *http.Request {
	r := httptest.NewRequest(method, s.url+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:32123"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.url)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	return r
}

func serve(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func signIn(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, nil, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("sign in: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	c := cookies[0]
	if c.Name != "soba_session" || c.Value == "" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 8*60*60 {
		t.Fatalf("unsafe or incomplete session cookie: %+v", c)
	}
	var result struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.CSRF == "" || result.CSRF == c.Value || result.CSRF == code {
		t.Fatalf("invalid CSRF response: %s, %v", w.Body.String(), err)
	}
	return c, result.CSRF
}

func TestManagementRejectsForeignHostsOriginsAndAddresses(t *testing.T) {
	s, b := testServer(t)
	cookie, csrf := signIn(t, s)
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{"hostname-alias", func(r *http.Request) { r.Host = "localhost:45678" }},
		{"dns-rebinding", func(r *http.Request) { r.Host = "attacker.invalid:45678" }},
		{"wrong-port", func(r *http.Request) { r.Host = "127.0.0.1:45679" }},
		{"nonlocal-source", func(r *http.Request) { r.RemoteAddr = "100.64.0.1:32123" }},
		{"malformed-source", func(r *http.Request) { r.RemoteAddr = "invalid" }},
		{"foreign-origin", func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }},
		{"null-origin", func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"missing-origin", func(r *http.Request) { r.Header.Del("Origin") }},
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"same-site-other-origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request(s, "POST", "/api/command", `{"requestId":"one","name":"settings.update","payload":{}}`, cookie, csrf)
			tc.edit(r)
			if w := serve(s, r); w.Code != http.StatusForbidden {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if b.commands.Load() != 0 {
		t.Fatal("refused requests reached command backend")
	}
}

func TestManagementAuthenticationCSRFAndReadOnlyMethods(t *testing.T) {
	s, b := testServer(t)
	for _, path := range []string{"/api/state", "/api/command", "/api/upload"} {
		if w := serve(s, request(s, "GET", path, "", nil, "")); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	cookie, csrf := signIn(t, s)
	for _, token := range []string{"", "wrong-token"} {
		for _, path := range []string{"/api/command", "/api/upload", "/api/session"} {
			method := "POST"
			if path == "/api/session" {
				method = "DELETE"
			}
			if w := serve(s, request(s, method, path, `{}`, cookie, token)); w.Code != http.StatusForbidden {
				t.Fatalf("invalid CSRF %s: %d", path, w.Code)
			}
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"/api/command", "/api/upload", "/api/session"} {
			if w := serve(s, request(s, method, path, `{}`, cookie, csrf)); w.Code != http.StatusNotFound {
				t.Fatalf("read method %s %s: %d", method, path, w.Code)
			}
		}
	}
	if b.commands.Load() != 0 || b.uploads.Load() != 0 {
		t.Fatal("CSRF or read-only request reached mutation backend")
	}
	r := request(s, "GET", "/api/state", "", cookie, "")
	r.Header.Del("Origin")
	w := serve(s, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), csrf) {
		t.Fatalf("authenticated state: %d %s", w.Code, w.Body.String())
	}
	for _, header := range []string{"Cache-Control", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy"} {
		if w.Header().Get(header) == "" {
			t.Errorf("missing security header %s", header)
		}
	}
	if w := serve(s, request(s, "POST", "/api/command", `{"requestId":"one","name":"settings.update","payload":{}}`, cookie, csrf)); w.Code != http.StatusOK || b.commands.Load() != 1 {
		t.Fatalf("authorized command: %d %s", w.Code, w.Body.String())
	}
	if w := serve(s, request(s, "DELETE", "/api/session", "", cookie, csrf)); w.Code != http.StatusOK {
		t.Fatalf("logout: %d", w.Code)
	} else if cs := w.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge != -1 {
		t.Fatalf("logout did not expire cookie: %v", cs)
	}
	if w := serve(s, request(s, "GET", "/api/state", "", cookie, "")); w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session remained valid: %d", w.Code)
	}
}

func TestManagementCodeRotationReuseAndExpiry(t *testing.T) {
	s, _ := testServer(t)
	old, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	login := func(code string) *httptest.ResponseRecorder {
		return serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, nil, ""))
	}
	if w := login(old); w.Code != http.StatusUnauthorized {
		t.Fatalf("rotated code accepted: %d", w.Code)
	}
	if w := login(current); w.Code != http.StatusOK {
		t.Fatalf("current code refused: %d", w.Code)
	}
	if w := login(current); w.Code != http.StatusUnauthorized {
		t.Fatalf("used code accepted: %d", w.Code)
	}
	expired, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.codeUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if w := login(expired); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired code accepted: %d", w.Code)
	}
	cookie, _ := signIn(t, s)
	s.mu.Lock()
	value := s.sessions[cookie.Value]
	value.expires = time.Now().Add(-time.Second)
	s.sessions[cookie.Value] = value
	s.mu.Unlock()
	if w := serve(s, request(s, "GET", "/api/state", "", cookie, "")); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session accepted: %d", w.Code)
	}
}

func TestManagementOneTimeCodeIsAtomicAndAttemptsBounded(t *testing.T) {
	s, _ := testServer(t)
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, nil, "")).Code == http.StatusOK {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("code used successfully %d times", successes.Load())
	}
	for range 2 {
		serve(s, request(s, "POST", "/api/session", `{"code":"wrong"}`, nil, ""))
	}
	newCode, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(s, request(s, "POST", "/api/session", `{"code":"`+newCode+`"}`, nil, "")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("issuing another code bypassed rate limit: %d", w.Code)
	}
}

func TestManagementRejectsMalformedAndOversizedJSON(t *testing.T) {
	s, b := testServer(t)
	cookie, csrf := signIn(t, s)
	for _, body := range []string{
		`{"requestId":"one","name":"settings.update","payload":{},"unexpected":true}`,
		`{"requestId":"one","name":"settings.update","payload":{}} {}`,
		`{"requestId":"","name":"settings.update","payload":{}}`,
		`{"requestId":"one","name":"settings.update","payload":{"value":"` + strings.Repeat("x", 64<<10) + `"}}`,
	} {
		if w := serve(s, request(s, "POST", "/api/command", body, cookie, csrf)); w.Code != http.StatusBadRequest {
			t.Fatalf("malformed body accepted: %d", w.Code)
		}
	}
	r := request(s, "POST", "/api/command", `{"requestId":"one","name":"settings.update","payload":{}}`, cookie, csrf)
	r.Header.Set("Content-Type", "text/plain")
	if w := serve(s, r); w.Code != http.StatusBadRequest {
		t.Fatalf("non-JSON command accepted: %d", w.Code)
	}
	if b.commands.Load() != 0 {
		t.Fatal("invalid JSON reached command backend")
	}
}

func TestManagementBoundsSessionsAndReclaimsExpiredEntries(t *testing.T) {
	s, _ := testServer(t)
	for range 8 {
		signIn(t, s)
	}
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, nil, "")); w.Code != http.StatusConflict {
		t.Fatalf("unbounded session creation: %d", w.Code)
	}
	s.mu.Lock()
	for token, value := range s.sessions {
		value.expires = time.Now().Add(-time.Second)
		s.sessions[token] = value
		break
	}
	s.mu.Unlock()
	if w := serve(s, request(s, "POST", "/api/session", `{"code":"`+code+`"}`, nil, "")); w.Code != http.StatusOK {
		t.Fatalf("expired session was not reclaimed: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	count := len(s.sessions)
	s.mu.Unlock()
	if count != 8 {
		t.Fatalf("session count: %d", count)
	}
}
