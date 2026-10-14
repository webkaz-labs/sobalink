package nearby

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

var errSyntheticClose = errors.New("synthetic close failure")

type failingCloseTransport struct {
	*fakeTransport
	calls   atomic.Int32
	unblock bool
}

func (t *failingCloseTransport) Close() error {
	n := t.calls.Add(1)
	if t.unblock {
		_ = t.fakeTransport.Close()
	}
	if n == 1 {
		return errSyntheticClose
	}
	return nil
}

func startCloseTest(t *testing.T, tr Transport) (*Session, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), a, clock}, func(Scope) (Transport, error) { return tr, nil })
	if err != nil {
		t.Fatal(err)
	}
	return s, clock
}
func TestSessionRetainsFirstCloseFailure(t *testing.T) {
	tr := &failingCloseTransport{fakeTransport: newFakeTransport(), unblock: true}
	s, clock := startCloseTest(t, tr)
	t.Cleanup(func() { _ = tr.fakeTransport.Close(); _ = s.Close() })
	eventually(t, func() bool { return tr.count() == 2 })
	for i := 0; i < 3; i++ {
		err := s.Close()
		if !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, errSyntheticClose) {
			t.Fatalf("close failure lost: %v", err)
		}
	}
	if tr.calls.Load() != 1 {
		t.Fatal("Close retries launched new transport-close operations")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("joined close did not finish")
	}
	if !errors.Is(s.Err(), ErrCloseUncertain) || clock.active() != 0 || len(s.Candidates()) != 0 {
		t.Fatal("closure uncertainty/state was lost")
	}
	if IsUnavailable(errors.Join(ErrSocketUnavailable, s.Err())) {
		t.Fatal("uncertain close treated as ordinary unavailable")
	}
}
func TestSessionUnresolvedCloseIsFiniteAndRetainsOwner(t *testing.T) {
	tr := &failingCloseTransport{fakeTransport: newFakeTransport()}
	s, clock := startCloseTest(t, tr)
	t.Cleanup(func() { _ = tr.fakeTransport.Close(); _ = s.Close() })
	eventually(t, func() bool { return tr.count() == 2 })
	started := time.Now()
	err := s.Close()
	if time.Since(started) > closeJoinTimeout+time.Second {
		t.Fatal("Close exceeded finite owner budget")
	}
	if !errors.Is(err, ErrCloseUnresolved) || !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, errSyntheticClose) {
		t.Fatalf("unresolved close not explicit: %v", err)
	}
	select {
	case <-s.Done():
		t.Fatal("unjoined owner reported done")
	default:
	}
	if !errors.Is(s.Err(), ErrCloseUnresolved) || len(s.Candidates()) != 0 || clock.active() != 0 {
		t.Fatal("unresolved owner state not retained")
	}
	// Test-only release simulates the blocked reader finally terminating. The
	// original explicit close failure must remain even though joining now works.
	_ = tr.fakeTransport.Close()
	if err := s.Close(); !errors.Is(err, errSyntheticClose) {
		t.Fatalf("later join erased first close failure: %v", err)
	}
	if tr.calls.Load() != 1 {
		t.Fatal("retry spawned another close operation")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("released reader did not join")
	}
}
func TestConstructorRollbackRetainsUncertainOwner(t *testing.T) {
	var attempts atomic.Int32
	owner := &closeOnlyTransport{close: func() error { attempts.Add(1); return errSyntheticClose }}
	returned, err := rollbackTransport(owner, ErrSocketUnavailable)
	if returned != owner || !errors.Is(err, ErrCloseUncertain) || IsUnavailable(err) {
		t.Fatal("rollback uncertainty lost owner or admitted fallback")
	}
	clock := newFakeClock()
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, startErr := Start(context.Background(), Config{testScope(), a, clock}, func(Scope) (Transport, error) { return returned, err })
	if s == nil || !errors.Is(startErr, ErrCloseUncertain) {
		t.Fatal("Start dropped failed rollback owner")
	}
	if len(s.Candidates()) != 0 {
		t.Fatal("failed startup admitted hints")
	}
	for i := 0; i < 3; i++ {
		if closeErr := s.Close(); !errors.Is(closeErr, errSyntheticClose) {
			t.Fatalf("startup close result not retained: %v", closeErr)
		}
	}
	if attempts.Load() != 1 {
		t.Fatal("rollback repeated underlying close")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("close-only owner goroutine not joined")
	}
}
func TestStartupCleanupFailureReturnsSessionAndError(t *testing.T) {
	tr := &failingCloseTransport{fakeTransport: newFakeTransport()}
	t.Cleanup(func() { _ = tr.fakeTransport.Close() })
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), a, newFakeClock()}, func(Scope) (Transport, error) { return tr, ErrSocketUnavailable })
	if s == nil || !errors.Is(err, ErrCloseUncertain) || tr.count() != 0 {
		t.Fatal("failed startup advertised or dropped owner")
	}
	if closeErr := s.Close(); !errors.Is(closeErr, errSyntheticClose) || tr.calls.Load() != 1 {
		t.Fatal("startup cleanup failure was not retained")
	}
}
func TestOnlyAlreadyClosedErrorIsNormalized(t *testing.T) {
	if normalizedCloseError(net.ErrClosed) != nil || normalizedCloseError(fmt.Errorf("close: %w", net.ErrClosed)) != nil {
		t.Fatal("already closed was not normalized")
	}
	err := normalizedCloseError(errors.Join(net.ErrClosed, errSyntheticClose))
	if !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, errSyntheticClose) {
		t.Fatal("joined real failure was incorrectly normalized")
	}
	owner := &closeOnlyTransport{close: func() error { return net.ErrClosed }}
	tr, err := rollbackTransport(owner, ErrSocketUnavailable)
	if tr != nil || !errors.Is(err, ErrSocketUnavailable) || errors.Is(err, ErrCloseUncertain) {
		t.Fatal("confirmed rollback did not release owner")
	}
}
func TestAcceptanceObserverRetainsCloseError(t *testing.T) {
	tr := &failingCloseTransport{fakeTransport: newFakeTransport(), unblock: true}
	audit := &observedNativeTransport{Transport: tr, scope: testScope()}
	for i := 0; i < 3; i++ {
		if err := audit.Close(); !errors.Is(err, ErrCloseUncertain) {
			t.Fatal("acceptance observer hid close error")
		}
	}
	_, _, _, closed := audit.snapshot()
	if closed || tr.calls.Load() != 1 {
		t.Fatal("uncertain observer falsely confirmed closure")
	}
}

