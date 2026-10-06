package routecat

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"tailscale.com/ipn/ipnstate"

	"tailscale.com/tailcfg"
	"tailscale.com/wgengine/magicsock"
)

// relayFailureState belongs to one engine, never to a peer or replacement client.
// It retains the first terminal failure until actual DERP admission succeeds.
// Repeated availability failures replace each other, bounding retained memory.
type relayFailureState struct {
	mu              sync.Mutex
	region          tailcfg.DERPRegionID
	epoch, sequence uint64
	retired, closed bool
	err             error
	terminal        bool
	changed         chan struct{}
}

func (s *relayFailureState) notifyLocked() {
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}

func (s *relayFailureState) update(e magicsock.DERPConnectionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || e.RegionID != s.region || e.Epoch < s.epoch || e.Epoch == 0 {
		return
	}
	if e.Epoch > s.epoch {
		s.epoch, s.sequence, s.retired = e.Epoch, 0, false
		if !s.terminal {
			s.err = nil
		}
	} else if s.retired || !e.Retired && e.Sequence <= s.sequence {
		return
	}
	if e.Retired {
		s.retired = true
		if !s.terminal {
			s.err = nil
		}
	} else {
		s.sequence = e.Sequence
		if e.Err == nil && e.Sequence > 0 {
			// Only ServerInfo proves admission; a replacement epoch does not.
			s.err, s.terminal = nil, false
		} else if e.Err != nil && !s.terminal {
			terminal := !relayDialAvailability(e.Err, 0)
			if terminal {
				s.err = errors.Join(s.err, e.Err)
			} else {
				s.err = e.Err
			}
			s.terminal = terminal
		}
	}
	s.notifyLocked()
}

func (s *relayFailureState) snapshot() (error, <-chan struct{}) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.err, s.changed
}

func (s *relayFailureState) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.err, s.terminal = true, net.ErrClosed, true
	s.notifyLocked()
}

// This predicate only controls bounded failure retention, never routing. The
// coordinator independently decides whether an actual error permits failover.
func relayDialAvailability(err error, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if staged, ok := err.(interface{ DERPFailurePhase() string }); ok && staged.DERPFailurePhase() != "dial" {
		return false
	}
	if _, ok := err.(interface{ ErrorCode() string }); ok {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !relayDialAvailability(cause, depth+1) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return relayDialAvailability(wrapped.Unwrap(), depth+1)
	}
	cause, ok := err.(syscall.Errno)
	if !ok {
		return false
	}
	switch cause {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ETIMEDOUT:
		return true
	}
	return relayPlatformAvailability(cause)
}

// waitDiscoPing never short-circuits a direct proof for a background relay error.
func waitDiscoPing(ctx context.Context, ch <-chan *ipnstate.PingResult, failures *relayFailureState) (*ipnstate.PingResult, error) {
	result := func(r *ipnstate.PingResult) (*ipnstate.PingResult, error) {
		if r == nil {
			return nil, errors.New("empty discovery proof")
		}
		if r.Err != "" {
			return nil, errors.New(r.Err)
		}
		return r, nil
	}
	select {
	case r := <-ch:
		return result(r)
	case <-ctx.Done():
		// Preserve a concurrently ready success or explicit proof failure.
		select {
		case r := <-ch:
			return result(r)
		default:
		}
		if failure, _ := failures.snapshot(); failure != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, errors.Join(ctx.Err(), failure)
			}
			return nil, failure
		}
		// An unexplained timeout remains terminal; it is not availability evidence.
		return nil, ctx.Err()
	}
}

// Re-snapshot after the wait ends: a real failure and context completion may
// both become ready after the caller's initial state read. Keep all causes.
func relayWaitFailure(contextErr, sendErr error, failures *relayFailureState) error {
	failure, _ := failures.snapshot()
	return errors.Join(contextErr, sendErr, failure)
}
