package routecat

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"tailscale.com/ipn/ipnstate"
	"testing"
	"time"

	"tailscale.com/derp/derphttp"
	"tailscale.com/wgengine/magicsock"
)

func relayEvent(epoch, seq uint64, err error) magicsock.DERPConnectionEvent {
	return magicsock.DERPConnectionEvent{RegionID: 1, Epoch: epoch, Sequence: seq, Err: err}
}
func TestRelayFailureAvailabilityIsPositiveAndAllCauses(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		allowed bool
	}{
		"refused":          {syscall.ECONNREFUSED, true},
		"network":          {&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}, true},
		"joined-available": {errors.Join(syscall.EHOSTUNREACH, syscall.ETIMEDOUT), true},
		"timeout":          {context.DeadlineExceeded, false}, "cancel": {context.Canceled, false},
		"permission": {syscall.EACCES, false}, "unknown": {errors.New("synthetic unknown"), false},
		"certificate":   {x509.UnknownAuthorityError{}, false},
		"mixed-denial":  {errors.Join(syscall.ECONNREFUSED, syscall.EPERM), false},
		"mixed-timeout": {errors.Join(syscall.ECONNREFUSED, context.DeadlineExceeded), false},
		"mixed-unknown": {errors.Join(syscall.ECONNREFUSED, errors.New("synthetic unknown")), false},
	} {
		t.Run(name, func(t *testing.T) {
			for _, phase := range []string{"dial", "tls", "protocol", "unknown"} {
				err := fmt.Errorf("attempt: %w", &derphttp.ConnectionError{Phase: phase, Err: tc.err})
				if got := relayDialAvailability(err, 0); got != (tc.allowed && phase == "dial") {
					t.Fatalf("phase %s: availability=%v", phase, got)
				}
				if !errors.Is(err, tc.err) {
					t.Fatal("cause was discarded")
				}
			}
		})
	}
}
func TestRelayFailureEpochSequenceAndTerminalLatch(t *testing.T) {
	s := &relayFailureState{region: 1}
	refused := &derphttp.ConnectionError{Phase: "dial", Err: syscall.ECONNREFUSED}
	pin := &derphttp.ConnectionError{Phase: "tls", Err: errors.New("synthetic pin rejection")}
	s.update(relayEvent(1, 0, nil))
	_, wake := s.snapshot()
	s.update(relayEvent(1, 1, refused))
	select {
	case <-wake:
	default:
		t.Fatal("failure did not wake waiter")
	}
	if got, _ := s.snapshot(); got != refused {
		t.Fatal("dial cause was lost")
	}
	s.update(relayEvent(1, 2, pin))
	s.update(relayEvent(1, 3, refused))
	if got, _ := s.snapshot(); !errors.Is(got, pin) || !errors.Is(got, refused) {
		t.Fatal("retry hid terminal failure")
	}
	s.update(relayEvent(1, 2, nil))
	if got, _ := s.snapshot(); got == nil {
		t.Fatal("stale success cleared terminal failure")
	}
	s.update(relayEvent(1, 4, nil))
	if got, _ := s.snapshot(); got != nil {
		t.Fatal("verified admission did not clear failure")
	}
	s.update(magicsock.DERPConnectionEvent{RegionID: 1, Epoch: 1, Retired: true})
	s.update(relayEvent(1, 99, pin))
	if got, _ := s.snapshot(); got != nil {
		t.Fatal("retired client contaminated state")
	}
	s.update(relayEvent(2, 0, nil))
	s.update(relayEvent(1, 100, pin))
	wrong := relayEvent(3, 1, pin)
	wrong.RegionID = 2
	s.update(wrong)
	if got, _ := s.snapshot(); got != nil {
		t.Fatal("old epoch or wrong region contaminated replacement")
	}
	s.update(relayEvent(2, 1, refused))
	if got, _ := s.snapshot(); got != refused {
		t.Fatal("current generation failure missing")
	}
	s.close()
	s.update(relayEvent(3, 1, nil))
	if got, _ := s.snapshot(); !errors.Is(got, net.ErrClosed) {
		t.Fatal("late callback reopened closed state")
	}
}
func TestRelayFailureStaleGenerationRace(t *testing.T) {
	s := &relayFailureState{region: 1}
	s.update(relayEvent(20, 0, nil))
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for n := uint64(1); n <= 100; n++ {
				s.update(relayEvent(19, n, &derphttp.ConnectionError{Phase: "tls", Err: syscall.ECONNRESET}))
				s.snapshot()
			}
		})
	}
	for n := uint64(1); n <= 100; n++ {
		s.update(relayEvent(20, n, nil))
	}
	wg.Wait()
	if got, _ := s.snapshot(); got != nil {
		t.Fatal("stale race poisoned current engine")
	}
}

