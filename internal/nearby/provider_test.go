package nearby

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRecordProvider struct {
	incoming chan recordObservation
	closed   chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	reads    atomic.Int32
	once     sync.Once
	closeErr error
}

func newFakeRecordProvider() *fakeRecordProvider {
	return &fakeRecordProvider{incoming: make(chan recordObservation, MaxReceivedDatagrams+1), closed: make(chan struct{})}
}
func (p *fakeRecordProvider) ReadObservation() (recordObservation, error) {
	select {
	case o := <-p.incoming:
		p.reads.Add(1)
		return o, nil
	case <-p.closed:
		return recordObservation{}, context.Canceled
	}
}
func (p *fakeRecordProvider) Close() error {
	p.calls.Add(1)
	p.once.Do(func() { close(p.closed) })
	if p.release != nil {
		<-p.release
	}
	return p.closeErr
}
func startFakeProvider(t *testing.T, p *fakeRecordProvider) (*Session, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	ad := testAd(999)
	ad.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := startProvider(context.Background(), Config{testScope(), ad, clock}, func(context.Context, Config, discoveryPolicy) (recordProvider, error) { return p, nil }, discoveryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clock
}
func TestProviderOSObservationAndLateCallbackCancellation(t *testing.T) {
	p := newFakeRecordProvider()
	s, _ := startFakeProvider(t, p)
	o := testRecordObservation(t, testAd(1), osDNSServiceObservation)
	p.incoming <- o
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	c := s.Candidates()[0]
	if c.Source.IsValid() || c.Provenance != OSDNSServiceProvenance {
		t.Fatal("fabricated packet provenance")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p.incoming <- o
	if len(s.Candidates()) != 0 {
		t.Fatal("late callback resurrected candidate")
	}
	if _, err := s.Lookup(c.ID); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("closed ID accepted")
	}
	if p.calls.Load() != 1 {
		t.Fatal("provider close repeated")
	}
}
func TestProviderHeldCloseRetainsOwnerAndJoinsOnce(t *testing.T) {
	p := newFakeRecordProvider()
	p.release = make(chan struct{})
	s, _ := startFakeProvider(t, p)
	p.incoming <- testRecordObservation(t, testAd(1), osDNSServiceObservation)
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	id := s.Candidates()[0].ID
	err := s.Close()
	if !errors.Is(err, ErrCloseUnresolved) {
		t.Fatal("held callback barrier reported closed", err)
	}
	if _, err := s.Lookup(id); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("held close kept candidate")
	}
	select {
	case <-s.Done():
		t.Fatal("Done before physical provider join")
	default:
	}
	close(p.release)
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("release failed to join")
	}
	if err := s.Close(); err != nil || p.calls.Load() != 1 {
		t.Fatal("close retried uncertain owner", err)
	}
}
func TestProviderFailedCloseRemainsUncertain(t *testing.T) {
	p := newFakeRecordProvider()
	p.closeErr = errors.New("synthetic provider close failure")
	s, _ := startFakeProvider(t, p)
	for i := 0; i < 2; i++ {
		if err := s.Close(); !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, p.closeErr) {
			t.Fatal("close failure lost", err)
		}
	}
	if p.calls.Load() != 1 {
		t.Fatal("close retried")
	}
}
func TestProviderRawQueueExhaustionFencesBeforeJoin(t *testing.T) {
	s, _, tr := startFake(t)
	// Hold admission so the raw reader's sole64-entry queue deterministically
	// overflows. OS providers instead own their callback queue before this pump.
	s.mu.Lock()
	for i := 0; i < maxObservationQueue+2; i++ {
		tr.incoming <- testPacket(t, testAd(1))
	}
	eventually(t, func() bool { return s.ctx.Err() != nil })
	s.mu.Unlock()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("overflow failed to join")
	}
	if !errors.Is(s.Err(), ErrReceiveBudget) || len(s.Candidates()) != 0 {
		t.Fatal("overflow retained admission", s.Err())
	}
}
func TestProviderRateCapNeverDropsRemovalOrConflict(t *testing.T) {
	for _, mutation := range []string{"remove", "change"} {
		t.Run(mutation, func(t *testing.T) {
			p := newFakeRecordProvider()
			s, _ := startFakeProvider(t, p)
			o := testRecordObservation(t, testAd(1), osDNSServiceObservation)
			p.incoming <- o
			eventually(t, func() bool { return len(s.Candidates()) == 1 })
			id := s.Candidates()[0].ID
			for i := 1; i < MaxParsedPerSecond; i++ {
				p.incoming <- o
			}
			changed := testRecordObservation(t, testAd(1), osDNSServiceObservation)
			if mutation == "remove" {
				changed.Records[0].Removed = true
			} else {
				changed.Records[1].Port++
			}
			p.incoming <- changed
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("rate-capped OS change was silently skipped")
			}
			if !errors.Is(s.Err(), ErrReceiveBudget) {
				t.Fatal(s.Err())
			}
			if _, err := s.Lookup(id); !errors.Is(err, ErrCandidateUnavailable) {
				t.Fatal("OS stale hint survived lost change")
			}
		})
	}
}
func TestProviderLifetimeCallbackBudgetIncludesEmptyEvents(t *testing.T) {
	p := newFakeRecordProvider()
	s, clock := startFakeProvider(t, p)
	// Eight events per virtual second stay below the OS rate cap and the owner
	// deadline, isolating the lifetime budget from either other terminal limit.
	for base := 0; base < MaxReceivedDatagrams; base += 8 {
		clock.advance(time.Second)
		for i := 0; i < 8; i++ {
			p.incoming <- recordObservation{Kind: osDNSServiceObservation, InterfaceIndex: 7}
		}
		eventually(t, func() bool { return p.reads.Load() >= int32(base+8) })
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("empty callback lifetime flood unbounded")
	}
	if !errors.Is(s.Err(), ErrReceiveBudget) || p.reads.Load() != MaxReceivedDatagrams {
		t.Fatal(s.Err(), p.reads.Load())
	}
}
func TestProviderPartialFactoryFailureRetainsCloseOwnership(t *testing.T) {
	p := newFakeRecordProvider()
	p.closeErr = errors.New("synthetic close failure")
	ad := testAd(999)
	ad.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := startProvider(context.Background(), Config{testScope(), ad, newFakeClock()}, func(context.Context, Config, discoveryPolicy) (recordProvider, error) { return p, ErrUnsupported }, discoveryPolicy{})
	if s == nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, ErrCloseUncertain) {
		t.Fatal("lost failed factory owner", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("failed factory not closed once")
	}
}

