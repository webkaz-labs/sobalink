//go:build resource_process_native

package resourceacceptance

import (
	"io"
	"os"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

type processPipeDirection uint8

const (
	processPipeRead processPipeDirection = iota + 1
	processPipeWrite
	processPipeTaskLimit = 2048
)

type processPipeOutcome uint8

const (
	processPipeOK processPipeOutcome = iota + 1
	processPipeEOF
	processPipeInvalid
	processPipeIOFailure
	processPipeTimeout
	processPipeUnjoined
)

type processPipeBlock struct {
	bytes  [processmodel.MaxFrameSize]byte
	length uint16
}

type processPipeTask struct {
	block   processPipeBlock
	outcome processPipeOutcome
	done    chan struct{}
}

type processPipeCloser struct {
	outcome processPipeOutcome
	done    chan struct{}
}

type processPipeJoin struct {
	ioJoined, closeJoined bool
	outcome               processPipeOutcome
}

// Concrete endpoint ownership is transferred by a later reviewed entry. The
// kind check is not provenance, authentication or a native cancellation proof.
// An unjoined task remains retained; its buffer never escapes to a caller.
type processPipe struct {
	file                *os.File
	direction           processPipeDirection
	mode                processmodel.Mode
	cutoff              time.Time
	mu                  sync.Mutex
	active              *processPipeTask
	closer              *processPipeCloser
	tasks               uint16
	closed, eof, failed bool
}

func newProcessPipe(file *os.File, direction processPipeDirection, mode processmodel.Mode, cutoff time.Time) (*processPipe, bool) {
	if file == nil || (direction != processPipeRead && direction != processPipeWrite) ||
		(mode != processmodel.Owner && mode != processmodel.CLI) || cutoff.IsZero() || !time.Now().Before(cutoff) {
		return nil, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeType != os.ModeNamedPipe {
		return nil, false
	}
	return &processPipe{file: file, direction: direction, mode: mode, cutoff: cutoff}, true
}

func processBefore(cutoff time.Time, margin time.Duration) time.Time {
	deadline := time.Now().Add(margin)
	if cutoff.Before(deadline) {
		return cutoff
	}
	return deadline
}

func processWait(done <-chan struct{}, deadline time.Time) bool {
	// A completion first observed after the deadline is conservatively failed;
	// a closed channel carries no trustworthy earlier completion timestamp.
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return time.Now().Before(deadline)
	case <-timer.C:
		return false
	}
}

func (p *processPipe) readExact(length uint16, deadline time.Time) (processPipeBlock, processPipeOutcome) {
	return p.perform(processPipeRead, processPipeBlock{length: length}, deadline)
}

func (p *processPipe) writeExact(block processPipeBlock, deadline time.Time) processPipeOutcome {
	_, outcome := p.perform(processPipeWrite, block, deadline)
	return outcome
}

func (p *processPipe) perform(direction processPipeDirection, block processPipeBlock, deadline time.Time) (processPipeBlock, processPipeOutcome) {
	if p == nil {
		return processPipeBlock{}, processPipeInvalid
	}
	p.mu.Lock()
	if p.direction != direction || block.length == 0 || int(block.length) > len(block.bytes) ||
		deadline.IsZero() || deadline.After(p.cutoff) || !time.Now().Before(deadline) ||
		p.failed || p.closed || p.eof || p.active != nil || p.tasks >= processPipeTaskLimit {
		p.failed = true
		p.mu.Unlock()
		return processPipeBlock{}, processPipeInvalid
	}
	task := &processPipeTask{block: block, done: make(chan struct{})}
	p.active = task
	p.tasks++
	p.mu.Unlock()
	// Registration precedes launch; no mutex covers an OS call or a join.
	go p.performTask(task)
	if !processWait(task.done, deadline) {
		p.markFailed()
		join := p.closeAndJoin(processBefore(p.cutoff, 2*time.Second))
		if !join.ioJoined || !join.closeJoined {
			return processPipeBlock{}, processPipeUnjoined
		}
		return processPipeBlock{}, processPipeTimeout
	}
	outcome := task.outcome
	p.mu.Lock()
	if p.active == task {
		p.active = nil
	}
	if outcome == processPipeEOF {
		p.eof = true
	}
	if outcome != processPipeOK && outcome != processPipeEOF {
		p.failed = true
	}
	failed := p.failed
	p.mu.Unlock()
	if failed {
		join := p.closeAndJoin(processBefore(p.cutoff, 2*time.Second))
		if !join.ioJoined || !join.closeJoined {
			return processPipeBlock{}, processPipeUnjoined
		}
		return processPipeBlock{}, processPipeIOFailure
	}
	if outcome == processPipeEOF {
		return processPipeBlock{}, outcome
	}
	return task.block, outcome
}

func (p *processPipe) performTask(task *processPipeTask) {
	defer close(task.done)
	if p.direction == processPipeRead {
		n, err := io.ReadFull(p.file, task.block.bytes[:task.block.length])
		switch {
		case err == nil && n == int(task.block.length):
			task.outcome = processPipeOK
		case err == io.EOF && n == 0:
			task.outcome = processPipeEOF
		default:
			task.outcome = processPipeIOFailure
		}
		return
	}
	n, err := p.file.Write(task.block.bytes[:task.block.length])
	if err != nil || n != int(task.block.length) {
		task.outcome = processPipeIOFailure
		return
	}
	task.outcome = processPipeOK
}

func (p *processPipe) markFailed() {
	p.mu.Lock()
	p.failed = true
	p.mu.Unlock()
}

func (p *processPipe) closeTask(closer *processPipeCloser) {
	defer close(closer.done)
	if err := p.file.Close(); err != nil {
		closer.outcome = processPipeIOFailure
		return
	}
	closer.outcome = processPipeOK
}

func (p *processPipe) closeAndJoin(deadline time.Time) processPipeJoin {
	if p == nil {
		return processPipeJoin{outcome: processPipeInvalid}
	}
	if deadline.After(p.cutoff) {
		deadline = p.cutoff
	}
	p.mu.Lock()
	p.closed = true
	start := p.closer == nil
	if start {
		p.closer = &processPipeCloser{done: make(chan struct{})}
	}
	closer, task := p.closer, p.active
	p.mu.Unlock()
	// Every concurrent path sees this same retained closer before it starts.
	if start {
		go p.closeTask(closer)
	}
	result := processPipeJoin{ioJoined: task == nil, outcome: processPipeUnjoined}
	if task != nil {
		result.ioJoined = processWait(task.done, deadline)
	}
	result.closeJoined = processWait(closer.done, deadline)
	p.mu.Lock()
	if result.ioJoined && task != nil && p.active == task {
		p.active = nil
	}
	if !result.ioJoined || !result.closeJoined {
		p.failed = true
	} else if closer.outcome != processPipeOK || task != nil && task.outcome != processPipeOK && task.outcome != processPipeEOF {
		p.failed = true
		result.outcome = processPipeIOFailure
	} else if p.failed {
		result.outcome = processPipeIOFailure
	} else {
		result.outcome = processPipeOK
	}
	p.mu.Unlock()
	return result
}