// Hold Close after the budget error is published, making the Err-before-Done
// ordering deterministic rather than depending on goroutine scheduling.
type heldBudgetCloseTransport struct {
	*fakeTransport
	started  chan struct{}
	release  chan struct{}
	closeErr error
	calls    atomic.Int32
}

func (tr *heldBudgetCloseTransport) Close() error {
	tr.calls.Add(1)
	close(tr.started)
	<-tr.release
	_ = tr.fakeTransport.Close()
	return tr.closeErr
}
func TestSendBudgetErrorPrecedesOwnerJoin(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		name := "joined"
		if uncertain {
			name = "retained_uncertain_owner"
		}
		t.Run(name, func(t *testing.T) {
			tr := &heldBudgetCloseTransport{fakeTransport: newFakeTransport(), started: make(chan struct{}), release: make(chan struct{})}
			if uncertain {
				tr.closeErr = errSyntheticClose
			}
			s, clock := startCloseTest(t, tr)
			released := false
			t.Cleanup(func() {
				if !released {
					close(tr.release)
				}
				_ = tr.fakeTransport.Close()
				_ = s.Close()
			})
			eventually(t, func() bool { return tr.count() == 2 })
			driveQueryFloodToTerminal(t, s, clock, tr.fakeTransport)
			eventually(t, func() bool {
				select {
				case <-tr.started:
					return true
				default:
					return false
				}
			})
			select {
			case <-s.Done():
				t.Fatal("terminal error falsely implied owner join")
			default:
			}
			if len(s.Candidates()) != 0 || tr.calls.Load() != 1 {
				t.Fatal("budget termination retained hints or repeated close")
			}
			close(tr.release)
			released = true
			eventually(t, func() bool {
				select {
				case <-s.Done():
					return true
				default:
					return false
				}
			})
			err := s.Close()
			if !errors.Is(s.Err(), ErrSendBudget) || tr.count() != MaxSentDatagrams || tr.calls.Load() != 1 {
				t.Fatal("joined budget result changed")
			}
			if uncertain {
				if !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, errSyntheticClose) || IsUnavailable(s.Err()) {
					t.Fatal("joined owner lost cleanup uncertainty")
				}
			} else if err != nil {
				t.Fatal("successful close was not confirmed")
			}
		})
	}
}

