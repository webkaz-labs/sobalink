//go:build resource_process_native

package resourceacceptance

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

const (
	processEntryEntering    = uint32(1)
	processEntryReady       = uint32(2)
	processEntryFinishing   = uint32(3)
	processEntryTerminal    = uint32(4)
	processEntryFailureCode = 72
	processEntryMaxFiles    = 10
)

var processEntryPhase atomic.Uint32
var processEntryStatePointer atomic.Pointer[processEntryState]

type processEntryFiles struct {
	mu      sync.Mutex
	stopped bool
	slots   [processEntryMaxFiles]*processEntryFile
}

// Reserve before an open. A late native return publishes into the same retained
// slot and starts its sole closer if failure cleanup already began.
func (files *processEntryFiles) reserve() *processEntryFile {
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.stopped {
		return nil
	}
	for i, slot := range files.slots {
		if slot == nil {
			slot = newProcessEntryFile(nil)
			files.slots[i] = slot
			return slot
		}
	}
	return nil
}

func (files *processEntryFiles) publish(slot *processEntryFile, file *os.File) bool {
	files.mu.Lock()
	slot.mu.Lock()
	slot.file = file
	slot.mu.Unlock()
	stopped := files.stopped
	if file == nil {
		for i, value := range files.slots {
			if value == slot {
				files.slots[i] = nil
				break
			}
		}
	}
	files.mu.Unlock()
	if stopped && file != nil {
		slot.beginClose()
	}
	return file != nil && !stopped
}

