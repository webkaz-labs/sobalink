//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

// The product runner's outer owner accounts the exact Playwright subtree,
// including Chromium children that create their own process groups. It is not
// a product launcher, a system process scanner, or a process-group kill fallback.
import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type productScopeIdentity struct {
	pid, parent int
	start       uint64
}
type productScopeChild struct {
	identity productScopeIdentity
	fd       int
}
type productScopeProof struct {
	Schema            int  `json:"schema"`
	Complete          bool `json:"complete"`
	DescendantsReaped bool `json:"descendantsReaped"`
	PlaywrightExit    int  `json:"playwrightExit"`
	Forced            bool `json:"forced"`
	DeadlineExceeded  bool `json:"deadlineExceeded"`
	Errors            int  `json:"errors"`
	Observed          int  `json:"observed"`
	Reaped            int  `json:"reaped"`
}

func productScopeGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH)
}
func productScopeStat(pid int) (productScopeIdentity, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return productScopeIdentity{}, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return productScopeIdentity{}, errors.New("invalid owned process identity")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return productScopeIdentity{}, errors.New("incomplete owned process identity")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return productScopeIdentity{}, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return productScopeIdentity{}, err
	}
	return productScopeIdentity{pid: pid, parent: parent, start: start}, nil
}
func productScopeChildren(identity productScopeIdentity) ([]int, error) {
	before, err := productScopeStat(identity.pid)
	if err != nil {
		return nil, err
	}
	if before.start != identity.start {
		return nil, os.ErrNotExist
	}
	// Aggregate children of every thread of THIS owned process. Reading only its
	// leader's children would miss fork/exec performed on another Go/Chrome thread.
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", identity.pid))
	if err != nil {
		return nil, err
	}
	children := make(map[int]bool)
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil {
			continue
		}
		file, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/children", identity.pid, tid))
		if productScopeGone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		if productScopeGone(readErr) && closeErr == nil {
			continue
		}
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if len(raw) > 1<<20 {
			return nil, errors.New("owned child census exceeded bound")
		}
		for _, field := range strings.Fields(string(raw)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return nil, errors.New("invalid owned child id")
			}
			children[pid] = true
		}
	}
	after, err := productScopeStat(identity.pid)
	if err != nil {
		return nil, err
	}
	if after.start != identity.start {
		return nil, os.ErrNotExist
	}
	result := make([]int, 0, len(children))
	for pid := range children {
		result = append(result, pid)
	}
	return result, nil
}
func productScopeLive(fd int) bool {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	// A poll error or any terminal/invalid event cannot confer ownership.
	// This includes POLLIN and post-reap POLLHUP; ERR/NVAL also fail closed.
	return err == nil && n == 0 && poll[0].Revents == 0
}
func productScopeOwnedParent(pid int, root productScopeIdentity, known map[int]*productScopeChild) bool {
	if pid == root.pid {
		return true
	}
	owner := known[pid]
	if owner == nil || !productScopeLive(owner.fd) {
		return false
	}
	now, err := productScopeStat(pid)
	return err == nil && now.start == owner.identity.start && productScopeLive(owner.fd)
}