// A split transport can unblock reads while a nested physical Close remains
// active. Session.Done must join that worker, not just the outer Close wrapper.
type heldNestedCloseTransport struct {
	*fakeTransport
	closeErr   error
	closeOwner Transport
	entered    chan struct{}
	release    chan struct{}
	calls      atomic.Int32
}

func newHeldNestedCloseTransport() *heldNestedCloseTransport {
	tr := &heldNestedCloseTransport{fakeTransport: newFakeTransport(), entered: make(chan struct{}), release: make(chan struct{})}
	first := &closeOnlyTransport{close: func() error {
		tr.calls.Add(1)
		_ = tr.fakeTransport.Close()
		close(tr.entered)
		<-tr.release
		return tr.closeErr
	}}
	tr.closeOwner = pairedCloseOwner(first, &closeOnlyTransport{close: func() error { return nil }})
	return tr
}
func (tr *heldNestedCloseTransport) Close() error { return tr.closeOwner.Close() }

func TestSessionDoneWaitsForNestedPhysicalClose(t *testing.T) {
	tr := newHeldNestedCloseTransport()
	s, _ := startCloseTest(t, tr)
	released := false
	t.Cleanup(func() {
		if !released {
			close(tr.release)
		}
		_ = s.Close()
	})
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	<-tr.entered
	err := <-done
	if !errors.Is(err, ErrCloseUnresolved) {
		t.Fatal("held nested close was not bounded at Session")
	}
	select {
	case <-s.Done():
		t.Fatal("Session.Done preceded nested physical close")
	default:
	}
	if tr.calls.Load() != 1 {
		t.Fatal("nested close repeated")
	}
	close(tr.release)
	released = true
	eventually(t, func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	})
	if err := s.Close(); err != nil || tr.calls.Load() != 1 {
		t.Fatal("joined close did not reuse original worker")
	}
}

func TestConstructorRollbackRetainsNestedPhysicalJoin(t *testing.T) {
	tr := newHeldNestedCloseTransport()
	tr.closeErr = errSyntheticClose
	type outcome struct {
		owner Transport
		err   error
	}
	done := make(chan outcome, 1)
	go func() { owner, err := rollbackTransport(tr, ErrSocketUnavailable); done <- outcome{owner, err} }()
	<-tr.entered
	released := false
	t.Cleanup(func() {
		if !released {
			close(tr.release)
		}
	})
	var result outcome
	select {
	case result = <-done:
	case <-time.After(closeJoinTimeout + time.Second):
		t.Fatal("constructor rollback was not bounded")
	}
	if result.owner == nil || !errors.Is(result.err, ErrCloseUncertain) || !errors.Is(result.err, ErrCloseUnresolved) || IsUnavailable(result.err) {
		t.Fatal("rollback lost unresolved owner")
	}
	joined := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { joined <- result.owner.Close() }()
	}
	select {
	case <-joined:
		t.Fatal("retained rollback owner preceded physical join")
	default:
	}
	close(tr.release)
	released = true
	for i := 0; i < 2; i++ {
		if err := <-joined; !errors.Is(err, ErrCloseUnresolved) || !errors.Is(err, errSyntheticClose) || tr.calls.Load() != 1 {
			t.Fatal("rollback erased late error/uncertainty or repeated close")
		}
	}
	if err := result.owner.Close(); !errors.Is(err, ErrCloseUnresolved) || !errors.Is(err, errSyntheticClose) || tr.calls.Load() != 1 {
		t.Fatal("repeated retained close changed evidence")
	}
}
