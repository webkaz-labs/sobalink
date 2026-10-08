//go:build web_activation_native && managed_restart_native && linux

package main

// Opt-in real HTTP/IPC/process/browser harness. In the original seven-case
// mode old/successor owners are SYNTHETIC: no Core or peer transport is opened.
// The separately compiled/opted-in product mode below uses genuine Core owners.
// Both modes retain production authentication, helper and native-exit gates.
import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const webAcceptanceMarker = "SOBALINK_WEB_ACTIVATION_FIXTURE"

// This dispatcher exists only in the explicitly tagged acceptance test binary.
// Internal product argv is exercised unchanged. Original synthetic mode maps
// run --offline to a fictional owner; separately gated product mode calls the
// genuine foreground/Core entry. Unknown modes fail before owner I/O.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--product-browser-scope" {
		os.Exit(runProductBrowserScope())
	}
	if os.Getenv(webAcceptanceMarker) != "1" {
		os.Exit(m.Run())
	}
	dir := os.Getenv("SOBA_ACTIVATION_PROFILE")
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "sr-") {
		os.Exit(91)
	}
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		os.Exit(91)
	}
	owned, e := os.ReadFile(filepath.Join(dir, "fixture-owned"))
	if e != nil || string(owned) != "synthetic-web-activation-only" {
		os.Exit(91)
	}
	if strings.HasPrefix(os.Getenv("SOBA_ACTIVATION_CASE"), "product-core-") {
		os.Exit(runProductActivationDispatch(dir))
	}
	if len(os.Args) == 2 && os.Args[1] == "--web-activation-supervisor" {
		if err := runActivationSupervisor(dir); err != nil {
			os.Exit(95)
		}
		os.Exit(0)
	}
	// Fixture-only self-exit bound, never a product force-kill fallback.
	watchdog := time.AfterFunc(80*time.Second, func() { os.Exit(92) })
	defer watchdog.Stop()
	if len(os.Args) == 2 && os.Args[1] == "__upgrade-handoff" {
		if registerActivationChild(dir, "helper") != nil {
			os.Exit(96)
		}
		launchLoginBrowser = func(_ context.Context, url string) error {
			if !validUpgradeUIURL(url) {
				return errors.New("invalid synthetic browser open")
			}
			return os.WriteFile(filepath.Join(dir, "browser-opened"), []byte("one-use verified bare URL"), 0600)
		}
		code, _ := runUpgradeHandoffProcess()
		os.Exit(code)
	}
	old := len(os.Args) == 2 && os.Args[1] == "--web-activation-fixture"
	successor := len(os.Args) == 7 && os.Args[1] == "--state-dir" && os.Args[2] == dir && os.Args[3] == "--locale" && (os.Args[4] == "en" || os.Args[4] == "ja") && os.Args[5] == "run" && os.Args[6] == "--offline"
	if !old && !successor {
		os.Exit(93)
	}
	role := "old"
	if successor {
		role = "successor"
	}
	if registerActivationChild(dir, role) != nil {
		os.Exit(96)
	}
	if err := runWebAcceptanceOwner(dir, old); err != nil {
		os.Exit(94)
	}
	os.Exit(0)
}

type webAcceptanceBackend struct {
	mu        sync.Mutex
	successor bool
	applied   bool
	intent    core.UpgradeIntent
}

