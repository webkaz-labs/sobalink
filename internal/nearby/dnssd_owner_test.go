package nearby

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type dnssdFakeBackend struct {
	events               chan dnssdEvent
	stopped              chan struct{}
	closeEntered         chan struct{}
	startEntered         chan struct{}
	holdStart, holdClose <-chan struct{}
	startErr, closeErr   error
	once                 sync.Once
	closes               atomic.Int32
	mu                   sync.Mutex
	queries              []dnssdRecordData
}

func dnssdFake() *dnssdFakeBackend {
	return &dnssdFakeBackend{events: make(chan dnssdEvent, MaxReceivedDatagrams+1), stopped: make(chan struct{}), closeEntered: make(chan struct{}), startEntered: make(chan struct{})}
}
func (f *dnssdFakeBackend) Start() error {
	close(f.startEntered)
	if f.holdStart != nil {
		<-f.holdStart
	}
	return f.startErr
}
func (f *dnssdFakeBackend) Next() (dnssdEvent, error) {
	select {
	case <-f.stopped:
		return dnssdEvent{}, context.Canceled
	case e := <-f.events:
		return e, nil
	}
}
func (f *dnssdFakeBackend) Query(name string, typ uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, dnssdRecordData{Name: name, Type: typ})
	return nil
}
func (f *dnssdFakeBackend) Close() error {
	f.once.Do(func() {
		f.closes.Add(1)
		close(f.stopped)
		close(f.closeEntered)
		if f.holdClose != nil {
			<-f.holdClose
		}
	})
	return f.closeErr
}
func dnssdTestConfig() Config {
	return Config{Scope: Scope{InterfaceIndex: 7, LocalAddress: netip.MustParseAddr("192.168.40.2"), Prefix: netip.MustParsePrefix("192.168.40.0/24")}, Advertisement: Advertisement{SessionID: strings.Repeat("1", 64), Alias: "Example", Protocol: Protocol, Capability: Capability, Bootstrap: netip.MustParseAddrPort("192.168.40.2:15443")}}
}
func dnssdFakeBinding(scope Scope, original *net.Interface, policy discoveryPolicy) (net.Interface, error) {
	return net.Interface{Index: scope.InterfaceIndex, Name: "synthetic", Flags: net.FlagUp | net.FlagMulticast}, nil
}
func dnssdWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("bounded worker did not reach expected barrier")
	}
}
func dnssdRead(t *testing.T, p recordProvider) recordObservation {
	t.Helper()
	type result struct {
		o recordObservation
		e error
	}
	done := make(chan result, 1)
	go func() { o, e := p.ReadObservation(); done <- result{o, e} }()
	select {
	case got := <-done:
		if got.e != nil {
			t.Fatal(got.e)
		}
		return got.o
	case <-time.After(time.Second):
		t.Fatal("bounded observation missing")
		return recordObservation{}
	}
}
func dnssdPeerRecords(t *testing.T) dnssdRegistration {
	t.Helper()
	cfg := dnssdTestConfig()
	cfg.Advertisement.SessionID = strings.Repeat("2", 64)
	r, e := dnssdRegistrationFor(cfg, discoveryPolicy{})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func dnssdEventFor(r dnssdRecordData) dnssdEvent {
	return dnssdEvent{InterfaceIndex: 7, Name: r.Name, Type: r.Type, Class: 1, Data: append([]byte(nil), r.Data...), TTL: 30}
}

func TestDNSServiceOwnerPublishedBeforeBlockingFactory(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := dnssdFake()
	p, err := newDNSServiceProvider(context.Background(), dnssdTestConfig(), discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { close(entered); <-release; return f, nil }, dnssdFakeBinding)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	dnssdWait(t, entered)
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Close lost blocked factory owner")
	default:
	}
	close(release)
	dnssdWait(t, done)
	if f.closes.Load() != 1 {
		t.Fatal("backend not closed once")
	}
}
func TestDNSServiceCloseJoinsBlockedSetupAndNativeTeardown(t *testing.T) {
	for _, stage := range []string{"setup", "teardown"} {
		t.Run(stage, func(t *testing.T) {
			f := dnssdFake()
			release := make(chan struct{})
			if stage == "setup" {
				f.holdStart = release
			} else {
				f.holdClose = release
			}
			p, err := newDNSServiceProvider(context.Background(), dnssdTestConfig(), discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { return f, nil }, dnssdFakeBinding)
			if err != nil {
				t.Fatal(err)
			}
			dnssdWait(t, f.startEntered)
			done := make(chan error, 2)
			go func() { done <- p.Close() }()
			dnssdWait(t, f.closeEntered)
			if _, err := p.ReadObservation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("admission remains live: %v", err)
			}
			// Native Close has already fenced/unblocked Next. A physical hold still
			// prevents provider Close, independently of outer Session timeout timers.
			select {
			case <-done:
				t.Fatal("Close preceded physical native join")
			default:
			}
			go func() { done <- p.Close() }()
			close(release)
			for i := 0; i < 2; i++ {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(time.Second):
					t.Fatal("close not joined")
				}
			}
			if f.closes.Load() != 1 {
				t.Fatal("duplicate backend Close")
			}
		})
	}
}
func TestDNSServicePartialConstructionRetainsCloseError(t *testing.T) {
	f := dnssdFake()
	f.closeErr = errors.New("synthetic close error")
	startErr := errors.New("synthetic constructor error")
	p, err := newDNSServiceProvider(context.Background(), dnssdTestConfig(), discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { return f, startErr }, dnssdFakeBinding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.ReadObservation(); !errors.Is(err, startErr) {
		t.Fatalf("lost constructor failure: %v", err)
	}
	if err = p.Close(); !errors.Is(err, ErrCloseUncertain) || !errors.Is(err, f.closeErr) {
		t.Fatalf("lost close owner failure: %v", err)
	}
	if f.closes.Load() != 1 {
		t.Fatal("partial owner not closed once")
	}
}
func TestDNSServiceExactQueriesProvenanceAndBinding(t *testing.T) {
	f := dnssdFake()
	var changed atomic.Bool
	binding := func(s Scope, n *net.Interface, p discoveryPolicy) (net.Interface, error) {
		if changed.Load() {
			return net.Interface{}, ErrScopeChanged
		}
		return dnssdFakeBinding(s, n, p)
	}
	p, err := newDNSServiceProvider(context.Background(), dnssdTestConfig(), discoveryPolicy{}, func(reg dnssdRegistration) (dnssdBackend, error) {
		if reg.InterfaceIndex != 7 {
			t.Error("scope widened")
		}
		return f, nil
	}, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	records := dnssdPeerRecords(t)
	f.events <- dnssdEventFor(records.Records[0])
	o := dnssdRead(t, p)
	if o.Source.IsValid() || o.Kind != osDNSServiceObservation || o.InterfaceIndex != 7 || len(o.Records) != 1 || o.query {
		t.Fatalf("fabricated or widened provenance: %+v", o)
	}
	f.mu.Lock()
	queries := append([]dnssdRecordData(nil), f.queries...)
	f.mu.Unlock()
	if len(queries) != 3 || queries[0].Name != records.Records[1].Name || queries[0].Type != dnssdTypeSRV || queries[1].Type != dnssdTypeTXT || queries[2].Name != records.Records[3].Name || queries[2].Type != dnssdTypeA {
		t.Fatalf("unexpected query expansion: %+v", queries)
	}
	changed.Store(true)
	f.events <- dnssdEventFor(records.Records[1])
	if _, err = p.ReadObservation(); !errors.Is(err, ErrScopeChanged) {
		t.Fatalf("changed scope admitted: %v", err)
	}
}
func TestDNSServiceRateBudgetAndMalformedUpdatesFence(t *testing.T) {
	for _, scenario := range []string{"rate", "index", "rdata"} {
		t.Run(scenario, func(t *testing.T) {
			f := dnssdFake()
			cfg := dnssdTestConfig()
			cfg.Clock = newFakeClock()
			p, err := newDNSServiceProvider(context.Background(), cfg, discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { return f, nil }, dnssdFakeBinding)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			event := dnssdEventFor(dnssdPeerRecords(t).Records[0])
			if scenario == "index" {
				event.InterfaceIndex = 0
			} else if scenario == "rdata" {
				event.Data = []byte{0xc0, 0}
			}
			expected := ErrInvalidPacket
			if scenario == "rate" {
				for i := 0; i < MaxParsedPerSecond; i++ {
					f.events <- event
					dnssdRead(t, p)
				}
				expected = ErrReceiveBudget
			}
			f.events <- event
			if _, err = p.ReadObservation(); !errors.Is(err, expected) {
				t.Fatalf("lost fail-closed fence: %v", err)
			}
		})
	}
}
func TestDNSServiceSilentBindingChangeStopsOwner(t *testing.T) {
	f := dnssdFake()
	cfg := dnssdTestConfig()
	clock := newFakeClock()
	cfg.Clock = clock
	var changed atomic.Bool
	binding := func(s Scope, n *net.Interface, p discoveryPolicy) (net.Interface, error) {
		if changed.Load() {
			return net.Interface{}, ErrScopeChanged
		}
		return dnssdFakeBinding(s, n, p)
	}
	p, err := newDNSServiceProvider(context.Background(), cfg, discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { return f, nil }, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	dnssdWait(t, f.startEntered)
	// Wait only for the deterministic watcher timer to exist, without a socket.
	eventually(t, func() bool { return clock.active() == 2 })
	changed.Store(true)
	clock.advance(time.Second)
	if _, err = p.ReadObservation(); !errors.Is(err, ErrScopeChanged) {
		t.Fatalf("silent binding change remained live: %v", err)
	}
	dnssdWait(t, f.closeEntered)
}
func TestDNSServiceRecordProfileRoundTripAndRejections(t *testing.T) {
	reg := dnssdPeerRecords(t)
	for _, raw := range reg.Records {
		r, err := dnssdDecodeRecord(dnssdEventFor(raw))
		if err != nil || r.TTL != CandidateTTL {
			t.Fatalf("record roundtrip: %+v %v", r, err)
		}
	}
	for _, mutate := range []func(*dnssdEvent){func(e *dnssdEvent) { e.Class = 2 }, func(e *dnssdEvent) { e.Data = []byte{0xc0, 0x0c} }, func(e *dnssdEvent) { e.Name = "unrelated.local." }, func(e *dnssdEvent) { e.Data = make([]byte, maxRecordBytes+1) }} {
		e := dnssdEventFor(reg.Records[0])
		mutate(&e)
		if _, err := dnssdDecodeRecord(e); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
	e := dnssdEventFor(reg.Records[0])
	e.TTL = 0
	r, err := dnssdDecodeRecord(e)
	if err != nil || !r.Removed {
		t.Fatal("zero TTL did not invalidate")
	}
	for _, raw := range reg.Records {
		if strings.Contains(raw.Name, "synthetic") || strings.Contains(raw.Name, "Example") {
			t.Fatal("advertisement exposed a stable/OS name")
		}
	}
}

func TestDNSServiceCanonicalDNSNamesPreserveLabelBoundaries(t *testing.T) {
	raw := dnssdPeerRecords(t).Records[0]
	event := dnssdEventFor(raw)
	event.Name = strings.ToUpper(event.Name)
	for i, b := range event.Data {
		if b >= 'a' && b <= 'z' {
			event.Data[i] = b - 'a' + 'A'
		}
	}
	if _, err := dnssdDecodeRecord(event); err != nil {
		t.Fatalf("ordinary mixed case rejected: %v", err)
	}
	for _, bad := range []string{"\\046.local.", "s..local."} {
		if _, ok := dnssdCanonicalName(bad); ok {
			t.Fatal("escaped/empty label accepted")
		}
	}
	for _, bad := range [][]byte{{3, 'a', '.', 'b', 0}, {3, 'a', '\\', 'b', 0}} {
		if _, err := dnssdDecodeName(bad); err == nil {
			t.Fatal("embedded label separator accepted")
		}
	}
}

// A step clock isolates the lifetime cap from the independently tested rate cap.
type dnssdStepClock struct{ ticks atomic.Int64 }

func (c *dnssdStepClock) Now() time.Time               { return time.Unix(1700000000+c.ticks.Add(1), 0) }
func (*dnssdStepClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func TestDNSServiceLifetimeAndQueryCaps(t *testing.T) {
	for _, kind := range []string{"callbacks", "queries"} {
		t.Run(kind, func(t *testing.T) {
			f := dnssdFake()
			cfg := dnssdTestConfig()
			cfg.Clock = &dnssdStepClock{}
			p, err := newDNSServiceProvider(context.Background(), cfg, discoveryPolicy{}, func(dnssdRegistration) (dnssdBackend, error) { return f, nil }, dnssdFakeBinding)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			count := MaxReceivedDatagrams
			if kind == "queries" {
				count = MaxCandidates + 1
			}
			for i := 0; i < count; i++ {
				event := dnssdEventFor(dnssdPeerRecords(t).Records[0])
				if kind == "queries" {
					peer := cfg
					raw := make([]byte, 32)
					raw[31] = byte(i + 2)
					peer.Advertisement.SessionID = hex.EncodeToString(raw)
					records, e := dnssdRegistrationFor(peer, discoveryPolicy{})
					if e != nil {
						t.Fatal(e)
					}
					event = dnssdEventFor(records.Records[0])
				}
				f.events <- event
				if i == count-1 {
					if _, e := p.ReadObservation(); !errors.Is(e, ErrReceiveBudget) {
						t.Fatalf("lifetime cap lost: %v", e)
					}
				} else {
					dnssdRead(t, p)
				}
			}
			f.mu.Lock()
			queries := len(f.queries)
			f.mu.Unlock()
			if queries > 3*MaxCandidates {
				t.Fatal("query cap exceeded")
			}
		})
	}
}
