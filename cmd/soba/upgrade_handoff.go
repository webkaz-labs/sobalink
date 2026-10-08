package main

// The Web continuation is an ephemeral local supervisor, not a replacement
// authentication service. Its bootstrap and private results use anonymous pipes
// and same-origin bodies only; all authority expires with the original review.
import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/httpbound"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type webUpgradeRequest struct {
	Intent core.UpgradeIntent `json:"intent"`
	Locale string             `json:"locale"`
}
type upgradeHandoffBootstrap struct {
	Directory, Origin, Locale string
	Old                       upgradeIdentity
	Review                    core.UpgradeReview
	Intent                    core.UpgradeIntent
	Authorization             string
	AuthorizationPayload      json.RawMessage
}
type upgradeHandoffDescriptor struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Deadline string `json:"deadline"`
}

func (l *upgradeLifecycle) webHandoff(app *core.Core) webui.UpgradeHandoff {
	return func(ctx context.Context, origin, authorization string, raw json.RawMessage) (any, error) {
		var in webUpgradeRequest
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || (in.Locale != "en" && in.Locale != "ja") || !validUpgradeUIURL(origin) {
			return nil, errors.New("invalid handoff")
		}
		payload, _ := json.Marshal(in.Intent)
		result, err := app.Command(ctx, webui.Command{RequestID: newCLIRequestID("handoff-review"), Name: "direct-lan.upgrade.review", Payload: payload})
		if err != nil {
			return nil, err
		}
		review, ok := result.(core.UpgradeReview)
		if !ok || !review.RestartRequired || in.Intent.ExpectedRevision == "" || review.Revision != in.Intent.ExpectedRevision || review.PeerID != in.Intent.PeerID || review.Deadline != in.Intent.Deadline {
			return nil, errors.New("review changed")
		}
		if l.stopping.Load() || !l.webPending.CompareAndSwap(false, true) {
			return nil, errors.New("handoff already pending")
		}
		bootstrap := upgradeHandoffBootstrap{Directory: l.dir, Origin: origin, Locale: in.Locale, Old: l.identity, Review: review, Intent: in.Intent, Authorization: authorization, AuthorizationPayload: append(json.RawMessage(nil), raw...)}
		descriptor, done, err := launchUpgradeHandoff(ctx, bootstrap)
		if done != nil {
			go func() { <-done; l.webPending.Store(false) }()
		} else {
			l.webPending.Store(false)
		}
		return descriptor, err
	}
}

func launchUpgradeHandoff(ctx context.Context, bootstrap upgradeHandoffBootstrap) (upgradeHandoffDescriptor, <-chan error, error) {
	var result upgradeHandoffDescriptor
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		return result, nil, err
	}
	cmd := exec.Command(bootstrap.Old.Executable, "__upgrade-handoff")
	cmd.Stdin = bytes.NewReader(raw)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, nil, err
	}
	// nil stderr goes to the null device; private output has no log destination.
	detach(cmd)
	if err := cmd.Start(); err != nil {
		stdout.Close()
		return result, nil, err
	}
	done := make(chan error, 1)
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(io.LimitReader(stdout, 2048)).Decode(&result) }()
	ready := make(chan error, 1)
	go func() { e := <-decoded; stdout.Close(); ready <- e; done <- cmd.Wait() }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err = <-ready:
		if err == nil && (!validUpgradeUIURL(result.URL) || len(result.Token) != 64 || result.Deadline != bootstrap.Intent.Deadline) {
			err = errors.New("invalid helper readiness")
		}
		return result, done, err
	case <-ctx.Done():
		return upgradeHandoffDescriptor{}, done, ctx.Err()
	case <-timer.C:
		return upgradeHandoffDescriptor{}, done, errors.New("helper readiness unknown; no retry or force kill")
	}
}

func runUpgradeHandoffProcess() (int, bool) {
	if len(os.Args) < 2 || os.Args[1] != "__upgrade-handoff" {
		return 0, false
	}
	if len(os.Args) != 2 {
		return 2, true
	}
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, e := f.Stat()
		if e != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return 2, true
		}
	}
	var in upgradeHandoffBootstrap
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return 2, true
	}
	_ = os.Stdin.Close()
	if err := serveUpgradeHandoff(in, os.Stdout); err != nil {
		return 1, true
	}
	return 0, true
}

type handoffState struct {
	mu                       sync.Mutex
	transfer, control, phase string
	in                       upgradeHandoffBootstrap
	until                    time.Time
	cancel                   context.CancelFunc
	start                    func()
	result                   map[string]any
	delivered                bool
	workStarted              bool
	workDone                 chan struct{}
	lastSeen                 time.Time
	open                     func() error
}

