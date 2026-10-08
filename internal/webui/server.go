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
	"math"
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

// UpgradeHandoff is installed only by the local lifecycle owner. It is never
// exposed through peer protocols or the generic command API.
type UpgradeHandoff func(context.Context, string, string, json.RawMessage) (any, error)

type session struct {
	csrf    string
	expires time.Time
}

type Server struct {
	handoff       UpgradeHandoff
	backend       Backend
	assets        fs.FS
	listener      net.Listener
	http          *http.Server
	url, host     string
	mu            sync.Mutex
	code          string
	codeUntil     time.Time
	sessions      map[string]session
	handoffs      map[string]upgradeAuthorization
	attempts      int
	attemptWindow time.Time
	slots         chan struct{}
	done          chan error
	closeSignal   chan struct{}
	watcherDone   chan struct{}
	serveDone     chan struct{}
	closeOnce     sync.Once
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
func Start(ctx context.Context, assets fs.FS, backend Backend, handoff ...UpgradeHandoff) (*Server, error) {
	if backend == nil || assets == nil {
		return nil, errors.New("management backend and assets required")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ln = httpbound.New(32).Wrap(ln)
	s := &Server{backend: backend, assets: assets, listener: ln, host: ln.Addr().String(), sessions: map[string]session{}, slots: make(chan struct{}, 16), done: make(chan error, 1), closeSignal: make(chan struct{}), watcherDone: make(chan struct{}), serveDone: make(chan struct{})}
	if len(handoff) == 1 {
		s.handoff = handoff[0]
	}
	s.url = "http://" + s.host
	if _, err := s.IssueCode(); err != nil {
		ln.Close()
		return nil, err
	}
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }, ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, bodyConnectionKey{}, conn)
	}}
	go func() {
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.done <- err
		close(s.serveDone)
	}()
	go func() {
		defer close(s.watcherDone)
		select {
		case <-ctx.Done():
		case <-s.closeSignal:
		case <-s.serveDone:
		}
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
	s.closeOnce.Do(func() { close(s.closeSignal) })
	s.mu.Lock()
	s.code = ""
	clear(s.sessions)
	for _, grant := range s.handoffs {
		if grant.timer != nil {
			grant.timer.Stop()
		}
	}
	clear(s.handoffs)
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
	body, err := BoundBody(w, r, 5*time.Second, false)
	if err != nil {
		return err
	}
	defer body.Finish(false)
	if err := decodeJSON(w, r, limit, v); err != nil {
		return err
	}
	return body.Finish(true)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
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
		case r.URL.Path == "/api/upgrade-handoff" && r.Method == "POST":
			if s.handoff == nil {
				fail(w, 503, "unavailable", "Local restart is unavailable")
				return
			}
			var payload json.RawMessage
			if decode(w, r, 2048, &payload) != nil {
				fail(w, 400, "invalid", "Invalid handoff request")
				return
			}
			authorization, err := s.issueUpgradeAuthorization(cookie.Value, session, payload)
			if err != nil {
				fail(w, 401, "unauthenticated", "The local session expired")
				return
			}
			result, err := s.handoff(r.Context(), s.url, authorization, payload)
			if err != nil {
				s.discardUpgradeAuthorization(authorization)
				fail(w, 400, "handoff_failed", "Restart handoff was not established; inspect current state before retrying")
				return
			}
			jsonReply(w, 200, result)
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
			limit := int64(messageframe.CommandBytes)
			if bounded, ok := s.backend.(interface{ CommandRequestBytes() int64 }); ok {
				limit = bounded.CommandRequestBytes()
				if limit < 1 || limit >= math.MaxInt64 {
					fail(w, 503, "unavailable", "Invalid local command budget")
					return
				}
			}
			if err := decode(w, r, limit, &cmd); err != nil {
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
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/favicon.svg" && r.URL.Path != "/png-worker-manifest.json" {
		http.NotFound(w, r)
		return
	}
	if s.servePNGWorker(w, r) {
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

type bodyConnectionKey struct{}

type bodyCancellation struct {
	stop func() bool
	done chan struct{}
}

// RequestBody bounds native socket reads without waiting for the HTTP body's
// Read/Close mutex. Finish must join cancellation before connection reuse.
type RequestBody struct {
	source                                     io.ReadCloser
	controller                                 *http.ResponseController
	connection                                 net.Conn
	writer                                     http.ResponseWriter
	mu                                         sync.Mutex
	window                                     time.Duration
	rolling, library, cancelled, eof, finished bool
	deadline                                   time.Time
	progressDeadline                           time.Time
	contexts                                   []context.Context
	callbacks                                  []bodyCancellation
	err                                        error
}

// BoundBody installs the read window before the first body read. A rolling
// window advances only on bytes read; watched context deadlines cap that window.
// Close interruption is retained for non-server library readers only.
func BoundBody(w http.ResponseWriter, r *http.Request, window time.Duration, rolling bool) (*RequestBody, error) {
	b := &RequestBody{source: r.Body, controller: http.NewResponseController(w), writer: w, window: window, rolling: rolling}
	b.connection, _ = r.Context().Value(bodyConnectionKey{}).(net.Conn)
	w.Header().Set("Connection", "close")
	if window <= 0 {
		return nil, errors.New("positive request read window required")
	}
	deadline := time.Now().Add(window)
	b.progressDeadline = deadline
	if err := b.controller.SetReadDeadline(deadline); err != nil {
		if errors.Is(err, http.ErrNotSupported) && r.Context().Value(http.ServerContextKey) == nil {
			b.library = true
		} else {
			if b.connection != nil {
				_ = b.connection.Close()
			}
			return nil, err
		}
	}
	if !rolling {
		b.deadline = deadline
	}
	r.Body = b
	if err := b.Watch(r.Context()); err != nil {
		_ = b.Finish(false)
		return nil, err
	}
	return b, nil
}

// Watch adds a cancellation source and its absolute deadline. Call only from
// the request handler, before Finish, including when staging gains a batch life.
func (b *RequestBody) Watch(ctx context.Context) error {
	b.mu.Lock()
	b.contexts = append(b.contexts, ctx)
	if deadline, ok := ctx.Deadline(); ok && (b.deadline.IsZero() || deadline.Before(b.deadline)) {
		b.deadline = deadline
	}
	err := b.refreshLocked()
	b.mu.Unlock()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		b.mu.Lock()
		b.cancelled = true
		_ = b.setLocked(time.Now())
		b.mu.Unlock()
		if b.library {
			_ = b.source.Close()
		}
	})
	b.callbacks = append(b.callbacks, bodyCancellation{stop, done})
	return err
}