func (files *processEntryFiles) release(slot *processEntryFile, deadline time.Time) bool {
	if slot == nil || !slot.closeBefore(deadline) {
		return false
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	for i, value := range files.slots {
		if value == slot {
			files.slots[i] = nil
			return true
		}
	}
	return false
}

func (files *processEntryFiles) closeBefore(deadline time.Time) bool {
	files.mu.Lock()
	files.stopped = true
	slots := files.slots
	files.mu.Unlock()
	for _, slot := range slots {
		if slot != nil {
			slot.beginClose()
		}
	}
	ok := true
	for _, slot := range slots {
		if slot != nil && !slot.closeBefore(deadline) {
			ok = false
		}
	}
	return ok
}

type processEntryVerifier struct {
	arguments processEntryArguments
	binding   processmodel.Bootstrap
	files     *processEntryFiles
	deadline  time.Time
	done      chan struct{}
	ok        bool
}

func (v *processEntryVerifier) verify() { defer close(v.done); v.ok = processEntryVerifyNative(v) }

type processEntryState struct {
	cutoff                time.Time
	stdin, stdout         *processEntryFile
	stdioVerified         bool
	bootstrap             *processEntryBootstrap
	verifier              *processEntryVerifier
	files                 processEntryFiles
	observation           *processObservation
	output                *processOutput
	inputPipe, outputPipe *processPipe
	reporter              *processReporter
	signalMu              sync.Mutex
	signal                *processEntrySignal
	signalStarted         bool
	failed                atomic.Bool
}

func (s *processEntryState) fail() {
	s.failed.Store(true)
	if observation := processObserver.Load(); observation != nil {
		observation.invalidate()
	}
}

func (s *processEntryState) failedBegin(deadline time.Time) bool {
	s.fail()
	processEntryPhase.Store(processEntryTerminal)
	_ = s.files.closeBefore(deadline)
	if s.inputPipe != nil {
		_ = s.inputPipe.closeAndJoin(deadline)
	} else if s.stdioVerified && s.stdin != nil {
		_ = s.stdin.closeBefore(deadline)
	}
	if s.outputPipe != nil {
		_ = s.outputPipe.closeAndJoin(deadline)
	} else if s.stdioVerified && s.stdout != nil {
		_ = s.stdout.closeBefore(deadline)
	}
	return false
}

// BeginProcessEntry can only inspect this actual process and inherited stdio.
// No supplied binding, token, writer, owner or callback can install the singleton.
func BeginProcessEntry() bool {
	if !processEntryPhase.CompareAndSwap(0, processEntryEntering) {
		if s := processEntryStatePointer.Load(); s != nil {
			s.fail()
		}
		return false
	}
	entered := time.Now()
	s := &processEntryState{stdin: newProcessEntryFile(os.Stdin), stdout: newProcessEntryFile(os.Stdout)}
	processEntryStatePointer.Store(s)
	preCleanup := entered.Add(4 * time.Second)
	if !processEntrySupported() {
		s.fail()
		processEntryPhase.Store(processEntryTerminal)
		return false
	}
	build, ok := processEntryPinnedBuild()
	if !ok {
		return s.failedBegin(preCleanup)
	}
	arguments, ok := processEntryParseArguments(os.Args)
	if !ok || !processEntryEnvironment(os.Environ(), arguments.root) || !processEntryStandardFiles() {
		return s.failedBegin(preCleanup)
	}
	s.stdioVerified = true
	argvDigest, ok := processmodel.DigestArgv(os.Args)
	if !ok {
		return s.failedBegin(preCleanup)
	}
	s.bootstrap, ok = processEntryReadBootstrap(s.stdin, entered)
	if !ok {
		return s.failedBegin(preCleanup)
	}
	binding := s.bootstrap.value
	now := time.Now()
	absolute := time.Unix(int64(binding.Schedule.CleanupCutoffSeconds), 0)
	s.cutoff = now.Add(absolute.Sub(now))
	admissionCutoff := time.Unix(int64(binding.Schedule.WorkCutoffSeconds), 0)
	// Only the closed normal stop may be launched in the reserved cleanup phase.
	if arguments.mode == processmodel.CLI && arguments.operation == processmodel.CLILocalStop {
		admissionCutoff = absolute
	}
	if !now.Before(admissionCutoff) || now.Before(time.Unix(int64(binding.Schedule.EntrySeconds), int64(binding.Schedule.EntryNanoseconds))) || !now.Before(s.cutoff) || !processEntryArgumentsMatch(arguments, binding) || argvDigest != binding.ArgvSHA256 || !processEntryBuildMatches(build, binding) {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	// A successful read joins before the exact stdin object is transferred.
	s.verifier = &processEntryVerifier{arguments: arguments, binding: binding, files: &s.files, deadline: processBefore(s.cutoff, 2*time.Second), done: make(chan struct{})}
	go s.verifier.verify()
	if !processWait(s.verifier.done, s.verifier.deadline) || !s.verifier.ok {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	if digest, valid := processmodel.DigestArgv(os.Args); !valid || digest != argvDigest {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	if s.failed.Load() {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	s.observation, ok = newScopedProcessObservation(binding.Invocation.Mode, arguments.directory)
	if !ok {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	s.output, ok = newProcessOutput(s.observation)
	if !ok {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	s.inputPipe, ok = newProcessPipe(s.stdin.file, processPipeRead, binding.Invocation.Mode, s.cutoff)
	if !ok {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	s.outputPipe, ok = newProcessPipe(s.stdout.file, processPipeWrite, binding.Invocation.Mode, s.cutoff)
	if !ok {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	s.reporter, ok = newProcessReporter(binding, s.observation, s.inputPipe, s.outputPipe, s.output)
	if !ok || s.failed.Load() || !processObserver.CompareAndSwap(nil, s.observation) {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	if !s.reporter.start() {
		return s.failedBegin(processBefore(s.cutoff, 2*time.Second))
	}
	processEntryPhase.Store(processEntryReady)
	return true
}

type processEntryRejectingWriter struct{}

func (processEntryRejectingWriter) Write([]byte) (int, error) { return 0, errProcessOutput }

func ProcessEntryOutput() io.Writer {
	s := processEntryStatePointer.Load()
	if s == nil || processEntryPhase.Load() != processEntryReady || s.output == nil {
		if s != nil {
			s.fail()
		}
		return processEntryRejectingWriter{}
	}
	return s.output
}

func ProcessEntrySignalContext() context.Context {
	s := processEntryStatePointer.Load()
	if s != nil && processEntryPhase.Load() == processEntryReady {
		s.signalMu.Lock()
		if !s.signalStarted {
			s.signalStarted = true
			s.signal = newProcessEntrySignal()
			ctx := s.signal.ctx
			s.signalMu.Unlock()
			return ctx
		}
		s.signalMu.Unlock()
		s.fail()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// FinishProcessEntry accepts only the actual ordinary main return. Private
// product hooks, owned task completions and P's retained result decide success.
func FinishProcessEntry(productExitCode int) int {
	s := processEntryStatePointer.Load()
	if s == nil || !processEntryPhase.CompareAndSwap(processEntryReady, processEntryFinishing) {
		if s != nil {
			s.fail()
		}
		return processEntryFailureCode
	}
	if productExitCode != 0 && productExitCode != 1 {
		s.fail()
	}
	// Publish one immutable reporter-owned deadline before any entry joins.
	// finish consumes the same request only after those joins; it cannot renew it.
	deadline, prepared := s.reporter.prepareFinalization(productExitCode)
	if !prepared {
		s.fail()
	}
	s.signalMu.Lock()
	signalOwner := s.signal
	s.signalMu.Unlock()
	if signalOwner == nil || !signalOwner.stopBefore(deadline) {
		s.fail()
	}
	if !s.files.closeBefore(deadline) {
		s.fail()
	}
	if s.verifier == nil || !processWait(s.verifier.done, deadline) {
		s.fail()
	}
	result := s.reporter.finish(productExitCode)
	processEntryPhase.Store(processEntryTerminal)
	if s.failed.Load() || !time.Now().Before(deadline) || !result.success || !result.sealed || !result.inputJoined || !result.coordinatorJoined || !result.inputPipe.ioJoined || !result.inputPipe.closeJoined || !result.outputPipe.ioJoined || !result.outputPipe.closeJoined {
		return processEntryFailureCode
	}
	return productExitCode
}
