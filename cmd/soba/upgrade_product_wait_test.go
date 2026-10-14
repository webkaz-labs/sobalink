//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// The actual launcher still owns cmd.Wait and its original buffered result.
// Only its registered parent may attest the resulting native status.
func installProductWaitObserver(dir string) {
	observeBackgroundProcessExit = func(pid int, state *os.ProcessState, waitErr error) {
		request := productWaitRegistration(pid, os.Getpid(), state, waitErr)
		// Malformed/absent state is an explicit failed handoff. A failed RPC cannot
		// become success: the supervisor still requires complete status coverage.
		_ = activationSupervisorCall(dir, request)
	}
}

func productWaitRegistration(pid, parent int, state *os.ProcessState, waitErr error) activationRegistration {
	request := activationRegistration{Kind: "wait", Role: "successor", PID: pid, Parent: parent}
	if state != nil && state.Pid() == pid {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && (status.Exited() || status.Signaled()) && (waitErr == nil) == (status.Exited() && status.ExitStatus() == 0) {
			value := int(status)
			request.WaitStatus = &value
		}
	}
	return request
}

func (s *activationSupervisor) recordProductWaitLocked(in activationRegistration) (any, error) {
	child, parent := s.children[in.PID], s.children[in.Parent]
	if !productMode() || in.Role != "successor" || child == nil || child.role != "successor" || child.parent != in.Parent || parent == nil || parent.role != "helper" || parent.exited || in.Origin != "" || in.WaitStatus == nil || *in.WaitStatus < 0 || *in.WaitStatus > 0xffff {
		s.failLocked("exit-status-invalid")
		return nil, errors.New("unowned or invalid product wait status")
	}
	status := unix.WaitStatus(*in.WaitStatus)
	if (!status.Exited() && !status.Signaled()) || child.exitKnown {
		s.failLocked("exit-status-conflict")
		return nil, errors.New("duplicate or invalid product wait status")
	}
	s.recordExitLocked(child, status)
	return map[string]bool{"observed": true}, nil
}