func TestRelayFailureWaitPreservesDirectProofAndCancellation(t *testing.T) {
	cause := &derphttp.ConnectionError{Phase: "dial", Err: syscall.ECONNREFUSED}
	s := &relayFailureState{region: 1}
	s.update(relayEvent(1, 1, cause))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch := make(chan *ipnstate.PingResult, 1)
	proof := &ipnstate.PingResult{Endpoint: "[::1]:42006"}
	done := make(chan error, 1)
	go func() {
		got, err := waitDiscoPing(ctx, ch, s)
		if err == nil && got != proof {
			err = errors.New("wrong direct proof")
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("background failure preempted direct proof: %v", err)
	default:
	}
	ch <- proof
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for range 100 {
		ch <- proof
		if got, err := waitDiscoPing(expired, ch, s); err != nil || got != proof {
			t.Fatal("ready proof lost to deadline")
		}
	}
	ch <- &ipnstate.PingResult{Err: "synthetic permission failure"}
	if _, err := waitDiscoPing(expired, ch, s); err == nil || errors.Is(err, cause) {
		t.Fatal("explicit proof denial became availability")
	}
	if _, err := waitDiscoPing(expired, ch, s); !errors.Is(err, cause) {
		t.Fatal("actual relay failure not retained")
	}
	if _, err := waitDiscoPing(expired, ch, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("ambiguous timeout reclassified")
	}
	canceled, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	if _, err := waitDiscoPing(canceled, ch, s); !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatal("cancellation or actual cause lost")
	}
}

func TestRelayFailureTerminalSurvivesAutomaticRebind(t *testing.T) {
	s := &relayFailureState{region: 1}
	pin := &derphttp.ConnectionError{Phase: "tls", Err: errors.New("synthetic pin rejection")}
	refused := &derphttp.ConnectionError{Phase: "dial", Err: syscall.ECONNREFUSED}
	s.update(relayEvent(1, 1, pin))
	s.update(magicsock.DERPConnectionEvent{RegionID: 1, Epoch: 1, Retired: true})
	if got, _ := s.snapshot(); !errors.Is(got, pin) {
		t.Fatal("retirement erased observed terminal failure")
	}
	s.update(relayEvent(2, 0, nil))
	s.update(relayEvent(2, 1, refused))
	s.update(relayEvent(1, 99, nil))
	if got, _ := s.snapshot(); !errors.Is(got, pin) {
		t.Fatal("new attempt or stale admission hid terminal failure")
	}
	s.update(relayEvent(2, 2, nil))
	if got, _ := s.snapshot(); got != nil {
		t.Fatal("actual current admission failed to clear terminal failure")
	}
}

func TestRelayFailureLatePublicationRetainsTimeoutDiagnostics(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		s := &relayFailureState{region: 1}
		s.update(relayEvent(1, 0, nil))
		before, _ := s.snapshot()
		if before != nil {
			t.Fatal("unexpected initial failure")
		}
		permission := &derphttp.ConnectionError{Phase: "protocol", Err: syscall.EACCES}
		s.update(relayEvent(1, 1, permission))
		sendErr := errors.New("synthetic queued send failure")
		err := relayWaitFailure(contextErr, sendErr, s)
		if !errors.Is(err, contextErr) || !errors.Is(err, permission) || !errors.Is(err, sendErr) {
			t.Fatal("simultaneous completion lost cause")
		}
		if relayDialAvailability(err, 0) {
			t.Fatal("deadline/cancel became availability")
		}
	}
}
