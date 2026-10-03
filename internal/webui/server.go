// Package webui serves the local management surface. Peer protocols must never
// use this handler: its authority belongs to the local operating-system user.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/httpbound"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Command struct {
	RequestID string          `json:"requestId"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
}

type Backend interface {
	Snapshot(context.Context) (map[string]any, error)
	Command(context.Context, Command) (any, error)
	Upload(http.ResponseWriter, *http.Request)
}

type session struct {
	csrf    string
	expires time.Time
}

type Server struct {
	backend       Backend
	assets        fs.FS
	listener      net.Listener
	http          *http.Server
	url, host     string
	mu            sync.Mutex
	code          string
	codeUntil     time.Time
	sessions      map[string]session
	attempts      int
	attemptWindow time.Time
	slots         chan struct{}
	done          chan error
}

func randomToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Start always binds a fresh numeric loopback port. No configurable wildcard,
// LAN address, or peer listener can broaden the management surface.
func Start(ctx context.Context, assets fs.FS, backend Backend) (*Server, error) {
	if backend == nil || assets == nil {
		return nil, errors.New("management backend and assets required")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ln = httpbound.New(32).Wrap(ln)
	s := &Server{backend: backend, assets: assets, listener: ln, host: ln.Addr().String(), sessions: map[string]session{}, slots: make(chan struct{}, 16), done: make(chan error, 1)}
	s.url = "http://" + s.host
	if _, err := s.IssueCode(); err != nil {
		ln.Close()
		return nil, err
	}
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
	}()
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Close(closeCtx)
	}()
	return s, nil
}

func (s *Server) URL() string  { return s.url }
func (s *Server) Port() uint16 { ap, _ := netip.ParseAddrPort(s.host); return ap.Port() }
func (s *Server) IssueCode() (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
	s.codeUntil = time.Now().Add(5 * time.Minute)
	return code, nil
}
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.code = ""
	clear(s.sessions)
	s.mu.Unlock()
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	return err
}

func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	jsonReply(w, status, map[string]string{"code": code, "error": message})
}

// Error codes are stable local API identifiers, never arbitrary payload data.
// The human message stays separate so clients can offer localized recovery.
func commandErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return "command_failed"
	}
	code := coded.ErrorCode()
	if len(code) == 0 || len(code) > 64 {
		return "command_failed"
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return "command_failed"
		}
	}
	return code
}
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON content type required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errCommandTooLarge
		}
		return errors.New("invalid request")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errCommandTooLarge
		}
		return errors.New("unexpected request data")
	}
	return nil
}

var errCommandTooLarge = errors.New("local command exceeds its JSON envelope limit; shorten the text or reduce the command payload")

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(ip) == nil || !net.ParseIP(ip).IsLoopback() || r.Host != s.host {
		fail(w, http.StatusForbidden, "local_only", "Use the exact local address printed by soba")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		fail(w, http.StatusForbidden, "origin", "Cross-origin management requests are refused")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.url {
		fail(w, http.StatusForbidden, "origin", "Origin does not match the local application")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != s.url {
		fail(w, http.StatusForbidden, "origin", "A same-origin request is required")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, 503, "busy", "Local application is busy; retry shortly")
		return
	}
	if r.URL.Path == "/api/session" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		cookie, err := r.Cookie("soba_session")
		if err != nil {
			fail(w, 401, "unauthenticated", "Enter the one-time code shown by soba")
			return
		}
		s.mu.Lock()
		session, ok := s.sessions[cookie.Value]
		if ok && !time.Now().Before(session.expires) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			fail(w, 401, "unauthenticated", "The local session expired; request a new code with soba ui")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.csrf)) != 1 {
			fail(w, 403, "csrf", "Refresh the local page and retry")
			return
		}
		switch {
		case r.URL.Path == "/api/state" && r.Method == "GET":
			state, err := s.backend.Snapshot(r.Context())
			if err != nil {
				fail(w, 503, "unavailable", err.Error())
				return
			}
			state["csrfToken"] = session.csrf
			jsonReply(w, 200, state)
		case r.URL.Path == "/api/command" && r.Method == "POST":
			var cmd Command
			if err := decode(w, r, int64(messageframe.CommandBytes), &cmd); err != nil {
				if errors.Is(err, errCommandTooLarge) {
					fail(w, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
					return
				}
				fail(w, 400, "invalid", err.Error())
				return
			}
			if len(cmd.RequestID) < 1 || len(cmd.RequestID) > messageframe.RequestIDBytes || len(cmd.Name) > messageframe.CommandNameBytes {
				fail(w, 400, "invalid", "A bounded request ID and command are required")
				return
			}
			result, err := s.backend.Command(r.Context(), cmd)
			if err != nil {
				fail(w, 400, commandErrorCode(err), err.Error())
				return
			}
			jsonReply(w, 200, map[string]any{"ok": true, "result": result})
		case r.URL.Path == "/api/upload" && r.Method == "POST":
			s.backend.Upload(w, r)
		case r.URL.Path == "/api/session" && r.Method == "DELETE":
			s.mu.Lock()
			delete(s.sessions, cookie.Value)
			s.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "soba_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			jsonReply(w, 200, map[string]bool{"ok": true})
		default:
			fail(w, 404, "not_found", "Unknown local API route")
		}
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "method", "Read-only asset route")
		return
	}
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/favicon.svg" {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if err := decode(w, r, 4096, &input); err != nil {
		fail(w, 400, "invalid", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.attemptWindow) >= time.Minute {
		s.attempts = 0
		s.attemptWindow = now
	}
	if s.attempts >= 10 {
		fail(w, 429, "rate_limited", "Too many attempts; wait one minute")
		return
	}
	s.attempts++
	if s.code == "" || !now.Before(s.codeUntil) || subtle.ConstantTimeCompare([]byte(input.Code), []byte(s.code)) != 1 {
		fail(w, 401, "invalid_code", "The code is invalid or expired; use soba ui for a new code")
		return
	}
	token, err := randomToken()
	if err != nil {
		fail(w, 500, "random", "Could not create local session")
		return
	}
	csrf, err := randomToken()
	if err != nil {
		fail(w, 500, "random", "Could not create local session")
		return
	}
	for key, value := range s.sessions {
		if !now.Before(value.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 8 {
		fail(w, 409, "sessions", "Close another local session before signing in")
		return
	}
	s.code = ""
	s.sessions[token] = session{csrf: csrf, expires: now.Add(8 * time.Hour)}
	http.SetCookie(w, &http.Cookie{Name: "soba_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	jsonReply(w, 200, map[string]string{"csrfToken": csrf})
}