func TestProviderInvalidOSCallbackAlwaysInvalidates(t *testing.T) {
	for _, mutate := range []func(*recordObservation){
		func(o *recordObservation) {
			o.Kind = rawPacketObservation
			o.Source = netip.MustParseAddrPort("192.168.40.9:5353")
		},
		func(o *recordObservation) { o.Source = netip.MustParseAddrPort("192.168.40.9:5353") },
		func(o *recordObservation) { o.InterfaceIndex++ },
		func(o *recordObservation) { o.query = true },
		func(o *recordObservation) { o.Records[2].TXT[0] = "unsupported=value" },
	} {
		p := newFakeRecordProvider()
		s, _ := startFakeProvider(t, p)
		p.incoming <- testRecordObservation(t, testAd(1), osDNSServiceObservation)
		eventually(t, func() bool { return len(s.Candidates()) == 1 })
		id := s.Candidates()[0].ID
		changed := testRecordObservation(t, testAd(1), osDNSServiceObservation)
		mutate(&changed)
		p.incoming <- changed
		select {
		case <-s.Done():
		case <-time.After(time.Second):
			t.Fatal("invalid OS callback left hint active")
		}
		if !errors.Is(s.Err(), ErrInvalidPacket) {
			t.Fatal("invalid OS callback not surfaced", s.Err())
		}
		if _, err := s.Lookup(id); !errors.Is(err, ErrCandidateUnavailable) {
			t.Fatal("OS callback invented provenance or retained a hint")
		}
	}
}
