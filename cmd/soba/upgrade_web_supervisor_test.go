//go:build web_activation_native && managed_restart_native && linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"golang.org/x/sys/unix"
)

type activationRegistration struct {
	Kind, Role, Origin string
	PID, Parent        int
}
type activationChild struct {
	role     string
	observer upgradeProcessObserver
	exited   bool
	done     chan struct{}
}
type activationSupervisor struct {
	mu        sync.Mutex
	children  map[int]*activationChild
	origins   map[string]int
	old       int
	reaped    int
	failed    bool
	oldExited bool
	dir       string
	ctx       context.Context
}

func activationSupervisorCall(dir string, request activationRegistration) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return control.Call(ctx, filepath.Join(dir, ".supervisor"), string(raw), nil)
}
func registerActivationChild(dir, role string) error {
	return activationSupervisorCall(dir, activationRegistration{Kind: "register", Role: role, PID: os.Getpid(), Parent: os.Getppid()})
}
func registerActivationOrigin(dir, role, origin string) error {
	return activationSupervisorCall(dir, activationRegistration{Kind: "origin", Role: role, PID: os.Getpid(), Parent: os.Getppid(), Origin: origin})
}
func (s *activationSupervisor) observeLocked(pid int, role string) error {
	observer, err := observeUpgradeProcess(pid)
	if err != nil {
		return err
	}
	child := &activationChild{role: role, observer: observer, done: make(chan struct{})}
	s.children[pid] = child
	go func() {
		defer close(child.done)
		err := observer.Wait(s.ctx)
		s.mu.Lock()
		child.exited = err == nil
		if child.exited {
			for origin, owner := range s.origins {
				if owner == pid {
					delete(s.origins, origin)
				}
			}
			if e := s.writeOriginsLocked(); e != nil {
				s.failed = true
			}
		}
		if err != nil {
			s.failed = true
		}
		s.mu.Unlock()
	}()
	return nil
}
func (s *activationSupervisor) handle(_ context.Context, raw string) (any, error) {
	var in activationRegistration
	if json.Unmarshal([]byte(raw), &in) != nil {
		return nil, errors.New("invalid fixture registration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	child := s.children[in.PID]
	if in.Kind == "register" {
		if in.Role == "old" {
			if in.PID != s.old || in.Parent != os.Getpid() || child == nil {
				return nil, errors.New("unexpected old owner")
			}
			return map[string]bool{"registered": true}, nil
		}
		parent := s.children[in.Parent]
		expected := "old"
		if in.Role == "successor" {
			expected = "helper"
		} else if in.Role != "helper" {
			return nil, errors.New("unexpected fixture role")
		}
		if child != nil || parent == nil || parent.role != expected || parent.exited {
			return nil, errors.New("unowned fixture child")
		}
		for _, existing := range s.children {
			if existing.role == in.Role {
				return nil, errors.New("duplicate fixture child")
			}
		}
		if err := s.observeLocked(in.PID, in.Role); err != nil {
			return nil, err
		}
		return map[string]bool{"registered": true}, nil
	}
	if in.Kind != "origin" || !validUpgradeUIURL(in.Origin) {
		return nil, errors.New("invalid fixture origin")
	}
	// Helper origin is announced by its verified old parent after the real
	// anonymous-pipe descriptor arrives, before any browser receives it.
	ownerPID := in.PID
	if in.Role == "helper" {
		if child == nil || child.role != "old" {
			return nil, errors.New("unowned helper descriptor")
		}
		found := false
		for pid, c := range s.children {
			if c.role == "helper" && !c.exited {
				found = true
				ownerPID = pid
			}
		}
		if !found {
			return nil, errors.New("helper not registered")
		}
	} else if child == nil || child.role != in.Role || child.exited {
		return nil, errors.New("unowned origin")
	}
	s.origins[in.Origin] = ownerPID
	if err := s.writeOriginsLocked(); err != nil {
		return nil, err
	}
	return map[string]bool{"registered": true}, nil
}
func (s *activationSupervisor) writeOriginsLocked() error {
	origins := make([]string, 0, len(s.origins))
	for origin := range s.origins {
		origins = append(origins, origin)
	}
	return config.WriteJSON(filepath.Join(s.dir, "owned-origins.json"), origins)
}

// Linux subreaping proves even children that die before TestMain registration
// or startup markers. This never acts on unrelated OS processes: only this
// supervisor's own descendants become waitable children.
func runActivationSupervisor(dir string) error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	s := &activationSupervisor{dir: dir, ctx: ctx, children: make(map[int]*activationChild), origins: make(map[string]int)}
	controlDir := filepath.Join(dir, ".supervisor")
	if err := config.SecureDir(controlDir); err != nil {
		return err
	}
	server, err := control.Serve(ctx, controlDir, s.handle)
	if err != nil {
		return err
	}
	defer server.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(exe, "--web-activation-fixture")
	command.Env = os.Environ()
	command.Dir = dir
	s.mu.Lock()
	err = command.Start()
	if err == nil {
		s.old = command.Process.Pid
		err = s.observeLocked(s.old, "old")
		_ = command.Process.Release()
	}
	s.mu.Unlock()
	if err != nil {
		_ = os.WriteFile(filepath.Join(dir, "stop-fixture"), []byte("stop"), 0600)
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	complete := false
	publishedOld := false
	if e := config.WriteJSON(filepath.Join(dir, "native-progress.json"), map[string]bool{"oldExited": false}); e != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
	}
	for {
		// Reap direct children and all orphaned descendants. Living descendants of
		// living children prevent their ancestors from being considered all-exited.
		noChildren := false
		for {
			var status unix.WaitStatus
			pid, e := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(e, unix.ECHILD) {
				noChildren = true
				break
			}
			if e != nil {
				if errors.Is(e, unix.EINTR) {
					continue
				}
				s.mu.Lock()
				s.failed = true
				s.mu.Unlock()
				break
			}
			if pid == 0 {
				break
			}
			s.mu.Lock()
			s.reaped++
			if pid == s.old {
				s.oldExited = true
			}
			if !status.Exited() || status.ExitStatus() != 0 {
				s.failed = true
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		allObserved := true
		for _, child := range s.children {
			if !child.exited {
				allObserved = false
			}
		}
		oldExited := s.oldExited
		s.mu.Unlock()
		if oldExited != publishedOld {
			publishedOld = oldExited
			if e := config.WriteJSON(filepath.Join(dir, "native-progress.json"), map[string]bool{"oldExited": oldExited}); e != nil {
				s.mu.Lock()
				s.failed = true
				s.mu.Unlock()
			}
		}
		_, stopErr := os.Stat(filepath.Join(dir, "stop-fixture"))
		if stopErr == nil && noChildren && allObserved {
			complete = true
			break
		}
		select {
		case <-ctx.Done():
			goto finished
		case <-tick.C:
		}
	}
finished:
	cancel()
	s.mu.Lock()
	children := make([]*activationChild, 0, len(s.children))
	for _, child := range s.children {
		children = append(children, child)
	}
	s.mu.Unlock()
	for _, child := range children {
		<-child.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, child := range s.children {
		_ = child.observer.Close()
	}
	successorRegistered := false
	for _, child := range s.children {
		if child.role == "successor" {
			successorRegistered = true
		}
	}
	proof := map[string]any{"successorRegistered": successorRegistered, "allDescendantsReaped": complete, "registeredNativeExits": complete, "reaped": s.reaped, "success": complete && !s.failed}
	if e := config.WriteJSON(filepath.Join(dir, "exit-proof.json"), proof); e != nil {
		return e
	}
	if !complete || s.failed {
		return errors.New("fixture descendant exit proof failed")
	}
	return nil
}