func validHandoffBootstrap(in upgradeHandoffBootstrap, now time.Time) bool {
	var request webUpgradeRequest
	if in.Authorization == "" || json.Unmarshal(in.AuthorizationPayload, &request) != nil || request.Intent != in.Intent || request.Locale != in.Locale {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, in.Intent.Deadline)
	return err == nil && until.After(now) && until.Sub(now) <= 5*time.Minute && validUpgradeUIURL(in.Origin) && (in.Locale == "en" || in.Locale == "ja") && in.Old.ProcessID > 0 && in.Old.Instance != "" && in.Intent.PeerID != "" && in.Intent.ExpectedRevision != "" && in.Intent.PeerID == in.Review.PeerID && in.Intent.Deadline == in.Review.Deadline && in.Intent.ExpectedRevision == in.Review.Revision && in.Review.RestartRequired
}

func serveUpgradeHandoff(in upgradeHandoffBootstrap, out *os.File) error {
	if !validHandoffBootstrap(in, time.Now()) {
		return errors.New("invalid bounded review")
	}
	executable, err := os.Executable()
	if err != nil || !sameUpgradeExecutable(executable, in.Old.Executable) {
		return errors.New("helper executable mismatch")
	}
	until, _ := time.Parse(time.RFC3339Nano, in.Intent.Deadline)
	ctx, cancel := context.WithDeadline(context.Background(), until)
	defer cancel()
	transfer, err := upgradeRandomToken()
	if err != nil {
		return err
	}
	control, err := upgradeRandomToken()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	host := ln.Addr().String()
	origin := "http://" + host
	h := &handoffState{transfer: transfer, control: control, phase: "waiting", in: in, until: until, cancel: cancel, workDone: make(chan struct{}), lastSeen: time.Now()}
	h.start = func() { h.workStarted = true; go func() { defer close(h.workDone); h.run(ctx) }() }
	server := &http.Server{Handler: h.handler(origin, host), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10}
	exited := make(chan error, 1)
	go func() { exited <- server.Serve(httpbound.New(4).Wrap(ln)) }()
	if err = json.NewEncoder(out).Encode(upgradeHandoffDescriptor{origin, transfer, in.Intent.Deadline}); err != nil {
		_ = server.Close()
		return err
	}
	_ = out.Close()
	transfer = ""
	control = ""
	lease := time.NewTicker(time.Second)
	defer lease.Stop()
waiting:
	for {
		select {
		case <-ctx.Done():
			break waiting
		case <-exited:
			break waiting
		case now := <-lease.C:
			h.mu.Lock()
			lost := !h.lastSeen.IsZero() && now.Sub(h.lastSeen) > 10*time.Second
			h.mu.Unlock()
			if lost {
				cancel()
				break waiting
			}
		}
	}
	// No profile, credentials or continuation file is left behind.
	h.mu.Lock()
	h.transfer = ""
	h.control = ""
	h.result = nil
	h.open = nil
	started := h.workStarted
	h.mu.Unlock()
	cancel()
	if started {
		<-h.workDone
	}
	h.in.Authorization = ""
	h.in.AuthorizationPayload = nil
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	return server.Shutdown(closeCtx)
}

func (h *handoffState) handler(origin, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(remote) == nil || !net.ParseIP(remote).IsLoopback() || r.Host != host || r.URL.RawQuery != "" {
			http.Error(w, "denied", 403)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, handoffHTML)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/bridge.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			source, _ := json.Marshal(h.in.Origin)
			io.WriteString(w, "const previousOrigin="+string(source)+";\n"+handoffJS)
			return
		}
		if r.Method != "POST" || r.Header.Get("Origin") != origin || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Sec-Fetch-Site") == "cross-site" || r.Header.Get("Sec-Fetch-Site") == "same-site" {
			http.Error(w, "denied", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128)
		var empty struct{}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&empty) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid", 400)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if !time.Now().Before(h.until) || (!h.lastSeen.IsZero() && time.Since(h.lastSeen) > 10*time.Second) {
			h.cancel()
			http.Error(w, "expired", 410)
			return
		}
		token := r.Header.Get("X-Handoff-Token")
		equal := func(expected string) bool {
			return expected != "" && len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
		}
		reply := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
		}
		if r.URL.Path == "/claim" {
			if h.phase != "waiting" || !equal(h.transfer) {
				http.Error(w, "denied", 403)
				return
			}
			h.transfer = ""
			h.phase = "review"
			h.lastSeen = time.Now()
			reply(map[string]any{"token": h.control, "review": h.in.Review, "locale": h.in.Locale})
			return
		}
		if !equal(h.control) {
			http.Error(w, "denied", 403)
			return
		}
		if h.phase == "cancelled" || h.phase == "failed" {
			h.cancel()
			http.Error(w, "expired", 410)
			return
		}
		h.lastSeen = time.Now()
		switch r.URL.Path {
		case "/commit":
			if h.phase != "review" {
				http.Error(w, "already used", 409)
				return
			}
			h.phase = "acknowledgement"
			reply(map[string]string{"state": h.phase})
		case "/ack":
			if h.phase != "acknowledgement" {
				http.Error(w, "already used", 409)
				return
			}
			h.phase = "restarting"
			reply(map[string]string{"state": h.phase})
			h.start()
		case "/cancel":
			h.phase = "cancelled"
			h.cancel()
			reply(map[string]string{"state": "cancelled"})
		case "/open":
			if h.phase != "ready" || !h.delivered || h.open == nil {
				http.Error(w, "unavailable", 409)
				return
			}
			open := h.open
			h.open = nil
			h.mu.Unlock()
			err := open()
			h.mu.Lock()
			state := "open-requested"
			if err != nil {
				state = "manual-open-required"
			}
			reply(map[string]string{"state": state})
		case "/pulse":
			reply(map[string]string{"state": h.phase})
		case "/status":
			if h.result != nil && !h.delivered {
				h.delivered = true
				reply(h.result)
				h.result = nil
				return
			}
			reply(map[string]string{"state": h.phase})
		default:
			http.Error(w, "unknown", 404)
		}
	})
}

