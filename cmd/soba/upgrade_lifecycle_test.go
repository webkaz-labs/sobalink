package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

type upgradeTestCloser struct {
	name  string
	order *[]string
	err   error
}

func (c upgradeTestCloser) Close() error { *c.order = append(*c.order, c.name); return c.err }

// Pure fake-owner tests: no Core, process, socket, profile or network opens.
func TestManagedShutdownRetainsCloseFailureAfterRelease(t *testing.T) {
	failure := errors.New("close failed")
	for _, fail := range []string{"", "ipc", "core", "lock"} {
		var order []string
		owner := func(name string) upgradeTestCloser {
			var err error
			if name == fail {
				err = failure
			}
			return upgradeTestCloser{name, &order, err}
		}
		closed, released := shutdownManagedOwners(owner("ipc"), owner("core"), owner("lock"))
		if !reflect.DeepEqual(order, []string{"ipc", "core", "lock"}) {
			t.Fatal(order)
		}
		if (closed != nil) != (fail == "ipc" || fail == "core") || (released != nil) != (fail == "lock") {
			t.Fatal(fail, closed, released)
		}
	}
}

func TestUpgradeUIAcceptsOnlyBareNumericLoopback(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:1234", "http://127.0.0.1:1234/", "http://[::1]:1234/"} {
		if !validUpgradeUIURL(raw) {
			t.Errorf("rejected %q", raw)
		}
	}
	for _, raw := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://127.0.0.1:0", "http://127.0.0.1:1234/?code=secret", "http://127.0.0.1:1234/#code", "http://user@127.0.0.1:1234/", "http://192.0.2.1:1234/", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234/?", "http://[::1%25lo]:1234/", "file:///local", "http://127.0.0.1:01234/"} {
		if validUpgradeUIURL(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestUpgradeStopExactOwnerAndProtectedChild(t *testing.T) {
	identity := upgradeIdentity{ProcessID: 42, Instance: "instance"}
	// A path selected only in memory keeps this test independent of OS files.
	dir := "/profile"
	if runtime.GOOS == "windows" {
		dir = `C:\profile`
	}
	request := upgradeStop{ProcessID: 42, Instance: "instance", AcknowledgementDir: filepath.Join(dir, ".upgrade-test"), Token: strings.Repeat("a", 64)}
	if !validUpgradeStop(dir, identity, request) {
		t.Fatal("exact request refused")
	}
	for _, mutate := range []func(*upgradeStop){
		func(r *upgradeStop) { r.ProcessID++ },
		func(r *upgradeStop) { r.Instance = "different" },
		func(r *upgradeStop) { r.Token = "short" },
		func(r *upgradeStop) { r.AcknowledgementDir = filepath.Join(dir, "unrelated") },
		func(r *upgradeStop) { r.AcknowledgementDir = filepath.Join(dir, ".upgrade-test", "nested") },
	} {
		bad := request
		mutate(&bad)
		if validUpgradeStop(dir, identity, bad) {
			t.Fatal("mismatched request accepted")
		}
	}
}

func TestManagedRestartGateNeverLaunchesAfterUnconfirmedShutdown(t *testing.T) {
	old := upgradeIdentity{ProcessID: 41, Instance: "old"}
	for _, detail := range []string{"Core close", "IPC close", "lock release", "acknowledgement", "process exit"} {
		failure := errors.New(detail)
		_, err := managedUpgradeRestartSequence(old, func() error { return failure }, func() (upgradeIdentity, error) { t.Fatal("launched after", detail); return upgradeIdentity{}, nil })
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
}

func TestManagedRestartGateRequiresFreshOfflineSuccessor(t *testing.T) {
	old := upgradeIdentity{ProcessID: 41, Instance: "old"}
	valid := upgradeIdentity{ProcessID: 42, Instance: "new", Offline: true}
	for _, mutate := range []func(*upgradeIdentity){
		func(*upgradeIdentity) {},
		func(p *upgradeIdentity) { p.ProcessID = 0 },
		func(p *upgradeIdentity) { p.Instance = "old" },
		func(p *upgradeIdentity) { p.Offline = false },
		func(p *upgradeIdentity) { p.ManagedLifecycleState = core.ManagedLifecycleState{AttemptedNetwork: true} },
		func(p *upgradeIdentity) { p.ManagedLifecycleState = core.ManagedLifecycleState{NetworkReady: true} },
	} {
		successor := valid
		mutate(&successor)
		var order []string
		got, err := managedUpgradeRestartSequence(old, func() error { order = append(order, "closed-and-exited"); return nil }, func() (upgradeIdentity, error) { order = append(order, "launch"); return successor, nil })
		if !reflect.DeepEqual(order, []string{"closed-and-exited", "launch"}) {
			t.Fatal(order)
		}
		wantOK := successor == valid
		if (err == nil) != wantOK || wantOK && got != valid {
			t.Fatal(successor, err)
		}
	}
}

func TestUpgradeReopenUsesPrivateCodeAndBareURL(t *testing.T) {
	oldPrivate, oldBrowser := loginPrivateTerminal, launchLoginBrowser
	defer func() { loginPrivateTerminal, launchLoginBrowser = oldPrivate, oldBrowser }()
	loginPrivateTerminal = func(io.Writer) bool { return true }
	var opened string
	launchLoginBrowser = func(_ context.Context, address string) error { opened = address; return nil }
	var out bytes.Buffer
	client := func(_ context.Context, _, command string, result any) error {
		if command != "ui" {
			t.Fatal(command)
		}
		return json.Unmarshal([]byte(`{"url":"http://127.0.0.1:1234/","code":"private-fixture-code"}`), result)
	}
	if err := reopenUpgradeUI(context.Background(), "profile", false, &out, client); err != nil {
		t.Fatal(err)
	}
	if opened != "http://127.0.0.1:1234/" || !strings.Contains(out.String(), "private-fixture-code") {
		t.Fatal("missing private presentation")
	}
	loginPrivateTerminal = func(io.Writer) bool { return false }
	if err := reopenUpgradeUI(context.Background(), "profile", false, &out, func(context.Context, string, string, any) error {
		t.Fatal("requested a code without private output")
		return nil
	}); err == nil {
		t.Fatal("nonterminal accepted")
	}
}

func TestUpgradeApplyRefusesRedirectedOutputBeforeIPC(t *testing.T) {
	oldPrivate := loginPrivateTerminal
	defer func() { loginPrivateTerminal = oldPrivate }()
	loginPrivateTerminal = func(io.Writer) bool { return false }
	var out bytes.Buffer
	args := []string{"--peer", "fixture-peer", "--deadline", time.Now().UTC().Add(time.Minute).Truncate(time.Second).Format(time.RFC3339), "--apply", "--review", "fixture-review"}
	err := managedUpgradeCLI(context.Background(), args, "profile", "en", false, false, &out, func(context.Context, string, string, any) error {
		t.Fatal("apply contacted IPC before private-terminal gate")
		return nil
	})
	if err == nil {
		t.Fatal("redirected apply accepted")
	}
}