func (b *webAcceptanceBackend) Snapshot(context.Context) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{"syntheticOwner": true, "successor": b.successor, "applied": b.applied}, nil
}
func (*webAcceptanceBackend) Upload(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "fixture has no uploads", 403)
}
func (b *webAcceptanceBackend) Command(_ context.Context, cmd webui.Command) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var in core.UpgradeIntent
	if json.Unmarshal(cmd.Payload, &in) != nil {
		return nil, errors.New("invalid synthetic intent")
	}
	if cmd.Name == "direct-lan.upgrade.review" {
		return webAcceptanceReview(in, !b.successor)
	}
	if cmd.Name != "direct-lan.upgrade.run" || in.ExpectedRevision != "synthetic-web-review" {
		return nil, errors.New("unsupported synthetic command")
	}
	if _, err := webAcceptanceReview(in, !b.successor); err != nil {
		return nil, err
	}
	b.intent = in
	state := "restart-required"
	if b.successor {
		b.applied = true
		state = "network-started"
	}
	// This is a scripted result, NOT actual Core/network activation evidence.
	return core.UpgradeProgress{State: state, PeerID: in.PeerID, Deadline: in.Deadline, RestartRequired: !b.successor}, nil
}
func webAcceptanceReview(in core.UpgradeIntent, restart bool) (core.UpgradeReview, error) {
	until, err := time.Parse(time.RFC3339Nano, in.Deadline)
	if err != nil || !until.After(time.Now()) || until.Sub(time.Now()) > time.Minute || in.PeerID != "synthetic-web-peer" {
		return core.UpgradeReview{}, errors.New("invalid synthetic review")
	}
	return core.UpgradeReview{Revision: "synthetic-web-review", PeerID: in.PeerID, Deadline: in.Deadline, LocalEndpoint: "192.0.2.10:48444", PeerEndpoint: "192.0.2.20:48444", Scope: endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"192.0.2.0/24"}}, RestartRequired: restart}, nil
}
func runWebAcceptanceOwner(dir string, old bool) error {
	marker := "owner-successor-closed"
	if old {
		marker = "owner-old-closed"
	}
	defer os.WriteFile(filepath.Join(dir, marker), []byte("all owner closers returned"), 0600)
	assets := os.Getenv("SOBA_ACTIVATION_ASSETS")
	if !filepath.IsAbs(assets) {
		return errors.New("absolute synthetic assets required")
	}
	if err := config.Protect(dir, true); err != nil {
		return err
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	life, err := newUpgradeLifecycle(dir, !old)
	if err != nil {
		return err
	}
	life.identity.AttemptedNetwork = old // fictional old-owner state, no network opens
	backend := &webAcceptanceBackend{successor: !old}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	var server *webui.Server
	handoff := func(ctx context.Context, origin, authorization string, raw json.RawMessage) (any, error) {
		if !old {
			return nil, errors.New("successor needs no synthetic restart")
		}
		var input webUpgradeRequest
		if json.Unmarshal(raw, &input) != nil || input.Intent.ExpectedRevision != "synthetic-web-review" || (input.Locale != "ja" && input.Locale != "en") {
			return nil, errors.New("invalid synthetic request")
		}
		review, e := webAcceptanceReview(input.Intent, true)
		if e != nil {
			return nil, e
		}
		if !life.webPending.CompareAndSwap(false, true) {
			return nil, errors.New("synthetic helper busy")
		}
		descriptor, done, e := launchUpgradeHandoff(ctx, upgradeHandoffBootstrap{Directory: dir, Origin: origin, Locale: input.Locale, Old: life.identity, Review: review, Intent: input.Intent, Authorization: authorization, AuthorizationPayload: raw})
		if done != nil {
			go func() { <-done; life.webPending.Store(false) }()
		} else {
			life.webPending.Store(false)
		}
		if e == nil {
			e = registerActivationOrigin(dir, "helper", descriptor.URL)
		}
		return descriptor, e
	}
	server, err = webui.Start(ctx, os.DirFS(assets), backend, handoff)
	if err != nil {
		return err
	}
	defer server.Close(context.Background())
	role := "old"
	if !old {
		role = "successor"
	}
	if err := registerActivationOrigin(dir, role, server.URL()); err != nil {
		return err
	}
	// Synthetic adapter around the actual protected owner routes. Core-specific
	// dispatch is intentionally not claimed by this harness.
	stopHandler := life.handler(nil)
	ipc, err := control.Serve(ctx, dir, func(ctx context.Context, raw string) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch raw {
		case lifecycleIdentityCommand:
			return life.identity, nil
		case "status":
			return processStatus{ProcessID: os.Getpid()}, nil
		}
		if strings.HasPrefix(raw, lifecycleBoundPrefix) {
			var request upgradeBound
			if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleBoundPrefix)), &request) != nil || request.ProcessID != life.identity.ProcessID || request.Instance != life.identity.Instance {
				return nil, errors.New("synthetic instance mismatch")
			}
			if request.Authorization != "" {
				if e := server.CheckUpgradeAuthorization(request.Authorization, request.AuthorizationPayload, false); e != nil {
					return nil, e
				}
			}
			if request.CancelIntent != nil {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				if backend.intent != *request.CancelIntent {
					return nil, errors.New("synthetic cancel mismatch")
				}
				return map[string]bool{"cancelled": true}, nil
			}
			if request.Command == "ui.url" {
				return server.URL(), nil
			}
			if request.Command == "ui" {
				code, e := server.IssueCode()
				return map[string]string{"url": server.URL(), "code": code}, e
			}
			var cmd webui.Command
			if json.Unmarshal([]byte(request.Command), &cmd) != nil {
				return nil, errors.New("invalid synthetic bound command")
			}
			return backend.Command(ctx, cmd)
		}
		if strings.HasPrefix(raw, lifecycleStopPrefix) {
			var request upgradeStop
			if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleStopPrefix)), &request) != nil {
				return nil, errors.New("invalid synthetic stop")
			}
			if old && os.Getenv("SOBA_ACTIVATION_CASE") == "pause-before-stop" {
				if e := os.WriteFile(filepath.Join(dir, "stop-admission-waiting"), []byte("session check has not happened"), 0600); e != nil {
					return nil, e
				}
				tick := time.NewTicker(25 * time.Millisecond)
				defer tick.Stop()
				for {
					if _, e := os.Stat(filepath.Join(dir, "permit-stop")); e == nil {
						break
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-tick.C:
					}
				}
			}
			if old {
				if request.Authorization == "" {
					return nil, errors.New("session proof required")
				}
				if e := server.CheckUpgradeAuthorization(request.Authorization, request.AuthorizationPayload, true); e != nil {
					_ = os.WriteFile(filepath.Join(dir, "session-stop-denied"), []byte("session proof denied"), 0600)
					return nil, e
				}
			}
			request.Authorization = ""
			request.AuthorizationPayload = nil
			encoded, _ := json.Marshal(request)
			return stopHandler(ctx, lifecycleStopPrefix+string(encoded))
		}
		return nil, errors.New("unsupported synthetic IPC")
	})
	if err != nil {
		return err
	}
	defer ipc.Close()
	if old {
		code, e := server.IssueCode()
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(map[string]any{"url": server.URL(), "code": code, "syntheticOwner": true})
		if e = config.AtomicWritePrivate(filepath.Join(dir, "session.json"), raw); e != nil {
			return e
		}
	} else {
		if err = os.WriteFile(filepath.Join(dir, "successor-started"), []byte("synthetic offline owner"), 0600); err != nil {
			return err
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case request := <-life.stop:
			closeErr, lockErr := shutdownManagedOwners(ipc, restartCloser(func() error {
				closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				return server.Close(closeCtx)
			}), lock)
			if os.Getenv("SOBA_ACTIVATION_CASE") == "lost-ack" {
				return errors.Join(closeErr, lockErr)
			}
			if err := acknowledgeUpgradeShutdown(request, closeErr, lockErr); err != nil {
				return err
			}
			return errors.Join(closeErr, lockErr)
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if _, e := os.Stat(filepath.Join(dir, "stop-fixture")); e == nil {
				return nil
			} else if !errors.Is(e, fs.ErrNotExist) {
				return e
			}
		}
	}
}
