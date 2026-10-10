//go:build resource_process_native && directlan_activation_native && linux && (amd64 || arm64)

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pm "github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
	"golang.org/x/sys/unix"
)

// One retained once-only closer for every registered parent-owned endpoint.
// A timer cannot turn an unfinished Close or I/O task into a joined result.
type p1File struct {
	file *os.File
	once sync.Once
	done chan struct{}
	gate <-chan struct{}
	err  error
}

func (p *p1File) close() {
	if p != nil {
		p.once.Do(func() {
			go func() {
				defer close(p.done)
				if p.gate != nil {
					<-p.gate
				}
				p.err = p.file.Close()
			}()
		})
	}
}
func (p *p1File) joined(until time.Time) bool {
	if p == nil {
		return true
	}
	p.close()
	return p1Wait(p.done, until) && p.err == nil
}
func (f *p1Fixture) retain(file *os.File) *p1File {
	p := &p1File{file: file, done: make(chan struct{})}
	f.filesMu.Lock()
	defer f.filesMu.Unlock()
	f.require(len(f.files) < 512, "file_owner_capacity")
	f.files = append(f.files, p)
	return p
}
func p1Wait(done <-chan struct{}, until time.Time) bool {
	if !time.Now().Before(until) {
		return false
	}
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-done:
		return time.Now().Before(until)
	case <-timer.C:
		return false
	}
}
func p1Min(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (f *p1Fixture) admit(budget time.Duration, phase time.Time) time.Time {
	f.healthy()
	now := time.Now()
	until := now.Add(budget)
	f.require(budget > 0 && until.Before(phase) && until.Before(f.cleanup), "phase_budget_exhausted")
	return until
}
func (f *p1Fixture) owner(role pm.OwnerRole) *p1Child {
	if role < pm.RoleC0 || role > pm.RoleC2 {
		return nil
	}
	return f.owners[int(role)-1]
}
func (f *p1Fixture) healthy() {
	for _, c := range f.owners {
		if c != nil {
			f.require(!c.failed.Load(), "owner_evidence_failed")
		}
	}
	if f.activeCLI != nil {
		f.require(!f.activeCLI.failed.Load(), "cli_evidence_failed")
	}
}
func (f *p1Fixture) liveOwners() int {
	n := 0
	for _, c := range f.owners {
		if c != nil && !c.joined {
			n++
		}
	}
	return n
}

type p1Child struct {
	fixture                                         *p1Fixture
	invocation                                      pm.Invocation
	role                                            pm.OwnerRole
	cmd                                             *exec.Cmd
	binding                                         pm.Bootstrap
	plan                                            pm.CheckpointPlan
	input, parentOutput, parentError                *p1File
	childInput, childOutput, childError             *p1File
	stdoutFile, stderrFile, eventsFile, summaryFile *p1File
	pidfd                                           *p1File
	startDone, startTaskDone, exitDone, waitDone    chan struct{}
	stdoutDone, stderrDone, writerDone              chan struct{}
	startErr, waitErr, pollErr                      error
	nativeMu                                        sync.Mutex
	exitObserved, waitStarted                       bool
	exitCode                                        int
	joined                                          bool
	streamsStarted, pollStarted, cliValidated       bool
	failed                                          atomic.Bool
	changed                                         chan struct{}
	requests                                        chan p1Input
	finishInput                                     chan struct{}
	finishOnce                                      sync.Once
	mu                                              sync.Mutex
	boot, entry, sealed, stdoutEOF, stderrEOF       bool
	events                                          []pm.Event
	eventCount                                      uint64
	checkpoint                                      uint64
	stdoutBytes, stderrBytes, observerBytes         uint64
	summary                                         pm.TranscriptSummary
}

func (c *p1Child) notify() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}
func (c *p1Child) invalidate() { c.failed.Store(true); c.notify() }
func (c *p1Child) closeInput() { c.finishOnce.Do(func() { close(c.finishInput) }); c.input.close() }

