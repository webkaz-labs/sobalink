//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func productWaitTestSupervisor(parent, pid int) *activationSupervisor {
	return &activationSupervisor{children: map[int]*activationChild{
		1:      {role: "old", exited: true, exitKnown: true},
		parent: {role: "helper", exitKnown: true},
		pid:    {role: "successor", parent: parent, exited: true},
	}, failure: "none", successorExit: "not-observed", reaped: 2}
}

func TestProductParentWaitStatusClosesFalsePass(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	t.Setenv("SOBA_ACTIVATION_CASE", "product-core-web")
	for _, status := range []unix.WaitStatus{0, 94 << 8, 66 << 8, unix.WaitStatus(unix.SIGTERM)} {
		t.Run(fmt.Sprint(int(status)), func(t *testing.T) {
			s := productWaitTestSupervisor(2, 3)
			if s.registeredExitsKnownLocked() {
				t.Fatal("termination alone established successful exit")
			}
			value := int(status)
			_, err := s.recordProductWaitLocked(activationRegistration{Kind: "wait", Role: "successor", PID: 3, Parent: 2, WaitStatus: &value})
			if err != nil || !s.registeredExitsKnownLocked() || s.reaped != 2 || s.children[3].exitStatus != status {
				t.Fatal("lost exact parent wait or invented a Wait4 reap", err)
			}
			wantFailed := status != 0
			if s.failed != wantFailed || s.successorExit != activationExitCategory(status) {
				t.Fatal("parent-reaped failure was accepted")
			}
			// No second status, identical or conflicting, can overwrite the first.
			_, err = s.recordProductWaitLocked(activationRegistration{Kind: "wait", Role: "successor", PID: 3, Parent: 2, WaitStatus: &value})
			if err == nil || !s.failed {
				t.Fatal("duplicate handoff accepted")
			}
		})
	}
}

func TestProductMissingParentWaitCannotPassAfterJoin(t *testing.T) {
	s := productWaitTestSupervisor(2, 3)
	if s.requireRegisteredExitsLocked(true) || !s.failed || s.failure != "exit-status-missing" || s.reaped != 2 {
		t.Fatal("joined descendants with missing successor status were accepted")
	}
	s = productWaitTestSupervisor(2, 3)
	s.children[3].exitKnown = true
	if s.requireRegisteredExitsLocked(false) {
		t.Fatal("exit statuses replaced native descendant join")
	}
}

func TestProductWaitRejectsUnownedMissingAndStaleStatus(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	t.Setenv("SOBA_ACTIVATION_CASE", "product-core-web")
	for _, mode := range []string{"missing", "foreign-parent", "foreign-child", "wrong-role", "stale-parent", "stopped", "overflow", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			s := productWaitTestSupervisor(2, 3)
			value := 0
			request := activationRegistration{Kind: "wait", Role: "successor", PID: 3, Parent: 2, WaitStatus: &value}
			switch mode {
			case "missing":
				request.WaitStatus = nil
			case "foreign-parent":
				request.Parent = 8
			case "foreign-child":
				request.PID = 8
			case "wrong-role":
				request.Role = "old"
			case "stale-parent":
				s.children[2].exited = true
			case "stopped":
				value = 0x137f
			case "overflow":
				value = 1 << 20
			case "disabled":
				t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "")
			}
			if _, err := s.recordProductWaitLocked(request); err == nil || !s.failed || s.registeredExitsKnownLocked() || s.reaped != 2 {
				t.Fatal("invalid evidence passed or changed physical accounting")
			}
		})
	}
	s := productWaitTestSupervisor(2, 3)
	s.recordExitLocked(s.children[3], 94<<8)
	value := 0
	if _, err := s.recordProductWaitLocked(activationRegistration{Kind: "wait", Role: "successor", PID: 3, Parent: 2, WaitStatus: &value}); err == nil || !s.failed || s.successorExit != "owner-failed" {
		t.Fatal("conflicting handoff erased native failure")
	}
}

