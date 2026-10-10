//go:build resource_process_native

package resourceacceptance

import (
	"io"
	"os"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

type processEntryFile struct {
	mu      sync.Mutex
	file    *os.File
	closing bool
	done    chan struct{}
	ok      bool
}

func newProcessEntryFile(file *os.File) *processEntryFile {
	return &processEntryFile{file: file, done: make(chan struct{})}
}

func (f *processEntryFile) beginClose() {
	f.mu.Lock()
	start := !f.closing && f.file != nil
	if start {
		f.closing = true
	}
	file := f.file
	f.mu.Unlock()
	if start {
		go func() { f.ok = file.Close() == nil; close(f.done) }()
	}
}

func (f *processEntryFile) closeBefore(deadline time.Time) bool {
	f.beginClose()
	return processWait(f.done, deadline) && f.ok
}

type processEntryBootstrap struct {
	input *processEntryFile
	done  chan struct{}
	value processmodel.Bootstrap
	ok    bool
}

func (task *processEntryBootstrap) read() {
	defer close(task.done)
	var buffer [processmodel.MaxBootstrapSize]byte
	if _, err := io.ReadFull(task.input.file, buffer[:processmodel.InputHeaderSize]); err != nil {
		return
	}
	length, ok := processmodel.BootstrapMessageLength(buffer[:processmodel.InputHeaderSize])
	if !ok {
		return
	}
	if _, err := io.ReadFull(task.input.file, buffer[processmodel.InputHeaderSize:length]); err != nil {
		return
	}
	task.value, task.ok = processmodel.DecodeBootstrap(buffer[:length])
}

// This is the sole bootstrap consumption. The result is used only after join;
// the same exact stdin object is then transferred once to the passive P reader.
func processEntryReadBootstrap(input *processEntryFile, entered time.Time) (*processEntryBootstrap, bool) {
	task := &processEntryBootstrap{input: input, done: make(chan struct{})}
	go task.read()
	deadline := entered.Add(2 * time.Second)
	if !processWait(task.done, deadline) {
		cleanup := deadline.Add(2 * time.Second)
		input.beginClose()
		_ = processWait(task.done, cleanup)
		_ = input.closeBefore(cleanup)
		return task, false
	}
	return task, task.ok
}
