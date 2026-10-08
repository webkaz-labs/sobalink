package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const lifecycleIdentityCommand = "lifecycle.upgrade.identity"
const lifecycleBoundPrefix = "lifecycle.upgrade.bound "

type upgradeBound struct {
	ProcessID            int                 `json:"processId"`
	Instance             string              `json:"instance"`
	Command              string              `json:"command"`
	CancelIntent         *core.UpgradeIntent `json:"cancelIntent,omitempty"`
	Authorization        string              `json:"authorization,omitempty"`
	AuthorizationPayload json.RawMessage     `json:"authorizationPayload,omitempty"`
}

const lifecycleStopPrefix = "lifecycle.upgrade.stop "

type upgradeIdentity struct {
	ProcessID  int    `json:"processId"`
	Instance   string `json:"instance"`
	Executable string `json:"executable"`
	Offline    bool   `json:"offline"`
	core.ManagedLifecycleState
}

type upgradeStop struct {
	ProcessID            int             `json:"processId"`
	Instance             string          `json:"instance"`
	AcknowledgementDir   string          `json:"acknowledgementDir"`
	Token                string          `json:"token"`
	Authorization        string          `json:"authorization,omitempty"`
	AuthorizationPayload json.RawMessage `json:"authorizationPayload,omitempty"`
}

type upgradeClosed struct {
	ProcessID    int    `json:"processId"`
	Instance     string `json:"instance"`
	Token        string `json:"token"`
	Closed       bool   `json:"closed"`
	LockReleased bool   `json:"lockReleased"`
}

type upgradeLifecycle struct {
	identity   upgradeIdentity
	dir        string
	stop       chan upgradeStop
	stopping   atomic.Bool
	webPending atomic.Bool
}

func newUpgradeLifecycle(dir string, offline bool) (*upgradeLifecycle, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	token, err := upgradeRandomToken()
	if err != nil {
		return nil, err
	}
	return &upgradeLifecycle{dir: dir, identity: upgradeIdentity{ProcessID: os.Getpid(), Instance: token, Executable: exe, Offline: offline}, stop: make(chan upgradeStop, 1)}, nil
}

func upgradeRandomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Only the protected local IPC owner exposes this route. There is intentionally
// no generic Web command capable of selecting a filesystem path. The separate
// authenticated Web handoff receives only an exact intent and locale.
func (l *upgradeLifecycle) handler(app *core.Core) control.Handler {
	return func(ctx context.Context, raw string) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if l.stopping.Load() {
			return nil, errors.New("managed shutdown is in progress")
		}
		if strings.HasPrefix(raw, lifecycleBoundPrefix) {
			var in upgradeBound
			if json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleBoundPrefix)), &in) != nil || in.ProcessID != l.identity.ProcessID || in.Instance != l.identity.Instance {
				return nil, errors.New("process binding changed")
			}
			if in.Authorization != "" {
				if err := app.CheckWebUpgradeAuthorization(in.Authorization, in.AuthorizationPayload, false); err != nil {
					return nil, err
				}
			}
			if in.CancelIntent != nil {
				if in.Command != "" {
					return nil, errors.New("ambiguous bound command")
				}
				return app.CancelManagedUpgrade(ctx, *in.CancelIntent)
			}
			if in.Command == "ui.url" {
				return app.UpgradeUIURL()
			}
			if in.Command != "ui" {
				var command webui.Command
				if json.Unmarshal([]byte(in.Command), &command) != nil || (command.Name != "direct-lan.upgrade.run" && command.Name != "direct-lan.upgrade.status") {
					return nil, errors.New("invalid bound command")
				}
			}
			return app.IPC(ctx, in.Command)
		}
		if raw == lifecycleIdentityCommand {
			identity := l.identity
			identity.ManagedLifecycleState = app.ManagedLifecycleState()
			return identity, nil
		}
		if strings.HasPrefix(raw, lifecycleStopPrefix) {
			var request upgradeStop
			if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, lifecycleStopPrefix)), &request); err != nil {
				return nil, err
			}
			if !validUpgradeStop(l.dir, l.identity, request) {
				return nil, errors.New("invalid managed shutdown binding")
			}
			if request.Authorization != "" {
				if err := app.CheckWebUpgradeAuthorization(request.Authorization, request.AuthorizationPayload, true); err != nil {
					return nil, err
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !l.stopping.CompareAndSwap(false, true) {
				return nil, errors.New("managed shutdown already requested")
			}
			l.stop <- request
			return map[string]string{"state": "stopping"}, nil
		}
		return app.IPC(ctx, raw)
	}
}

