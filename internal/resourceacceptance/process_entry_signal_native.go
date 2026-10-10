//go:build resource_process_native

package resourceacceptance

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type processEntrySignalCause string

func (e processEntrySignalCause) Error() string        { return string(e) }
func (e processEntrySignalCause) Is(target error) bool { return target == context.Canceled }

type processEntrySignal struct {
	ctx                    context.Context
	cancel                 context.CancelCauseFunc
	channel                chan os.Signal
	producerDone, stopDone chan struct{}
	stopOnce               sync.Once
}

func newProcessEntrySignal() *processEntrySignal {
	ctx, cancel := context.WithCancelCause(context.Background())
	s := &processEntrySignal{ctx: ctx, cancel: cancel, channel: make(chan os.Signal, 1), producerDone: make(chan struct{}), stopDone: make(chan struct{})}
	signal.Notify(s.channel, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(s.producerDone)
		select {
		case received := <-s.channel:
			s.cancel(processEntrySignalCause(received.String() + " signal received"))
		case <-s.ctx.Done():
		}
	}()
	return s
}

func (s *processEntrySignal) stopBefore(deadline time.Time) bool {
	if s == nil {
		return false
	}
	s.cancel(nil)
	s.stopOnce.Do(func() { go func() { signal.Stop(s.channel); close(s.stopDone) }() })
	producer := processWait(s.producerDone, deadline)
	stopped := processWait(s.stopDone, deadline)
	return producer && stopped
}