// This subprocess does no product, networking, profile or listener work.
func TestProductWaitExitFixture(t *testing.T) {
	switch os.Getenv("SOBALINK_TEST_PARENT_WAIT_EXIT") {
	case "zero":
		os.Exit(0)
	case "owner-failed":
		os.Exit(94)
	}
}

func TestProductLauncherWaitObserverPrecedesDoneAndPreservesError(t *testing.T) {
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	t.Setenv("SOBA_ACTIVATION_CASE", "product-core-web")
	t.Setenv(webAcceptanceMarker, "")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	for _, mode := range []string{"zero", "owner-failed"} {
		if !t.Run(mode, func(t *testing.T) {
			t.Setenv("SOBALINK_TEST_PARENT_WAIT_EXIT", mode)
			log, err := os.CreateTemp(t.TempDir(), "startup-")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var original error
			s := productWaitTestSupervisor(os.Getpid(), -1)
			previous := observeBackgroundProcessExit
			observeBackgroundProcessExit = func(pid int, state *os.ProcessState, err error) {
				defer close(returned)
				original = err
				s.children[pid] = s.children[-1]
				delete(s.children, -1)
				request := productWaitRegistration(pid, os.Getpid(), state, err)
				_, handoffErr := s.recordProductWaitLocked(request)
				mismatch := error(nil)
				if err == nil {
					mismatch = errors.New("synthetic inconsistent wait error")
				}
				if productWaitRegistration(pid+1, os.Getpid(), state, err).WaitStatus != nil || productWaitRegistration(pid, os.Getpid(), nil, err).WaitStatus != nil || productWaitRegistration(pid, os.Getpid(), state, mismatch).WaitStatus != nil {
					panic("synthetic mismatched wait accepted")
				}
				if handoffErr != nil {
					panic("synthetic parent wait rejected")
				}
				close(entered)
				<-release
			}
			child, err := launchBackground(exe, []string{"-test.run=^TestProductWaitExitFixture$"}, log)
			if err != nil {
				observeBackgroundProcessExit = previous
				t.Fatal(err)
			}
			doneJoined := false
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				observerDone, processDone := (<-chan struct{})(returned), child.Done
				if doneJoined {
					processDone = nil
				}
				for observerDone != nil || processDone != nil {
					select {
					case <-observerDone:
						observerDone = nil
					case <-processDone:
						processDone = nil
					case <-timer.C:
						t.Error("fixture parent wait cleanup did not join")
						return // Never restore a hook with an unresolved observer.
					}
				}
				observeBackgroundProcessExit = previous
			})
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("parent wait not observed")
			}
			select {
			case <-child.Done:
				doneJoined = true
				t.Fatal("Done escaped held fixture observer")
			default:
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case result := <-child.Done:
				doneJoined = true
				if result != original || (result == nil) != (mode == "zero") {
					t.Fatal("original wait error changed")
				}
				if result != nil {
					var exit *exec.ExitError
					if !errors.As(result, &exit) || exit.ExitCode() != 94 {
						t.Fatal("exit status changed")
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original buffered Done not delivered")
			}
			if s.failed != (mode == "owner-failed") || !s.registeredExitsKnownLocked() || s.reaped != 2 {
				t.Fatal("parent-reaped successor failure gate incorrect")
			}
		}) {
			return
		}
	}
}

func TestProductFinalOwnerFailureDiagnosticIsClosed(t *testing.T) {
	dir := t.TempDir()
	d := newProductNativeDiagnostic(dir, "successor")
	d.view.ForegroundFailed = true
	d.view.MonitorFailed = true
	d.view.PeerCloseFailed = true
	d.view.LockCloseFailed = true
	d.finish(errors.New("synthetic-private-detail"))
	var result productNativeObservation
	if err := readProductJSON(filepath.Join(activationEvidenceDir(dir, "successor"), "native.json"), &result, 4096); err != nil {
		t.Fatal(err)
	}
	if !result.Failed || !result.ForegroundFailed || !result.MonitorFailed || !result.PeerCloseFailed || !result.LockCloseFailed {
		t.Fatal("final owner error components lost")
	}
	raw, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || strings.Contains(string(raw), "synthetic-private-detail") {
		t.Fatal("private error copied into diagnostic")
	}
}
