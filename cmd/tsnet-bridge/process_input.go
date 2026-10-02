package main

import (
	"context"
	"io"
	"sync"
)

// processInput belongs to main, not to an individual prompt. Some console reads
// cannot be canceled portably. A single lazy worker lets the command return and
// restore its terminal on Ctrl+C; at most one blocked OS read remains until the
// CLI process exits. Reads are requested, never prefetched, so child processes
// can use the original stdin after the prompts finish.
type processInput struct {
	ctx      context.Context
	in       io.Reader
	once     sync.Once
	requests chan processRead
	done     chan struct{}
}

type processRead struct {
	size   int
	result chan processReadResult
}

type processReadResult struct {
	data []byte
	err  error
}

func newProcessInput(ctx context.Context, in io.Reader) *processInput {
	return &processInput{ctx: ctx, in: in, requests: make(chan processRead), done: make(chan struct{})}
}

func (in *processInput) Context() context.Context { return in.ctx }
func (in *processInput) UnwrapReader() io.Reader  { return in.in }

func (in *processInput) Read(p []byte) (int, error) {
	if err := in.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	in.once.Do(func() { go in.serve() })
	request := processRead{size: len(p), result: make(chan processReadResult, 1)}
	select {
	case <-in.ctx.Done():
		return 0, in.ctx.Err()
	case in.requests <- request:
	}
	select {
	case <-in.ctx.Done():
		return 0, in.ctx.Err()
	case result := <-request.result:
		// Cancellation wins even if input (including a confirmation) arrived at
		// the same time. The worker owns its buffer, so a late read cannot write
		// into a scanner buffer after Read has returned.
		if err := in.ctx.Err(); err != nil {
			return 0, err
		}
		return copy(p, result.data), result.err
	}
}

func (in *processInput) serve() {
	defer close(in.done)
	for {
		select {
		case <-in.ctx.Done():
			return
		case request := <-in.requests:
			if in.ctx.Err() != nil {
				return
			}
			buffer := make([]byte, request.size)
			n, err := in.in.Read(buffer)
			request.result <- processReadResult{data: buffer[:n], err: err}
		}
	}
}

func unwrapProcessInput(in io.Reader) io.Reader {
	if input, ok := in.(*processInput); ok {
		return input.in
	}
	return in
}