func validUpgradeStop(dir string, identity upgradeIdentity, request upgradeStop) bool {
	decoded, err := hex.DecodeString(request.Token)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == request.Token &&
		request.ProcessID == identity.ProcessID && request.Instance == identity.Instance &&
		filepath.IsAbs(request.AcknowledgementDir) && filepath.Clean(request.AcknowledgementDir) == request.AcknowledgementDir &&
		filepath.Dir(request.AcknowledgementDir) == dir && strings.HasPrefix(filepath.Base(request.AcknowledgementDir), ".upgrade-")
}

// shutdownManagedOwners is deliberately ordered and error-returning. Releasing
// the lock does not erase a failed Core/IPC close. The launcher requires both.
func shutdownManagedOwners(ipc, app, lock io.Closer) (closeErr, lockErr error) {
	if ipc != nil {
		closeErr = errors.Join(closeErr, ipc.Close())
	}
	if app != nil {
		closeErr = errors.Join(closeErr, app.Close())
	}
	if lock != nil {
		lockErr = lock.Close()
	}
	return closeErr, lockErr
}

func acknowledgeUpgradeShutdown(request upgradeStop, closeErr, lockErr error) error {
	ack := upgradeClosed{ProcessID: os.Getpid(), Instance: request.Instance, Token: request.Token, Closed: closeErr == nil, LockReleased: lockErr == nil}
	raw, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return control.Call(ctx, request.AcknowledgementDir, string(raw), nil)
}

type upgradeProcessObserver interface {
	Wait(context.Context) error
	Close() error
}

func stopForManagedUpgrade(ctx context.Context, dir string, identity upgradeIdentity, client controlCaller) error {
	observer, err := observeUpgradeProcess(identity.ProcessID)
	if err != nil {
		return fmt.Errorf("could not supervise old process; no shutdown requested: %w", err)
	}
	defer observer.Close()
	ackDir, err := os.MkdirTemp(dir, ".upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(ackDir)
	if err := config.Protect(ackDir, true); err != nil {
		return err
	}
	token, err := upgradeRandomToken()
	if err != nil {
		return err
	}
	request := upgradeStop{ProcessID: identity.ProcessID, Instance: identity.Instance, AcknowledgementDir: ackDir, Token: token}
	acks := make(chan upgradeClosed, 1)
	server, err := control.Serve(ctx, ackDir, func(_ context.Context, raw string) (any, error) {
		var ack upgradeClosed
		if json.Unmarshal([]byte(raw), &ack) != nil || ack.ProcessID != request.ProcessID || ack.Instance != request.Instance || ack.Token != request.Token {
			return nil, errors.New("invalid shutdown acknowledgement")
		}
		select {
		case acks <- ack:
		default:
		}
		return map[string]bool{"accepted": true}, nil
	})
	if err != nil {
		return err
	}
	defer server.Close()
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	// Even a lost accepted response cannot authorize restart. Continue only when
	// the separately authenticated final acknowledgement and exit both arrive.
	stopErr := client(ctx, dir, lifecycleStopPrefix+string(raw), nil)
	select {
	case <-ctx.Done():
		return errors.Join(stopErr, ctx.Err())
	case ack := <-acks:
		if !ack.Closed || !ack.LockReleased {
			return errors.New("old application reported incomplete shutdown or profile-lock release; replacement was not launched")
		}
	}
	if err := observer.Wait(ctx); err != nil {
		return fmt.Errorf("old process exit is unconfirmed; replacement was not launched: %w", err)
	}
	return nil
}