func (f *p1Fixture) startOwner(role pm.OwnerRole) {
	f.require(f.owner(role) == nil && f.liveOwners() < 3 && f.activeCLI == nil && f.driver == nil, "owner_schedule")
	switch role {
	case pm.RoleC0:
		f.require(f.cliCount == 0, "owner_c0_order")
	case pm.RoleA0:
		f.require(f.owner(pm.RoleC0) != nil && f.cliCount == 0, "owner_a_order")
	case pm.RoleB0:
		f.require(f.owner(pm.RoleA0) != nil && f.cliCount == 0, "owner_b_order")
	case pm.RoleC1:
		f.require(f.cliCount == 7 && f.owner(pm.RoleC0).joined, "owner_c1_predecessor")
	case pm.RoleC2:
		f.require(f.cliCount == 25 && f.owner(pm.RoleC1).joined, "owner_c2_predecessor")
	}
	phase := f.setup
	if role == pm.RoleC2 {
		phase = f.work
	}
	until := f.admit(5*time.Second, phase)
	child := f.launch(pm.Invocation{Mode: pm.Owner, Role: role}, role, until)
	f.owners[int(role)-1] = child
	f.waitReadiness(child, f.admit(15*time.Second, phase))
}

func (f *p1Fixture) childEnvironment() []string {
	return []string{"HOME=" + filepath.Join(f.root, "home"), "USERPROFILE=" + filepath.Join(f.root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(f.root, "config"), "XDG_CACHE_HOME=" + filepath.Join(f.root, "cache"), "APPDATA=" + filepath.Join(f.root, "appdata"), "LOCALAPPDATA=" + filepath.Join(f.root, "localappdata"), "TMPDIR=" + filepath.Join(f.root, "tmp"), "TMP=" + filepath.Join(f.root, "tmp"), "TEMP=" + filepath.Join(f.root, "tmp"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "RUNEWIDTH_EASTASIAN=0", "GOTRACEBACK=single"}
}
func (f *p1Fixture) launch(inv pm.Invocation, role pm.OwnerRole, until time.Time) *p1Child {
	args := []string{f.manifest.Binary, "--state-dir", f.roleDir(role), "--locale", "en", "run"}
	if inv.Mode == pm.Owner {
		if role != pm.RoleC2 {
			args = append(args, "--offline")
		}
	} else {
		selectedRole, operation, suffix, locale, dry := f.cliArguments(int(inv.Ordinal))
		f.require(inv.Mode == pm.CLI && selectedRole == role && operation == inv.Operation, "launch_selection")
		args = []string{f.manifest.Binary, "--state-dir", f.roleDir(role), "--locale", locale, "--json-errors"}
		if dry {
			args = append(args, "--dry-run")
		}
		args = append(args, suffix...)
	}
	f.checkBinary()
	c := &p1Child{fixture: f, invocation: inv, role: role, exitCode: -1, startDone: make(chan struct{}), startTaskDone: make(chan struct{}), exitDone: make(chan struct{}), waitDone: make(chan struct{}), stdoutDone: make(chan struct{}), stderrDone: make(chan struct{}), writerDone: make(chan struct{}), changed: make(chan struct{}, 1), requests: make(chan p1Input, 1), finishInput: make(chan struct{})}
	limit := pm.MaxOwnerEvents
	if inv.Mode == pm.CLI {
		limit = pm.MaxCLIEvents
	}
	c.events = make([]pm.Event, limit)
	// The slot owns even a failed or delayed Start before any syscall can spawn.
	if inv.Mode == pm.Owner {
		f.owners[int(inv.Role)-1] = c
	} else {
		f.clis[int(inv.Ordinal)-1] = c
		f.activeCLI = c
	}
	ri, wi, e := os.Pipe()
	f.require(e == nil, "stdin_pipe")
	c.childInput = f.retain(ri)
	c.input = f.retain(wi)
	ro, wo, e := os.Pipe()
	f.require(e == nil, "stdout_pipe")
	c.parentOutput = f.retain(ro)
	c.childOutput = f.retain(wo)
	re, we, e := os.Pipe()
	f.require(e == nil, "stderr_pipe")
	c.parentError = f.retain(re)
	c.childError = f.retain(we)
	ordinal := int(inv.Ordinal) + 5
	if inv.Mode == pm.Owner {
		ordinal = int(inv.Role)
	}
	evidence := filepath.Join(f.root, "evidence", "launch"+strconv.Itoa(ordinal))
	f.require(os.Mkdir(evidence, 0700) == nil, "evidence_directory")
	for i, name := range []string{"stdout", "stderr", "events", "summary.json"} {
		file, err := os.OpenFile(filepath.Join(evidence, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		f.require(err == nil, "evidence_open")
		retained := f.retain(file)
		switch i {
		case 0:
			c.stdoutFile = retained
		case 1:
			c.stderrFile = retained
		case 2:
			c.eventsFile = retained
		case 3:
			c.summaryFile = retained
		}
	}
	c.cmd = exec.Command(args[0], args[1:]...)
	c.cmd.Env = f.childEnvironment()
	c.cmd.Dir = f.root
	c.cmd.Stdin = ri
	c.cmd.Stdout = wo
	c.cmd.Stderr = we
	for _, endpoint := range []*p1File{c.childInput, c.childOutput, c.childError} {
		endpoint.gate = c.startDone
	}
	go func() {
		defer close(c.startTaskDone)
		c.startErr = c.cmd.Start()
		close(c.startDone)
		// Publication precedes the latch check: cleanup either sees this Start
		// or this retained completion owner sees cleanup, with no lost window.
		if f.failureCleanup.Load() {
			c.terminate(syscall.SIGKILL)
		}
	}()
	f.require(p1Wait(c.startDone, until) && c.startErr == nil && c.cmd.Process != nil, "child_start")
	group, groupErr := syscall.Getpgid(c.cmd.Process.Pid)
	f.require(groupErr == nil && group == f.supervisorGroup, "inherited_group")
	var handleErr error
	e = c.cmd.Process.WithHandle(func(handle uintptr) {
		fd, err := unix.FcntlInt(uintptr(handle), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			handleErr = err
			return
		}
		c.pidfd = f.retain(os.NewFile(uintptr(fd), "p1-spawn-pidfd"))
	})
	f.require(e == nil && handleErr == nil && c.pidfd != nil, "spawn_handle")
	c.pollStarted = true
	go c.observeExit()
	f.require(c.childInput.joined(until) && c.childOutput.joined(until) && c.childError.joined(until), "child_pipe_copies")
	c.binding = f.bootstrap(c)
	var valid bool
	if inv.Mode == pm.Owner {
		c.plan.Count = []uint8{2, 9, 8, 4, 3}[int(inv.Role)-1]
	}
	for i := 0; i < int(c.plan.Count); i++ {
		_, e := rand.Read(c.plan.Nonces[i][:])
		f.require(e == nil && c.plan.Nonces[i] != [32]byte{}, "checkpoint_random")
	}
	decoder, valid := pm.NewTranscriptDecoder(c.binding, c.plan)
	f.require(valid, "transcript_binding")
	c.streamsStarted = true
	go c.readStdout(decoder)
	go c.readStderr()
	go c.writeInput()
	encoded, valid := pm.EncodeBootstrap(c.binding)
	f.require(valid, "bootstrap_encode")
	request := p1Input{length: encoded.Length, done: make(chan struct{})}
	copy(request.bytes[:], encoded.Bytes[:encoded.Length])
	c.requests <- request
	f.require(p1Wait(request.done, until), "bootstrap_write")
	f.waitBoot(c, until)
	return c
}
func (f *p1Fixture) bootstrap(c *p1Child) pm.Bootstrap {
	now := time.Now()
	b := pm.Bootstrap{Scenario: pm.ScenarioProcessRecovery, FixtureID: f.fixtureID, Invocation: c.invocation, SupervisorPID: uint64(os.Getpid()), ChildPID: uint64(c.cmd.Process.Pid), RoleDirectory: p1Path(f.roleDir(c.role)), RoleDirectoryIdentity: p1Identity(f.roleInfo[p1RoleIndex(c.role)]), WorkingDirectory: p1Path(f.root), WorkingDirectoryIdentity: p1Identity(f.rootInfo), Schedule: pm.ScheduleBinding{EpochSeconds: uint64(f.epoch), EntrySeconds: uint64(now.Unix()), EntryNanoseconds: uint32(now.Nanosecond()), SetupCutoffSeconds: uint64(f.epoch + 90), WorkCutoffSeconds: uint64(f.epoch + 300), CleanupCutoffSeconds: uint64(f.epoch + 330), OuterCutoffSeconds: uint64(f.epoch + 360), OriginalGrantExpirySeconds: uint64(f.epoch + 360)}}
	b.Target = pm.LinuxAMD64
	if f.manifest.Target == "linux-arm64" {
		b.Target = pm.LinuxARM64
	}
	for _, target := range []struct {
		s string
		b []byte
	}{{f.manifest.SourceCommit, b.SourceCommit[:]}, {f.manifest.SourceTree, b.SourceTree[:]}, {f.manifest.SourceManifestSHA256, b.SourceManifestSHA256[:]}, {f.manifest.BinarySHA256, b.ExecutableSHA256[:]}, {f.manifest.AssetSHA256, b.AssetSHA256[:]}} {
		raw, e := hex.DecodeString(target.s)
		f.require(e == nil && len(raw) == len(target.b), "bootstrap_digest")
		copy(target.b, raw)
	}
	var ok bool
	b.ArgvSHA256, ok = pm.DigestArgv(c.cmd.Args)
	f.require(ok, "argv_digest")
	for _, dst := range [][]byte{b.LaunchID[:], b.Challenge[:], b.AuthenticationKey[:]} {
		_, e := rand.Read(dst)
		f.require(e == nil, "launch_random")
	}
	return b
}
func (c *p1Child) observeExit() {
	defer close(c.exitDone)
	conn, err := c.pidfd.file.SyscallConn()
	if err != nil {
		c.pollErr = err
		c.invalidate()
		return
	}
	// Only the retained duplicate of this cmd.Process handle is ever polled.
	for time.Now().Before(c.fixture.cleanup) {
		var pollErr error
		var exited bool
		err = conn.Control(func(fd uintptr) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, pollErr = unix.Poll(fds, 100)
			exited = fds[0].Revents&unix.POLLIN != 0
			if fds[0].Revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
				pollErr = errors.New("pidfd poll failure")
			}
		})
		if err != nil || pollErr != nil && pollErr != unix.EINTR {
			c.pollErr = errors.New("pidfd observation failure")
			c.invalidate()
			return
		}
		if exited {
			c.nativeMu.Lock()
			c.exitObserved = true
			c.nativeMu.Unlock()
			c.notify()
			return
		}
	}
	c.pollErr = errors.New("pidfd observation deadline")
	c.invalidate()
}
func (c *p1Child) terminate(signal syscall.Signal) {
	select {
	case <-c.startDone:
	default:
		return
	}
	c.nativeMu.Lock()
	defer c.nativeMu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil || c.waitStarted || c.exitObserved {
		return
	}
	// Use only this retained spawn Process under the same lock as Wait. A
	// failure before WithHandle proof remains unverified, never a PID reopen.
	// Only the external runner owns the inherited supervisor-group signal.
	if err := c.cmd.Process.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) && err != syscall.ESRCH {
		c.invalidate()
	}
}
func (c *p1Child) startWait() {
	c.nativeMu.Lock()
	defer c.nativeMu.Unlock()
	if c.waitStarted || !c.exitObserved {
		return
	}
	c.waitStarted = true
	go func() {
		defer close(c.waitDone)
		c.waitErr = c.cmd.Wait()
		if c.cmd.ProcessState != nil {
			c.exitCode = c.cmd.ProcessState.ExitCode()
		}
	}()
}
func (f *p1Fixture) joinChild(c *p1Child, until time.Time) {
	f.require(c != nil && !c.joined, "join_owner")
	f.require(p1Wait(c.startTaskDone, until), "start_task_unjoined")
	f.require(p1Wait(c.exitDone, until), "child_exit_unobserved")
	c.nativeMu.Lock()
	exited := c.exitObserved
	c.nativeMu.Unlock()
	f.require(exited && c.pollErr == nil, "child_exit_failed")
	c.startWait()
	f.require(p1Wait(c.waitDone, until), "child_wait_unjoined")
	f.require(p1Wait(c.stdoutDone, until) && p1Wait(c.stderrDone, until) && p1Wait(c.writerDone, until), "pipe_tasks_unjoined")
	c.mu.Lock()
	valid := c.boot && c.entry && c.sealed && c.stdoutEOF && c.stderrEOF && c.summary.StructurallyClosed
	c.mu.Unlock()
	f.require(valid && !c.failed.Load(), "child_transcript_unsealed")
	receipt := struct {
		Role            pm.OwnerRole         `json:"role"`
		Ordinal         uint8                `json:"ordinal"`
		Summary         pm.TranscriptSummary `json:"summary"`
		NativeExit      bool                 `json:"nativeExit"`
		WaitJoined      bool                 `json:"waitJoined"`
		PipeTasksJoined bool                 `json:"pipeTasksJoined"`
	}{c.role, c.invocation.Ordinal, c.summary, true, true, true}
	raw, err := json.Marshal(receipt)
	f.require(err == nil && len(raw) < 4096 && c.retainBytes(c.summaryFile, append(raw, '\n'), 2), "child_summary_receipt")
	for _, p := range []*p1File{c.input, c.parentOutput, c.parentError, c.stdoutFile, c.stderrFile, c.eventsFile, c.summaryFile, c.pidfd} {
		f.require(p.joined(until), "child_handle_unjoined")
	}
	if c.invocation.Mode == pm.Owner {
		f.require(c.exitCode == 0 && c.waitErr == nil, "owner_product_exit")
	} else if c.exitCode == 0 {
		f.require(c.waitErr == nil, "cli_wait_error")
	} else {
		var exitErr *exec.ExitError
		f.require(errors.As(c.waitErr, &exitErr) && exitErr.ProcessState.ExitCode() == c.exitCode, "cli_exit_error")
	}
	c.joined = true
}
func (f *p1Fixture) closeFailure() {
	f.failureCleanup.Store(true)
	if f.driver != nil {
		f.driver.cancel()
		if !p1Wait(f.driver.done, p1Min(time.Now().Add(time.Second), f.cleanup.Add(-12*time.Second))) {
			f.stage = "driver_cleanup_unjoined"
		} else {
			f.driver = nil
		}
	}
	if f.activeCLI != nil {
		f.activeCLI.closeInput()
	}
	// Explicitly reserve native closure; no new cleanup interval is created.
	for _, c := range f.owners {
		if f.driver != nil || f.activeCLI != nil && !f.activeCLI.joined {
			break
		}
		if c == nil || c.joined || c.cmd == nil {
			continue
		}
		select {
		case <-c.startDone:
		default:
			continue
		}
		if c.startErr != nil {
			continue
		}
		until := time.Now().Add(4 * time.Second)
		if !until.Before(f.cleanup.Add(-12 * time.Second)) {
			break
		}
		ctx, cancel := context.WithDeadline(context.Background(), until)
		task := &p1Driver{cancel: cancel, done: make(chan struct{})}
		f.driver = task
		go func() { defer close(task.done); task.err = controlCall(ctx, f.roleDir(c.role), "stop", nil) }()
		joined := p1Wait(task.done, until)
		cancel()
		if !joined {
			f.stage = "driver_cleanup_unjoined"
			break
		}
		f.driver = nil
	}
	for _, c := range f.children() {
		if c == nil || c.joined {
			continue
		}
		c.closeInput()
	}
	termUntil := p1Min(time.Now().Add(2*time.Second), f.cleanup.Add(-8*time.Second))
	if time.Now().Before(f.cleanup.Add(-8 * time.Second)) {
		for _, c := range f.children() {
			if c != nil && !c.joined {
				f.observeDelayedStart(c)
				c.terminate(syscall.SIGTERM)
			}
		}
		for _, c := range f.children() {
			if c != nil && !c.joined && c.pidfd != nil {
				_ = p1Wait(c.exitDone, termUntil)
			}
		}
	}
	for _, c := range f.children() {
		if c != nil && !c.joined {
			f.observeDelayedStart(c)
			c.terminate(syscall.SIGKILL)
		}
	}
	until := f.cleanup.Add(-2 * time.Second)
	for _, c := range f.children() {
		if c == nil || c.joined {
			continue
		}
		c.parentOutput.close()
		c.parentError.close()
		if c.pidfd != nil && p1Wait(c.exitDone, until) {
			c.startWait()
			_ = p1Wait(c.waitDone, until)
		}
	}
}
func (f *p1Fixture) observeDelayedStart(c *p1Child) {
	if c.cmd == nil {
		return
	}
	select {
	case <-c.startDone:
	default:
		return
	}
	if c.startErr != nil || c.cmd.Process == nil {
		return
	}
	// A launch failure before pidfd installation still owns this unreaped direct
	// child; no arbitrary PID reopen. Retain its real spawn handle if available.
	if c.pidfd == nil {
		_ = c.cmd.Process.WithHandle(func(handle uintptr) {
			fd, e := unix.FcntlInt(handle, unix.F_DUPFD_CLOEXEC, 3)
			if e == nil {
				c.pidfd = f.retain(os.NewFile(uintptr(fd), "p1-spawn-pidfd"))
			}
		})
	}
	if c.pidfd != nil && !c.pollStarted {
		c.pollStarted = true
		go c.observeExit()
	}
}
func (f *p1Fixture) children() []*p1Child {
	all := make([]*p1Child, 0, 45)
	all = append(all, f.owners[:]...)
	all = append(all, f.clis[:]...)
	return all
}

// Native/task closure is independent of a valid application transcript.
// Counts are retained by kind; a failed scenario cannot become passed here.
type p1Unjoined struct {
	Start       int `json:"start"`
	Exit        int `json:"exit"`
	Wait        int `json:"wait"`
	Stdout      int `json:"stdout"`
	Stderr      int `json:"stderr"`
	Writer      int `json:"writer"`
	Handle      int `json:"handle"`
	Driver      int `json:"driver"`
	Reservation int `json:"reservation"`
}

func (f *p1Fixture) closeFixture() bool {
	for i, r := range f.reservations {
		if r != nil && !f.released[i] {
			if r.Close() != nil {
				f.unjoined.Reservation++
			} else {
				f.released[i] = true
			}
		}
	}
	if f.driver != nil {
		f.driver.cancel()
		if !p1Wait(f.driver.done, f.cleanup) {
			f.unjoined.Driver++
		}
	}
	nativeUntil := f.cleanup.Add(-2 * time.Second)
	for _, c := range f.children() {
		if c == nil {
			continue
		}
		if c.cmd != nil {
			if !p1Wait(c.startTaskDone, nativeUntil) {
				f.unjoined.Start++
			} else if c.startErr == nil {
				f.observeDelayedStart(c)
				if f.failureCleanup.Load() {
					c.terminate(syscall.SIGKILL)
				}
				if !c.pollStarted || !p1Wait(c.exitDone, nativeUntil) {
					f.unjoined.Exit++
				} else {
					c.startWait()
				}
				c.nativeMu.Lock()
				waiting := c.waitStarted
				c.nativeMu.Unlock()
				if !waiting || !p1Wait(c.waitDone, nativeUntil) {
					f.unjoined.Wait++
				}
			}
		}
		if !c.joined {
			c.closeInput()
			c.parentOutput.close()
			c.parentError.close()
		}
	}
	// All once-only endpoint closers are requested before any join. A child-end
	// closer waits for its retained Start publication; no FD is closed under Start.
	f.filesMu.Lock()
	files := append([]*p1File{}, f.files...)
	f.filesMu.Unlock()
	for _, file := range files {
		file.close()
	}
	for _, c := range f.children() {
		if c == nil {
			continue
		}
		// Re-observe tasks that missed the reserved native stage while spending only
		// the remaining original endpoint-closure interval. Earlier misses stay failed.
		if c.cmd != nil {
			_ = p1Wait(c.startTaskDone, f.cleanup)
		}
		if c.pollStarted {
			_ = p1Wait(c.exitDone, f.cleanup)
		}
		if c.streamsStarted {
			if !p1Wait(c.stdoutDone, f.cleanup) {
				f.unjoined.Stdout++
			}
			if !p1Wait(c.stderrDone, f.cleanup) {
				f.unjoined.Stderr++
			}
			if !p1Wait(c.writerDone, f.cleanup) {
				f.unjoined.Writer++
			}
		}
	}
	for _, file := range files {
		if !file.joined(f.cleanup) {
			f.unjoined.Handle++
		}
	}
	return f.unjoined == (p1Unjoined{})
}