func (b *RequestBody) setLocked(deadline time.Time) error {
	if b.library {
		return nil
	}
	err := b.controller.SetReadDeadline(deadline)
	if err != nil {
		b.err = err
		if b.connection != nil {
			_ = b.connection.Close()
		}
	}
	return err
}

func (b *RequestBody) refreshLocked() error {
	for _, ctx := range b.contexts {
		if err := ctx.Err(); err != nil {
			b.cancelled = true
			b.err = err
		}
	}
	if b.cancelled || b.err != nil {
		_ = b.setLocked(time.Now())
		if b.err != nil {
			return b.err
		}
		return context.Canceled
	}
	deadline := b.progressDeadline
	if !b.deadline.IsZero() && b.deadline.Before(deadline) {
		deadline = b.deadline
	}
	return b.setLocked(deadline)
}

func (b *RequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	err := b.err
	if b.cancelled && err == nil {
		err = context.Canceled
	}
	b.mu.Unlock()
	if err != nil {
		return 0, err
	}
	n, err := b.source.Read(p)
	b.mu.Lock()
	if err == io.EOF {
		b.eof = true
	} else if err != nil {
		b.err = err
	}
	if n > 0 && b.rolling && (err == nil || err == io.EOF) {
		b.progressDeadline = time.Now().Add(b.window)
		if refreshErr := b.refreshLocked(); refreshErr != nil {
			err = refreshErr
		}
	}
	b.mu.Unlock()
	return n, err
}

func (b *RequestBody) Close() error {
	b.mu.Lock()
	b.cancelled = true
	err := b.setLocked(time.Now())
	b.mu.Unlock()
	closeErr := b.source.Close()
	return errors.Join(err, closeErr)
}

// Finish permits reuse only after complete successful input and after every
// cancellation callback has stopped or returned. Failed input retains an expired
// deadline through net/http's implicit body cleanup.
func (b *RequestBody) Finish(success bool) error {
	if b.finished {
		return b.err
	}
	for _, callback := range b.callbacks {
		if !callback.stop() {
			<-callback.done
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finished = true
	for _, ctx := range b.contexts {
		if err := ctx.Err(); err != nil {
			b.cancelled = true
			b.err = err
		}
	}
	if success && b.eof && !b.cancelled && b.err == nil {
		if err := b.setLocked(time.Time{}); err != nil {
			return err
		}
		b.writer.Header().Del("Connection")
		return nil
	}
	_ = b.setLocked(time.Now())
	if b.err != nil {
		return b.err
	}
	if success {
		return errors.New("request body was not completed")
	}
	return nil
}
