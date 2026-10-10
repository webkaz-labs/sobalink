//go:build resource_process_native && linux && (amd64 || arm64)

package resourceacceptance

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

func TestProcessEntryBootstrapSingleReadNativePipe(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal("pipe creation failed")
	}
	r, w := newProcessEntryFile(read), newProcessEntryFile(write)
	var writerDone chan struct{}
	writerOK := false
	var task *processEntryBootstrap
	var tailDone chan struct{}
	t.Cleanup(func() {
		r.beginClose()
		w.beginClose()
		readClosed := r.closeBefore(cutoff)
		writeClosed := w.closeBefore(cutoff)
		if !readClosed || !writeClosed {
			t.Error("bootstrap fixture cleanup unjoined")
		}
		if writerDone != nil && !processWait(writerDone, cutoff) {
			t.Error("bootstrap writer unjoined")
		}
		if task != nil && !processWait(task.done, cutoff) {
			t.Error("bootstrap reader unjoined")
		}
		if tailDone != nil && !processWait(tailDone, cutoff) {
			t.Error("tail reader unjoined")
		}
	})
	binding := processTestBinding()
	encoded, ok := processmodel.EncodeBootstrap(binding)
	if !ok {
		t.Fatal("synthetic shape rejected")
	}
	// One bounded write includes a next-message sentinel. Bootstrap may not
	// buffer/read ahead into the passive stream owned by the later reporter.
	var bytes [processmodel.MaxBootstrapSize + 1]byte
	copy(bytes[:], encoded.Bytes[:encoded.Length])
	bytes[encoded.Length] = 77
	writerDone = make(chan struct{})
	go func() {
		defer close(writerDone)
		n, e := write.Write(bytes[:int(encoded.Length)+1])
		writerOK = e == nil && n == int(encoded.Length)+1
	}()
	task, ok = processEntryReadBootstrap(r, time.Now())
	if !ok || task.value != binding || !processWait(writerDone, cutoff) || !writerOK {
		t.Fatal("single bootstrap failed")
	}
	tailDone = make(chan struct{})
	tailOK := false
	go func() {
		defer close(tailDone)
		var tail [1]byte
		_, e := io.ReadFull(read, tail[:])
		tailOK = e == nil && tail[0] == 77
	}()
	if !processWait(tailDone, cutoff) || !tailOK {
		t.Fatal("bootstrap consumed passive input")
	}
	if processObserver.Load() != nil {
		t.Fatal("bootstrap alone installed observer")
	}
}

func TestProcessEntryBootstrapPartialEOFNativePipe(t *testing.T) {
	cutoff := time.Now().Add(5 * time.Second)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal("pipe creation failed")
	}
	r, w := newProcessEntryFile(read), newProcessEntryFile(write)
	done := make(chan struct{})
	writeOK := false
	var task *processEntryBootstrap
	t.Cleanup(func() {
		r.beginClose()
		w.beginClose()
		readClosed := r.closeBefore(cutoff)
		writeClosed := w.closeBefore(cutoff)
		if !readClosed || !writeClosed {
			t.Error("partial input cleanup unjoined")
		}
		if !processWait(done, cutoff) {
			t.Error("partial input writer unjoined")
		}
		if task != nil && !processWait(task.done, cutoff) {
			t.Error("partial input reader unjoined")
		}
	})
	go func() {
		defer close(done)
		n, e := write.Write([]byte("P1I"))
		writeOK = e == nil && n == 3
		w.beginClose()
	}()
	var ok bool
	task, ok = processEntryReadBootstrap(r, time.Now())
	if ok || !processWait(task.done, cutoff) || !processWait(done, cutoff) || !writeOK {
		t.Fatal("partial bootstrap accepted or unjoined")
	}
}