func (h *handoffState) run(ctx context.Context) {
	// Bind every modifying request and code issuance to a verified instance.
	pinned := h.in.Old
	bound := func(ctx context.Context, dir, raw string, result any) error {
		if strings.HasPrefix(raw, "{") || raw == "ui" {
			envelope, _ := json.Marshal(upgradeBound{ProcessID: pinned.ProcessID, Instance: pinned.Instance, Command: raw, Authorization: h.in.Authorization, AuthorizationPayload: h.in.AuthorizationPayload})
			return controlCall(ctx, dir, lifecycleBoundPrefix+string(envelope), result)
		}
		if strings.HasPrefix(raw, lifecycleStopPrefix) && h.in.Authorization != "" {
			var stop upgradeStop
			if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleStopPrefix)), &stop) != nil {
				return errors.New("invalid stop")
			}
			stop.Authorization = h.in.Authorization
			stop.AuthorizationPayload = h.in.AuthorizationPayload
			encoded, _ := json.Marshal(stop)
			return controlCall(ctx, dir, lifecycleStopPrefix+string(encoded), result)
		}
		return controlCall(ctx, dir, raw, result)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		payload, _ := json.Marshal(upgradeBound{ProcessID: pinned.ProcessID, Instance: pinned.Instance, CancelIntent: &h.in.Intent})
		_ = controlCall(cleanup, h.in.Directory, lifecycleBoundPrefix+string(payload), nil)
	}()
	query := func(name string, payload any, result any) error {
		raw, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		command, e := json.Marshal(webui.Command{RequestID: newCLIRequestID("web-upgrade"), Name: name, Payload: raw})
		if e != nil {
			return e
		}
		return bound(ctx, h.in.Directory, string(command), result)
	}
	progress, err := applyManagedUpgradeIntent(h.in.Intent, query, func() error {
		var old upgradeIdentity
		if e := controlCall(ctx, h.in.Directory, lifecycleIdentityCommand, &old); e != nil {
			return e
		}
		if old.ProcessID != pinned.ProcessID || old.Instance != pinned.Instance || !sameUpgradeExecutable(old.Executable, pinned.Executable) {
			return errors.New("old identity changed")
		}
		var next upgradeIdentity
		if e := restartManagedUpgradeProcess(ctx, h.in.Directory, h.in.Locale, h.in.Locale == "ja", io.Discard, bound, &old, &next); e != nil {
			return e
		}
		pinned = next
		h.in.Authorization = ""
		h.in.AuthorizationPayload = nil
		return nil
	})
	// Cancellation uses the same exact successor binding; it cannot cancel another
	// peer's or another process's workflow after an uncertain launch.
	if err != nil || ctx.Err() != nil {
		h.mu.Lock()
		h.phase = "failed"
		h.mu.Unlock()
		return
	}
	var ui struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	err = bound(ctx, h.in.Directory, "ui", &ui)
	h.mu.Lock()
	if err != nil || ctx.Err() != nil || !validUpgradeUIURL(ui.URL) || ui.Code == "" {
		h.phase = "failed"
		h.mu.Unlock()
		h.cancel()
		return
	}
	h.phase = "ready"
	address := ui.URL
	h.open = func() error {
		openCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		return openVerifiedUpgradeUI(openCtx, h.in.Directory, pinned, address, controlCall)
	}
	h.result = map[string]any{"state": "ready", "url": ui.URL, "code": ui.Code, "progress": progress}
	ui.Code = ""
	h.mu.Unlock()
	<-ctx.Done()
}

func openVerifiedUpgradeUI(ctx context.Context, dir string, pinned upgradeIdentity, address string, client controlCaller) error {
	if !validUpgradeUIURL(address) {
		return errors.New("invalid management address")
	}
	var current upgradeIdentity
	if err := client(ctx, dir, lifecycleIdentityCommand, &current); err != nil {
		return err
	}
	if current.ProcessID != pinned.ProcessID || current.Instance != pinned.Instance || current.Executable != pinned.Executable {
		return errors.New("successor changed")
	}
	var actual string
	if err := boundUpgradeClient(client, &pinned)(ctx, dir, "ui.url", &actual); err != nil {
		return err
	}
	if actual != address {
		return errors.New("management address changed")
	}
	return launchLoginBrowser(ctx, address)
}