func productScopeDiscover(root productScopeIdentity, known map[int]*productScopeChild, proof *productScopeProof) error {
	queue := []productScopeIdentity{root}
	visited := make(map[int]bool)
	for len(queue) > 0 {
		owner := queue[0]
		queue = queue[1:]
		if visited[owner.pid] {
			continue
		}
		visited[owner.pid] = true
		if owner.pid != root.pid && !productScopeOwnedParent(owner.pid, root, known) {
			continue
		}
		children, err := productScopeChildren(owner)
		if productScopeGone(err) {
			continue
		}
		if err != nil {
			return err
		}
		if owner.pid != root.pid && !productScopeOwnedParent(owner.pid, root, known) {
			continue
		}
		for _, pid := range children {
			first, err := productScopeStat(pid)
			if productScopeGone(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !productScopeOwnedParent(first.parent, root, known) {
				continue
			}
			if old := known[pid]; old != nil && old.identity.start == first.start && productScopeLive(old.fd) {
				queue = append(queue, first)
				continue
			}
			fd, err := unix.PidfdOpen(pid, 0)
			if errors.Is(err, unix.ESRCH) {
				continue
			}
			if err != nil {
				return err
			}
			second, err := productScopeStat(pid)
			if err != nil || first.start != second.start || !productScopeOwnedParent(second.parent, root, known) {
				unix.Close(fd)
				continue
			}
			if old := known[pid]; old != nil {
				unix.Close(old.fd)
				delete(known, pid)
			}
			if len(known) >= 512 {
				// Unexpected resource growth is a FAIL. This still uses a validated pidfd
				// for this exact owned child, never a bare-PID or process-name fallback.
				proof.Forced = true
				proof.Errors++
				_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
				_ = unix.Close(fd)
				continue
			}
			known[pid] = &productScopeChild{identity: second, fd: fd}
			proof.Observed++
			queue = append(queue, second)
		}
	}
	return nil
}
func productScopeSignal(known map[int]*productScopeChild, signal unix.Signal) error {
	var errs []error
	for _, child := range known {
		if err := unix.PidfdSendSignal(child.fd, signal, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func productScopePrune(known map[int]*productScopeChild) {
	for pid, child := range known {
		poll := []unix.PollFd{{Fd: int32(child.fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(poll, 0); err == nil && n > 0 && poll[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 && poll[0].Revents&(unix.POLLERR|unix.POLLNVAL) == 0 {
			_ = unix.Close(child.fd)
			delete(known, pid)
		}
	}
}

// Cancellation is a request to this owner, never permission to abandon children.
func productScopeCancelled(root string, signals <-chan os.Signal) (bool, error) {
	select {
	case <-signals:
		return true, nil
	default:
	}
	path := filepath.Join(root, "product-scope-cancel")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64 {
		return false, errors.New("invalid private cancellation marker")
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	opened, statErr := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, 65))
	closeErr := file.Close()
	if statErr != nil || readErr != nil || closeErr != nil {
		return false, errors.Join(statErr, readErr, closeErr)
	}
	if !os.SameFile(info, opened) || string(raw) != "cancel-owned-product-scope-v1" {
		return false, errors.New("invalid private cancellation marker")
	}
	return true, nil
}

func runProductBrowserScope() int {
	if os.Getenv("SOBALINK_WEB_ACTIVATION_FIXTURE") != "1" || os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") != "1" {
		return 97
	}
	root := os.Getenv("SOBA_PRODUCT_ACTIVATION_PRIVATE_RUN")
	info, err := os.Lstat(root)
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(filepath.Base(root), "spa-") || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return 97
	}
	owner, ownerOK := info.Sys().(*syscall.Stat_t)
	if !ownerOK || int(owner.Uid) != os.Getuid() {
		return 97
	}
	markerPath := filepath.Join(root, "product-scope-owned")
	markerInfo, markerErr := os.Lstat(markerPath)
	if markerErr != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm()&0077 != 0 || markerInfo.Size() > 64 {
		return 97
	}
	markerOwner, markerOwned := markerInfo.Sys().(*syscall.Stat_t)
	if !markerOwned || int(markerOwner.Uid) != os.Getuid() {
		return 97
	}
	marker, err := os.ReadFile(filepath.Join(root, "product-scope-owned"))
	if err != nil || string(marker) != "product-browser-scope-v1" {
		return 97
	}
	// Keep ordinary CI cancellation attached through owned descendant reaping.
	// SIGKILL or executor destruction cannot provide graceful cleanup evidence.
	cancellation := make(chan os.Signal, 2)
	signal.Notify(cancellation, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(cancellation)
	node := os.Getenv("SOBA_PRODUCT_NODE_EXECUTABLE")
	nodeInfo, err := os.Stat(node)
	if err != nil || !filepath.IsAbs(node) || !nodeInfo.Mode().IsRegular() {
		return 97
	}
	if _, err := os.Stat("playwright.product-activation.config.mjs"); err != nil {
		return 97
	}
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return 97
	}
	identity, err := productScopeStat(os.Getpid())
	if err != nil {
		return 97
	}
	// Establish procfs/pidfd support before starting any child. No alternative
	// signal route is used if the required Linux ownership facilities are absent.
	if _, err := productScopeChildren(identity); err != nil {
		return 97
	}
	selfFD, err := unix.PidfdOpen(identity.pid, 0)
	if err != nil {
		return 97
	}
	if err := unix.PidfdSendSignal(selfFD, 0, nil, 0); err != nil {
		_ = unix.Close(selfFD)
		return 97
	}
	_ = unix.Close(selfFD)
	proof := productScopeProof{Schema: 1, PlaywrightExit: -1}
	path := filepath.Join(root, "scope-proof.json")
	if cancelled, err := productScopeCancelled(root, cancellation); cancelled || err != nil {
		proof.Forced = true
		if err != nil {
			proof.Errors++
		}
		proof.Complete = true
		proof.DescendantsReaped = true
		_ = productPrivateJSON(path, proof)
		return 1
	}
	command := exec.Command(node, "node_modules/@playwright/test/cli.js", "test", "--config", "playwright.product-activation.config.mjs")
	command.Env = os.Environ()
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		proof.Errors++
		proof.Complete = true
		proof.DescendantsReaped = true
		_ = productPrivateJSON(path, proof)
		return 1
	}
	primary := command.Process.Pid
	_ = command.Process.Release()
	known := make(map[int]*productScopeChild)
	defer func() {
		for _, child := range known {
			_ = unix.Close(child.fd)
		}
	}()
	started := time.Now()
	primaryExited := false
	forcePhase := 0
	var forcedAt time.Time
	reportedUnjoined := false
	for {
		if cancelled, err := productScopeCancelled(root, cancellation); cancelled || err != nil {
			proof.Forced = true
			if err != nil {
				proof.Errors++
			}
		}
		if err := productScopeDiscover(identity, known, &proof); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		noChildren := false
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ECHILD) {
				noChildren = true
				break
			}
			if err != nil {
				proof.Errors++
				proof.Forced = true
				break
			}
			if pid == 0 {
				break
			}
			proof.Reaped++
			if pid == primary && !primaryExited {
				primaryExited = true
				if status.Exited() {
					proof.PlaywrightExit = status.ExitStatus()
				} else {
					proof.PlaywrightExit = 128 + int(status.Signal())
				}
			}
		}
		productScopePrune(known)
		elapsed := time.Since(started)
		if elapsed >= 375*time.Second {
			proof.DeadlineExceeded = true
		}
		if noChildren && primaryExited {
			proof.Complete = true
			proof.DescendantsReaped = true
			if productPrivateJSON(path, proof) != nil {
				return 1
			}
			if proof.PlaywrightExit == 0 && !proof.Forced && !proof.DeadlineExceeded && proof.Errors == 0 {
				return 0
			}
			return 1
		}
		if (elapsed >= 375*time.Second || proof.Forced) && forcePhase == 0 {
			proof.Forced = true
			forcePhase = 1
			forcedAt = time.Now()
			if productScopeSignal(known, unix.SIGTERM) != nil {
				proof.Errors++
			}
		}
		if elapsed >= 390*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 15*time.Second) || proof.Errors > 0 {
			proof.Forced = true
			forcePhase = 2
			// Freeze only validated owned identities before terminating them. Orphans
			// and any last child fork are adopted/censused on subsequent passes.
			if productScopeSignal(known, unix.SIGSTOP) != nil {
				proof.Errors++
			}
			if productScopeSignal(known, unix.SIGKILL) != nil {
				proof.Errors++
			}
		} else if forcePhase == 1 {
			if productScopeSignal(known, unix.SIGTERM) != nil {
				proof.Errors++
			}
		}
		if (elapsed >= 405*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 30*time.Second)) && !reportedUnjoined {
			proof.Forced = true
			reportedUnjoined = true
			_ = productPrivateJSON(path, proof)
			// The bounded test is failed. Keep this cleanup owner ATTACHED until actual
			// reaping completes; do not abandon descendants or delete private evidence.
			fmt.Fprintln(os.Stderr, "FAIL: product process scope remains unjoined; cleanup ownership retained")
		}
		pause := 250 * time.Millisecond
		if forcePhase != 0 {
			pause = 25 * time.Millisecond
		}
		time.Sleep(pause)
	}
}
