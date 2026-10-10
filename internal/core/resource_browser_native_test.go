//go:build resource_browser_native && linux

package core

// A real offline Core and embedded Web UI, with one browser case. This test
// owns no IPC/peer listener and does not enable a production acceptance hook.
// HTTP counters are not independent Core-admission observations.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/web"
	"golang.org/x/sys/unix"
)

const resourceBrowserSelector = "^TestResourceBrowserNativeLocalCatalog$"

func resourceBrowserPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("private browser directory unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Getuid() {
		return errors.New("private browser directory invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("private browser directory alias forbidden")
	}
	return nil
}
func resourceBrowserWrite(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 256<<10 {
		return errors.New("bounded browser receipt required")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("private browser receipt creation failed")
	}
	_, writeErr := f.Write(raw)
	return errors.Join(writeErr, f.Close())
}
func resourceBrowserRead(path string, limit int64, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("private browser receipt unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Getuid() || info.Size() < 1 || info.Size() > limit {
		return errors.New("private browser receipt invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("private browser receipt open failed")
	}
	opened, statErr := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(info, opened) || int64(len(raw)) > limit {
		return errors.New("private browser receipt changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("closed browser receipt schema required")
	}
	return nil
}
func resourceBrowserVerifyBinary(path, expected string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("frozen browser executable unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("frozen browser executable unavailable")
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("frozen browser executable changed")
	}
	return nil
}
func resourceBrowserInventory(root string, maxBytes int64, strict bool) (map[string]string, error) {
	rootInfo, rootErr := os.Lstat(root)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("browser inventory root unavailable")
	}
	found := make(map[string]string)
	var total int64
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Chromium can remove its own transient descendants between WalkDir
			// and Info. This exception never applies to the root or product state.
			if !strict && path != root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		count++
		if count > 8192 {
			return errors.New("browser file count exceeded")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if !strict && path != root && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		if total > maxBytes {
			return errors.New("browser file bytes exceeded")
		}
		if !info.Mode().IsRegular() {
			if strict {
				return errors.New("product state contains unexpected file type")
			}
			// Chromium may own Unix sockets and lock symlinks in its private profile.
			// They are counted without following symlinks or treating them as files.
			return nil
		}
		if !strict {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, io.LimitReader(f, maxBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return errors.New("product state read failed")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found[name] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	after, afterErr := os.Lstat(root)
	if afterErr != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInfo, after) {
		return nil, errors.New("browser inventory root changed")
	}
	return found, err
}
func resourceBrowserOffline(c *Core, factory *atomic.Uint32) error {
	if c == nil || factory.Load() != 0 {
		return errors.New("unexpected offline constructor")
	}
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.RLock()
	valid := c.node == nil && c.peerServer == nil && c.contextControl == nil && c.contextUpgrade == nil && c.attemptedNetwork == "" && len(c.active) == 0 && len(c.proxies) == 0 && len(c.outgoing) == 0 && len(c.profile.Peers) == 0 && c.profile.Settings.Network == "none"
	c.mu.RUnlock()
	g := c.resourceGrants
	groups := c.resourceGroups
	valid = valid && g != nil && g.firstUse && len(g.state.Records) == 0 && len(g.state.ManagementRecords) == 0 && g.runtime == nil && g.retiring == nil && g.managementRuntime == nil && g.managementRetiring == nil
	valid = valid && groups != nil && groups.store != nil && groups.store.firstUse && len(groups.store.state.Runs) == 0 && groups.prepared == nil && groups.active == nil && groups.overlay == nil
	valid = valid && c.resourceLock != nil && resource.ValidID(c.resourceIdentity) && len(c.resourceState.Records) == 0 && c.resourceState.scoped == nil && c.transfers != nil && len(c.transfers.List()) == 0
	if !valid {
		return errors.New("offline structural oracle failed")
	}
	return nil
}

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

// Cancellation-marker decoder is reused with only B1 marker literals changed.
func productScopeCancelled(root string, signals <-chan os.Signal) (bool, error) {
	select {
	case <-signals:
		return true, nil
	default:
	}
	path := filepath.Join(root, "resource-browser-cancel")
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
	if !os.SameFile(info, opened) || string(raw) != "cancel-owned-resource-browser-v1" {
		return false, errors.New("invalid private cancellation marker")
	}
	return true, nil
}

// This bounded hosted-only coordinator reuses the product owner functions above.
// Detached Chromium groups remain descendants of this Linux subreaper. A hard
// Go/CI timeout is failed platform containment, never a passing test-owned join.
func resourceBrowserRunScope(c *Core, factory *atomic.Uint32, root, webDir, node, chromium string, entered time.Time, cancellation <-chan os.Signal) productScopeProof {
	proof := productScopeProof{Schema: 1, PlaywrightExit: -1}
	identity, err := productScopeStat(os.Getpid())
	if err != nil {
		proof.Errors++
		return proof
	}
	if _, err = productScopeChildren(identity); err != nil {
		proof.Errors++
		return proof
	}
	selfFD, err := unix.PidfdOpen(identity.pid, 0)
	if err != nil {
		proof.Errors++
		return proof
	}
	err = unix.PidfdSendSignal(selfFD, 0, nil, 0)
	_ = unix.Close(selfFD)
	if err != nil {
		proof.Errors++
		return proof
	}
	log, err := os.OpenFile(filepath.Join(root, "browser-private.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		proof.Errors++
		return proof
	}
	defer log.Close()
	command := exec.Command(node, filepath.Join(webDir, "node_modules", "@playwright", "test", "cli.js"), "test", "--config", filepath.Join(webDir, "playwright.resource-native.config.mjs"), "--grep", "(?:^| )resource-native-local-catalog-en-settings$", "--workers", "1", "--retries", "0")
	command.Dir = webDir
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"),
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PLAYWRIGHT_NO_COPY_PROMPT=1", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
		"SOBALINK_RUN_RESOURCE_BROWSER_NATIVE=reviewed-offline-catalog-settings-v1", "SOBA_RESOURCE_BROWSER_PRIVATE_ROOT=" + root, "SOBA_RESOURCE_BROWSER_CHROMIUM=" + chromium,
	}
	command.Stdout, command.Stderr = log, log
	if cancelled, cancelErr := productScopeCancelled(root, cancellation); cancelled || cancelErr != nil {
		proof.Forced = true
		if cancelErr != nil {
			proof.Errors++
		}
		proof.Complete = true
		proof.DescendantsReaped = true
		return proof
	}
	if time.Since(entered) >= 15*time.Second {
		proof.DeadlineExceeded = true
		return proof
	}
	if err := command.Start(); err != nil {
		proof.Errors++
		proof.Complete = true
		proof.DescendantsReaped = true
		return proof
	}
	primary := command.Process.Pid
	_ = command.Process.Release()
	known := make(map[int]*productScopeChild)
	defer func() {
		for _, child := range known {
			_ = unix.Close(child.fd)
		}
	}()
	primaryExited, reportedUnjoined := false, false
	forcePhase := 0
	var forcedAt time.Time
	for {
		if cancelled, cancelErr := productScopeCancelled(root, cancellation); cancelled || cancelErr != nil {
			proof.Forced = true
			if cancelErr != nil {
				proof.Errors++
			}
		}
		if err := productScopeDiscover(identity, known, &proof); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		// Node parent + one worker + at most 24 browser descendants/owners. The
		// unchanged inherited helper's 512 guard remains only an emergency ceiling.
		if len(known) > 27 || proof.Observed > 64 {
			proof.Errors++
			proof.Forced = true
		}
		if err := resourceBrowserOffline(c, factory); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		if _, err := resourceBrowserInventory(root, 256<<20, false); err != nil {
			proof.Errors++
			proof.Forced = true
		}
		if stat, err := log.Stat(); err != nil || stat.Size() > 1<<20 {
			proof.Errors++
			proof.Forced = true
		}
		noChildren := false
		for {
			var status unix.WaitStatus
			pid, waitErr := unix.Wait4(-1, &status, unix.WNOHANG, nil)
			if errors.Is(waitErr, unix.EINTR) {
				continue
			}
			if errors.Is(waitErr, unix.ECHILD) {
				noChildren = true
				break
			}
			if waitErr != nil {
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
		elapsed := time.Since(entered)
		if elapsed >= 90*time.Second {
			proof.DeadlineExceeded = true
		}
		if noChildren && primaryExited {
			proof.Complete = true
			proof.DescendantsReaped = true
			return proof
		}
		if (proof.Forced || proof.DeadlineExceeded) && forcePhase == 0 {
			proof.Forced = true
			forcePhase = 1
			forcedAt = time.Now()
			if productScopeSignal(known, unix.SIGTERM) != nil {
				proof.Errors++
			}
		}
		if elapsed >= 100*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 10*time.Second) || proof.Errors > 0 {
			proof.Forced = true
			forcePhase = 2
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
		if (elapsed >= 110*time.Second || (!forcedAt.IsZero() && time.Since(forcedAt) >= 20*time.Second)) && !reportedUnjoined {
			reportedUnjoined = true
			proof.Forced = true
			_ = resourceBrowserWrite(filepath.Join(root, "unjoined-failure.json"), proof)
			fmt.Fprintln(os.Stderr, "FAIL: browser descendants unjoined; private evidence retained; hosted timeout is containment only")
		}
		// We keep owning children until joined or the finite hosted Go/job boundary
		// terminates the FAILED test. That boundary cannot generate Complete=true.
		pause := 250 * time.Millisecond
		if forcePhase != 0 {
			pause = 25 * time.Millisecond
		}
		time.Sleep(pause)
	}
}

type resourceBrowserHTTP struct {
	Schema            int  `json:"schema"`
	Requests          int  `json:"requests"`
	StateRequests     int  `json:"stateRequests"`
	StateReady        int  `json:"stateReady"`
	LoginRequests     int  `json:"loginRequests"`
	ListRequests      int  `json:"listRequests"`
	ListResponses     int  `json:"listResponses"`
	Snapshots         int  `json:"snapshots"`
	SnapshotResponses int  `json:"snapshotResponses"`
	Blocked           int  `json:"blocked"`
	Errors            int  `json:"errors"`
	Captures          int  `json:"captures"`
	Completed         bool `json:"completed"`
	RequestsJoined    bool `json:"requestsJoined"`
}
type resourceBrowserSummary struct {
	Schema         int  `json:"schema"`
	Expected       int  `json:"expected"`
	Observed       int  `json:"observed"`
	Passed         int  `json:"passed"`
	Errors         int  `json:"errors"`
	SelectionValid bool `json:"selectionValid"`
	Unexpected     bool `json:"unexpected"`
	Accepted       bool `json:"accepted"`
}

func resourceBrowserVerifyResult(root string, c *Core, descriptor resource.Descriptor) error {
	var summary resourceBrowserSummary
	if resourceBrowserRead(filepath.Join(root, "browser-summary.json"), 4096, &summary) != nil || summary.Schema != 1 || summary.Expected != 1 || summary.Observed != 1 || summary.Passed != 1 || summary.Errors != 0 || !summary.SelectionValid || summary.Unexpected || !summary.Accepted {
		return errors.New("exact browser case did not pass")
	}
	var counts resourceBrowserHTTP
	if resourceBrowserRead(filepath.Join(root, "http-observations.json"), 4096, &counts) != nil || counts.Schema != 1 || counts.Requests < 1 || counts.Requests > 256 || counts.StateRequests < 1 || counts.StateRequests > 96 || counts.StateReady < 1 || counts.LoginRequests != 1 || counts.ListRequests != 2 || counts.ListResponses != 2 || counts.Snapshots != 1 || counts.SnapshotResponses != 1 || counts.Blocked != 0 || counts.Errors != 0 || counts.Captures != 2 || !counts.Completed || !counts.RequestsJoined {
		return errors.New("exact browser HTTP observations failed")
	}
	var input json.RawMessage
	if resourceBrowserRead(filepath.Join(root, "catalog-request.json"), 4096, &input) != nil {
		return errors.New("actual catalog request unavailable")
	}
	request, err := decodeResourceCatalogRequest(input)
	if err != nil || len(request.Sources) != 3 || request.Sources[0].Kind != resourcecatalog.LocalSettings || request.Sources[0].Target != descriptor.Target || request.Sources[1].Kind != resourcecatalog.LocalService || request.Sources[2].Kind != resourcecatalog.TransferActivity {
		return errors.New("actual catalog selection changed")
	}
	var raw json.RawMessage
	if resourceBrowserRead(filepath.Join(root, "catalog-result.json"), 1<<20, &raw) != nil {
		return errors.New("actual catalog response unavailable")
	}
	response, err := DecodeResourceCatalogResponse(raw)
	if err != nil || !response.Snapshot.Complete || len(response.Snapshot.Sources) != 3 {
		return errors.New("production catalog decoder rejected browser response")
	}
	seen := make(map[string]bool)
	for _, source := range response.Snapshot.Sources {
		if seen[source.Selection.Kind] || source.State != "current" || !source.Complete || source.Total == nil || *source.Total != int64(len(source.Rows)) {
			return errors.New("catalog source completeness failed")
		}
		seen[source.Selection.Kind] = true
		switch source.Selection.Kind {
		case resourcecatalog.LocalSettings:
			if len(source.Rows) != 1 || source.Rows[0].LocalSettings == nil || !reflect.DeepEqual(*source.Rows[0].LocalSettings, descriptor) {
				return errors.New("exact local settings descriptor changed")
			}
		case resourcecatalog.LocalService:
			if len(source.Rows) != 1 || source.Rows[0].Identity.ID != "synthetic-service" || source.Rows[0].LocalService == nil || source.Rows[0].LocalService.State != "saved" || source.Rows[0].LocalService.Application != "unverified" || source.Rows[0].LocalService.Lifetime != "until-stopped" {
				return errors.New("inactive saved service identity changed")
			}
		case resourcecatalog.TransferActivity:
			if len(source.Rows) != 0 || source.Selection.ProcessID != c.resourceCatalogProcessID() {
				return errors.New("empty real transfer process identity changed")
			}
		default:
			return errors.New("unexpected catalog source")
		}
	}
	return nil
}

func TestResourceBrowserNativeLocalCatalog(t *testing.T) {
	// Complete guard before temporary state, signal registration, Core, sockets,
	// subprocesses or PR_SET_CHILD_SUBREAPER. Exact hosted gate supplies provenance.
	if os.Getenv("SOBALINK_RUN_RESOURCE_BROWSER_NATIVE") != "reviewed-offline-catalog-settings-v1" {
		t.Skip("requires exact independently reviewed offline browser invocation")
	}
	run, parallel, timeout, count := flag.Lookup("test.run"), flag.Lookup("test.parallel"), flag.Lookup("test.timeout"), flag.Lookup("test.count")
	if run == nil || run.Value.String() != resourceBrowserSelector || parallel == nil || parallel.Value.String() != "1" || count == nil || count.Value.String() != "1" || timeout == nil || timeout.Value.String() != "2m0s" {
		t.Fatal("exact single browser selector and finite hosted bounds required")
	}
	if os.Getenv("SOBA_RESOURCE_BROWSER_SCOPE") != "hosted-job-v1" {
		t.Fatal("reviewed bounded hosted browser scope required")
	}
	for _, name := range []string{"SOBALINK_RUN_ACTIVATION_NATIVE", "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE", "SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE", "SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE", "SOBALINK_RUN_RESOURCE_PROCESS_NATIVE", "SOBALINK_RUN_WEB_ACTIVATION_NATIVE", "SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE", "SOBALINK_RUN_MANAGED_RESTART_NATIVE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY", "DEBUG", "PWDEBUG", "NODE_OPTIONS", "NODE_PATH"} {
		if os.Getenv(name) != "" {
			t.Fatal("browser invocation contains an unrelated opt-in or ambient override")
		}
	}
	root, webDir := os.Getenv("SOBA_RESOURCE_BROWSER_PRIVATE_ROOT"), os.Getenv("SOBA_RESOURCE_BROWSER_WEB_DIR")
	if resourceBrowserPrivateDir(root) != nil || !filepath.IsAbs(webDir) || filepath.Clean(webDir) != webDir {
		t.Fatal("exact private root and source web directory required")
	}
	resolved, err := filepath.EvalSymlinks(webDir)
	if err != nil || resolved != webDir {
		t.Fatal("source web directory alias forbidden")
	}
	node, chromium := os.Getenv("SOBA_RESOURCE_BROWSER_NODE"), os.Getenv("SOBA_RESOURCE_BROWSER_CHROMIUM")
	nodeHash, chromiumHash := os.Getenv("SOBA_RESOURCE_BROWSER_NODE_SHA256"), os.Getenv("SOBA_RESOURCE_BROWSER_CHROMIUM_SHA256")
	if !filepath.IsAbs(node) || !filepath.IsAbs(chromium) || !resource.ValidDigest(nodeHash) || !resource.ValidDigest(chromiumHash) || resourceBrowserVerifyBinary(node, nodeHash) != nil || resourceBrowserVerifyBinary(chromium, chromiumHash) != nil {
		t.Fatal("frozen browser executables do not match")
	}
	for _, name := range []string{"session.json", "catalog-result.json", "catalog-request.json", "http-observations.json", "browser-summary.json", "scope-proof.json", "native-result.json", "unjoined-failure.json", "browser-private.log", "product", "resource-browser-cancel"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("browser root is not a fresh invocation")
		}
	}
	entered := time.Now()
	cancellation := make(chan os.Signal, 2)
	signal.Notify(cancellation, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(cancellation)
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		t.Fatal("Linux browser descendant ownership unavailable")
	}
	defer unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	for _, name := range []string{"home", "tmp", "config", "cache", "data", "runtime", "captures"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal("private browser directories unavailable")
		}
	}
	dir := filepath.Join(root, "product")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal("private product directory unavailable")
	}
	profile := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "light", Network: "none", Hostname: "synthetic-browser"}, Peers: []Trust{}, Services: []ServiceSpec{{ID: "synthetic-service", Name: "Synthetic service", Direction: "forward", Network: "tcp", Ports: "8080", Lifetime: "until-stopped", LoopbackHost: "127.0.0.1", PeerID: "synthetic-peer"}}}
	if validateProfile(profile) != nil || resourceBrowserWrite(filepath.Join(dir, "sobalink.json"), profile) != nil {
		t.Fatal("inert synthetic profile unavailable")
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal("synthetic lifecycle ownership unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var factory atomic.Uint32
	c, openErr := Open(ctx, Options{Directory: dir, Version: "synthetic-browser-catalog", LifecycleLock: lock, EnableResourceInspection: true, SkipNetworkStart: true, NodeFactory: func(string, string) (NetworkBackend, error) {
		factory.Add(1)
		return nil, errors.New("offline browser backend forbidden")
	}})
	if openErr != nil || c == nil {
		_ = lock.Close()
		t.Fatal("ordinary owned offline Core failed")
	}
	closed := false
	defer func() {
		if !closed {
			cancel()
			if c.Close() != nil {
				t.Error("failed browser Core closure")
			}
			if lock.Close() != nil {
				t.Error("failed browser lock closure")
			}
		}
	}()
	if resourceBrowserOffline(c, &factory) != nil {
		t.Fatal("initial offline structural oracle failed")
	}
	c.op.Lock()
	descriptor := c.resourceDescriptor(c.capacityPolicy(), c.profileCopy())
	c.op.Unlock()
	before, err := resourceBrowserInventory(dir, 32<<20, true)
	if err != nil {
		t.Fatal("owned product baseline unavailable")
	}
	assets, err := web.Assets()
	if err != nil {
		t.Fatal("production embedded assets unavailable")
	}
	paths := []string{}
	err = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && path != "index.html" {
			paths = append(paths, "/"+path)
		}
		return nil
	})
	if err != nil || len(paths) < 1 || len(paths) > 32 {
		t.Fatal("bounded production asset inventory unavailable")
	}
	sort.Strings(paths)
	url, code, err := c.StartWeb(assets)
	if err != nil {
		t.Fatal("production local Web startup failed")
	}
	session := struct {
		Schema    int      `json:"schema"`
		URL       string   `json:"url"`
		Code      string   `json:"code"`
		ProcessID string   `json:"processID"`
		Assets    []string `json:"assets"`
	}{1, url, code, c.resourceCatalogProcessID(), paths}
	if resourceBrowserWrite(filepath.Join(root, "session.json"), session) != nil {
		t.Fatal("private session handoff failed")
	}
	code = ""
	session.Code = ""
	proof := resourceBrowserRunScope(c, &factory, root, webDir, node, chromium, entered, cancellation)
	if resourceBrowserWrite(filepath.Join(root, "scope-proof.json"), proof) != nil {
		t.Fatal("browser ownership receipt failed")
	}
	resultErr := resourceBrowserVerifyResult(root, c, descriptor)
	if resourceBrowserOffline(c, &factory) != nil {
		resultErr = errors.New("final offline structural oracle failed")
	}
	after, inventoryErr := resourceBrowserInventory(dir, 32<<20, true)
	if inventoryErr != nil || !reflect.DeepEqual(before, after) {
		resultErr = errors.New("browser changed owned product state")
	}
	// No browser descendant may still be alive on the passing path. The finite
	// hosted alarm may terminate a failed/unjoined test, but cannot reach here.
	cancel()
	closeErr := c.Close()
	lockErr := lock.Close()
	closed = true
	accepted := proof.Complete && proof.DescendantsReaped && proof.PlaywrightExit == 0 && !proof.Forced && !proof.DeadlineExceeded && proof.Errors == 0 && resultErr == nil && closeErr == nil && lockErr == nil && time.Since(entered) < 110*time.Second
	receipt := struct {
		Schema          int  `json:"schema"`
		Accepted        bool `json:"accepted"`
		BrowserJoined   bool `json:"browserJoined"`
		CoreClosed      bool `json:"coreClosed"`
		LockClosed      bool `json:"lockClosed"`
		HTTPOnly        bool `json:"httpCountsOnly"`
		SavedNavigation bool `json:"savedServiceNavigationCovered"`
		PixelReview     bool `json:"pixelReviewPerformed"`
	}{1, accepted, proof.Complete && proof.DescendantsReaped, closeErr == nil, lockErr == nil, true, false, false}
	if resourceBrowserWrite(filepath.Join(root, "native-result.json"), receipt) != nil {
		t.Fatal("native browser result receipt failed")
	}
	// Keep the protected root for the parent to inspect/reconstruct. Never export
	// the profile, descriptor, raw replies, framework output or unscreened images.
	if !accepted {
		t.Fatal("real offline browser acceptance failed; protected evidence retained")
	}
	t.Log("PASS: one real offline browser catalog/settings case; owned cleanup joined; HTTP counts only; pixel review remains separate")
}
