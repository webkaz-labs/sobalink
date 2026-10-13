package nearby

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func testScope() Scope {
	return Scope{InterfaceIndex: 7, LocalAddress: netip.MustParseAddr("192.168.40.1"), Prefix: netip.MustParsePrefix("192.168.40.0/24")}
}
func testAd(n int) Advertisement {
	return Advertisement{SessionID: fmt.Sprintf("%064x", n), Alias: "Peer A", Protocol: Protocol, Capability: Capability, Bootstrap: netip.MustParseAddrPort("192.168.40.2:42000")}
}
func testPacket(t *testing.T, a Advertisement) Packet {
	t.Helper()
	data, err := packAdvertisement(a)
	if err != nil {
		t.Fatal(err)
	}
	return Packet{Data: data, Source: netip.AddrPortFrom(a.Bootstrap.Addr(), mdnsPort), Destination: multicast4, InterfaceIndex: 7, HopLimit: 255}
}
func TestScopeRejectsBroaderOrAmbiguousAddresses(t *testing.T) {
	for _, s := range []Scope{
		{7, netip.MustParseAddr("192.168.40.1"), netip.MustParsePrefix("0.0.0.0/0")},
		{7, netip.MustParseAddr("192.168.40.1"), netip.MustParsePrefix("192.0.0.0/8")},
		{7, netip.MustParseAddr("192.168.40.1"), netip.MustParsePrefix("192.168.40.1/24")},
		{0, netip.MustParseAddr("192.168.40.1"), netip.MustParsePrefix("192.168.40.0/24")},
		{7, netip.MustParseAddr("::ffff:192.168.40.1"), netip.MustParsePrefix("::ffff:192.168.40.0/120")},
		{7, netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8")},
		{7, netip.MustParseAddr("fd00::1%nic"), netip.MustParsePrefix("fd00::/64")},
	} {
		if s.Validate() == nil {
			t.Errorf("accepted invalid scope %#v", s)
		}
	}
	for _, s := range []Scope{testScope(), {7, netip.MustParseAddr("fd00::1"), netip.MustParsePrefix("fd00::/64")}} {
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestAdvertisementOnlyExplicitPublicHints(t *testing.T) {
	a := testAd(1)
	a.Alias = "端末 A"
	if a.Validate() != nil {
		t.Fatal("valid explicit Unicode alias rejected")
	}
	for _, mutate := range []func(*Advertisement){
		func(a *Advertisement) { a.SessionID = strings.Repeat("a", 32) },
		func(a *Advertisement) { a.SessionID = strings.Repeat("0", 64) },
		func(a *Advertisement) { a.SessionID = strings.Repeat("A", 64) },
		func(a *Advertisement) { a.Alias = "hidden\nvalue" },
		func(a *Advertisement) { a.Alias = "peer\u202Ename" },
		func(a *Advertisement) { a.Alias = " Peer A" },
		func(a *Advertisement) { a.Alias = "Peer\u2028A" },
		func(a *Advertisement) { a.Alias = "Peer\u2029A" },
		func(a *Advertisement) { a.Alias = strings.Repeat("a", 81) },
		func(a *Advertisement) { a.Protocol = "other" },
		func(a *Advertisement) { a.Capability = "files" },
		func(a *Advertisement) { a.Bootstrap = netip.MustParseAddrPort("192.168.40.2:443") },
		func(a *Advertisement) { a.Bootstrap = netip.MustParseAddrPort("192.168.40.2:54543") },
		func(a *Advertisement) { a.Bootstrap = netip.MustParseAddrPort("192.168.40.2:54544") },
		func(a *Advertisement) { a.Bootstrap = netip.MustParseAddrPort("192.168.40.2:54545") },
		func(a *Advertisement) { a.Bootstrap = netip.MustParseAddrPort("203.0.113.1:42000") },
	} {
		b := a
		mutate(&b)
		if b.Validate() == nil {
			t.Errorf("accepted invalid ad %#v", b)
		}
	}
	id, err := NewSessionID()
	if err != nil || len(id) != 64 {
		t.Fatalf("session ID: %q %v", id, err)
	}
	data, err := packAdvertisement(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hostname=", "identity=", "token=", "pin=", "files=", "services="} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("forbidden ad field")
		}
	}
	if len(data) > MaxDatagramBytes {
		t.Fatal("unbounded advertisement")
	}
}
func TestWireRoundTripAndGoodbye(t *testing.T) {
	for _, ip := range []string{"192.168.40.2", "fd00::2"} {
		a := testAd(1)
		a.Bootstrap = netip.AddrPortFrom(netip.MustParseAddr(ip), 42000)
		p := testPacket(t, a)
		got, ttl, q, err := unpackPacket(p)
		if err != nil || q || got != a || ttl != CandidateTTL {
			t.Fatalf("roundtrip %v %v %v %v", got, ttl, q, err)
		}
		var m dnsmessage.Message
		_ = m.Unpack(p.Data)
		for i := range m.Answers {
			m.Answers[i].Header.TTL = 0
		}
		p.Data, _ = m.Pack()
		_, ttl, _, err = unpackPacket(p)
		if err != nil || ttl != 0 {
			t.Fatal("goodbye not represented")
		}
	}
	p := Packet{Data: packQuery()}
	_, _, q, err := unpackPacket(p)
	if err != nil || !q {
		t.Fatal("query rejected")
	}
}
func TestWireRejectsSpoofedMalformedAndUnknownData(t *testing.T) {
	base := testPacket(t, testAd(1))
	for name, mutate := range map[string]func(*dnsmessage.Message){
		"wrong_instance":  func(m *dnsmessage.Message) { m.Answers[1].Header.Name, _ = dnsmessage.NewName("other.local.") },
		"unknown_field":   func(m *dnsmessage.Message) { m.Answers[2].Body.(*dnsmessage.TXTResource).TXT[0] = "token=secret" },
		"duplicate_field": func(m *dnsmessage.Message) { m.Answers[2].Body.(*dnsmessage.TXTResource).TXT[0] = "alias=other" },
		"redirect_host": func(m *dnsmessage.Message) {
			m.Answers[1].Body.(*dnsmessage.SRVResource).Target, _ = dnsmessage.NewName("other.local.")
		},
		"third_party_address": func(m *dnsmessage.Message) { m.Answers[3].Body.(*dnsmessage.AResource).A = [4]byte{192, 168, 40, 3} },
		"long_ttl": func(m *dnsmessage.Message) {
			for i := range m.Answers {
				m.Answers[i].Header.TTL = 31
			}
		},
		"mixed_ttl":      func(m *dnsmessage.Message) { m.Answers[0].Header.TTL = 1 },
		"header_id":      func(m *dnsmessage.Message) { m.Header.ID = 1 },
		"truncated":      func(m *dnsmessage.Message) { m.Header.Truncated = true },
		"duplicate_type": func(m *dnsmessage.Message) { m.Answers[3] = m.Answers[2] },
		"extra_record":   func(m *dnsmessage.Message) { m.Answers = append(m.Answers, m.Answers[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			var m dnsmessage.Message
			_ = m.Unpack(base.Data)
			mutate(&m)
			p := base
			p.Data, _ = m.Pack()
			if _, _, _, err := unpackPacket(p); err == nil {
				t.Fatal("accepted malformed advertisement")
			}
		})
	}
	for _, data := range [][]byte{nil, make([]byte, 11), make([]byte, MaxDatagramBytes+1), append([]byte(nil), base.Data[:15]...)} {
		if _, _, _, err := unpackPacket(Packet{Data: data}); err == nil {
			t.Fatal("malformed input accepted")
		}
	}
	p := base
	p.Data = bytes.Clone(base.Data)
	binary.BigEndian.PutUint16(p.Data[6:8], 65535)
	if _, _, _, err := unpackPacket(p); err == nil {
		t.Fatal("unbounded count accepted")
	}
}
func TestPacketMetadataMustMatchSelectedScope(t *testing.T) {
	s, p := testScope(), testPacket(t, testAd(1))
	if !validatePacket(s, p) {
		t.Fatal("valid packet rejected")
	}
	for _, mutate := range []func(*Packet){
		func(p *Packet) { p.InterfaceIndex++ }, func(p *Packet) { p.HopLimit = 254 }, func(p *Packet) { p.Destination = s.LocalAddress },
		func(p *Packet) { p.Source = netip.MustParseAddrPort("192.168.41.2:5353") }, func(p *Packet) { p.Source = netip.MustParseAddrPort("192.168.40.2:53") },
		func(p *Packet) { p.Source = netip.MustParseAddrPort("[::ffff:192.168.40.2]:5353") },
	} {
		q := p
		mutate(&q)
		if validatePacket(s, q) {
			t.Fatal("invalid socket metadata accepted")
		}
	}
}
func TestCandidateRevisionsExpiryCollisionsAndCapacity(t *testing.T) {
	s := newStore(testScope())
	now := time.Now()
	a := testAd(1)
	source := netip.AddrPortFrom(a.Bootstrap.Addr(), mdnsPort)
	s.observe(a, source, CandidateTTL, now)
	old := s.list(now)[0]
	s.observe(a, source, CandidateTTL, now.Add(time.Second))
	current := s.list(now.Add(time.Second))[0]
	if old.ID != current.ID || current.ExpiresAt != now.Add(31*time.Second) {
		t.Fatal("heartbeat changed immutable ID or expiry")
	}
	a.Bootstrap = netip.MustParseAddrPort("192.168.40.2:42001")
	s.observe(a, source, CandidateTTL, now.Add(2*time.Second))
	replacement := s.list(now.Add(2 * time.Second))[0]
	if replacement.ID == old.ID {
		t.Fatal("endpoint silently retargeted")
	}
	if _, err := s.lookup(old.ID, now.Add(2*time.Second)); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("old selection survived replacement")
	}
	a.Bootstrap = old.Advertisement.Bootstrap
	s.observe(a, source, CandidateTTL, now.Add(3*time.Second))
	replay := s.list(now.Add(3 * time.Second))[0]
	if replay.ID == old.ID {
		t.Fatal("replay resurrected stale ID")
	}
	s.observe(a, source, CandidateTTL, now.Add(33*time.Second))
	afterExpiry := s.list(now.Add(33 * time.Second))[0]
	if afterExpiry.ID == replay.ID {
		t.Fatal("expiry revived old candidate")
	}
	second := a
	second.Bootstrap = netip.MustParseAddrPort("192.168.40.3:42000")
	s.observe(second, netip.MustParseAddrPort("192.168.40.3:5353"), CandidateTTL, now.Add(33*time.Second))
	if len(s.list(now.Add(33*time.Second))) != 2 {
		t.Fatal("same alias/session merged distinct sources")
	}
	for i := 2; i < 100; i++ {
		s.observe(testAd(i), source, CandidateTTL, now.Add(34*time.Second))
	}
	if len(s.items) != MaxCandidates {
		t.Fatal("candidate cap failed")
	}
	s.prune(now.Add(64 * time.Second))
	if len(s.items) != 0 {
		t.Fatal("expired candidates retained")
	}
}
func TestGoodbyeOnlyRemovesMatchingObservation(t *testing.T) {
	s := newStore(testScope())
	a := testAd(1)
	now := time.Now()
	source := netip.MustParseAddrPort("192.168.40.2:5353")
	s.observe(a, source, CandidateTTL, now)
	changed := a
	changed.Alias = "other"
	s.observe(changed, source, 0, now)
	if len(s.items) != 1 {
		t.Fatal("unmatched goodbye removed candidate")
	}
	s.observe(a, source, 0, now)
	if len(s.items) != 0 {
		t.Fatal("matching goodbye ignored")
	}
}

func TestBindingRejectsStaleAddressPrefixAndInterface(t *testing.T) {
	scope := testScope()
	ifi := net.Interface{Index: 7, Name: "test-interface", HardwareAddr: net.HardwareAddr{0x02, 0, 0, 0, 0, 1}, Flags: net.FlagUp | net.FlagMulticast}
	addrs := []net.Addr{&net.IPNet{IP: net.IPv4(192, 168, 40, 1), Mask: net.CIDRMask(24, 32)}}
	if !bindingMatches(scope, ifi, addrs, &ifi) {
		t.Fatal("current binding rejected")
	}
	original := ifi
	original.Name = "prior-interface"
	if bindingMatches(scope, ifi, addrs, &original) {
		t.Fatal("reassigned interface accepted")
	}
	changed := scope
	changed.Prefix = netip.MustParsePrefix("192.168.0.0/16")
	if bindingMatches(changed, ifi, addrs, &ifi) || bindingMatches(scope, ifi, nil, &ifi) {
		t.Fatal("stale prefix/address accepted")
	}
	ifi.Flags |= net.FlagLoopback
	if bindingMatches(scope, ifi, addrs, nil) {
		t.Fatal("production loopback accepted")
	}
}

// All session tests inject transport and fake time. None open network sockets.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]bool
}
type fakeTimer struct {
	clock *fakeClock
	at    time.Time
	ch    chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0), timers: make(map[*fakeTimer]bool)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers[t] = true
	return t
}
func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, active := t.clock.timers[t]
	delete(t.clock.timers, t)
	return active
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for t := range c.timers {
		if !c.now.Before(t.at) {
			delete(c.timers, t)
			t.ch <- c.now
		}
	}
}
func (c *fakeClock) active() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.timers) }

type fakeTransport struct {
	mu         sync.Mutex
	incoming   chan Packet
	closed     chan struct{}
	once       sync.Once
	writes     [][]byte
	blockWrite bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{incoming: make(chan Packet, MaxReceivedDatagrams+1), closed: make(chan struct{})}
}
func (f *fakeTransport) ReadPacket() (Packet, error) {
	select {
	case p := <-f.incoming:
		return p, nil
	case <-f.closed:
		return Packet{}, context.Canceled
	}
}
func (f *fakeTransport) WritePacket(b []byte) error {
	if f.blockWrite {
		<-f.closed
		return context.Canceled
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.closed:
		return context.Canceled
	default:
	}
	f.writes = append(f.writes, bytes.Clone(b))
	return nil
}
func (f *fakeTransport) Close() error { f.once.Do(func() { close(f.closed) }); return nil }
func (f *fakeTransport) count() int   { f.mu.Lock(); defer f.mu.Unlock(); return len(f.writes) }
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func startFake(t *testing.T) (*Session, *fakeClock, *fakeTransport) {
	t.Helper()
	clock, tr := newFakeClock(), newFakeTransport()
	local := testAd(999)
	local.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), local, clock}, func(Scope) (Transport, error) { return tr, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	eventually(t, func() bool { return tr.count() == 2 })
	return s, clock, tr
}
func TestSessionExpiryClosesAllResources(t *testing.T) {
	s, clock, tr := startFake(t)
	tr.incoming <- testPacket(t, testAd(1))
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	clock.advance(SessionLifetime)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expiry did not close session")
	}
	if !errors.Is(s.Err(), ErrExpired) || len(s.Candidates()) != 0 || clock.active() != 0 {
		t.Fatalf("expiry leaked state/timers: %v %d", s.Err(), clock.active())
	}
	select {
	case <-tr.closed:
	default:
		t.Fatal("socket not closed")
	}
}
func TestSessionCancelInvalidatesCandidateAndJoins(t *testing.T) {
	s, clock, tr := startFake(t)
	tr.incoming <- testPacket(t, testAd(1))
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	id := s.Candidates()[0].ID
	_ = s.Close()
	if _, err := s.Lookup(id); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("closed candidate usable")
	}
	if clock.active() != 0 {
		t.Fatal("timers leaked")
	}
	before := tr.count()
	clock.advance(time.Hour)
	if tr.count() != before {
		t.Fatal("advertised after close")
	}
}
func TestCancellationUnblocksBlockedWrite(t *testing.T) {
	clock, tr := newFakeClock(), newFakeTransport()
	tr.blockWrite = true
	ctx, cancel := context.WithCancel(context.Background())
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(ctx, Config{testScope(), a, clock}, func(Scope) (Transport, error) { return tr, nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancel left blocked writer/reader")
	}
	if clock.active() != 0 {
		t.Fatal("timers leaked")
	}
}
func TestSessionMalformedFloodStopsAtFiniteBudget(t *testing.T) {
	s, _, tr := startFake(t)
	for i := 0; i < MaxReceivedDatagrams; i++ {
		tr.incoming <- Packet{}
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("flood spun indefinitely")
	}
	if !errors.Is(s.Err(), ErrReceiveBudget) {
		t.Fatal(s.Err())
	}
}
func TestSessionParserAndResponseRateBounded(t *testing.T) {
	s, clock, tr := startFake(t)
	for i := 1; i <= 32; i++ {
		tr.incoming <- testPacket(t, testAd(i))
	}
	eventually(t, func() bool { return len(tr.incoming) == 0 })
	eventually(t, func() bool { return len(s.Candidates()) == MaxParsedPerSecond })
	clock.advance(time.Second)
	query := testPacket(t, testAd(100))
	query.Data = packQuery()
	for i := 0; i < 32; i++ {
		tr.incoming <- query
	}
	eventually(t, func() bool { return len(tr.incoming) == 0 })
	eventually(t, func() bool { return tr.count() == 3 })
	if len(s.Candidates()) != MaxParsedPerSecond {
		t.Fatal("query created candidate")
	}
}
func TestSessionTTLUsesLocalObservationAndSelectionChanges(t *testing.T) {
	s, clock, tr := startFake(t)
	a := testAd(1)
	tr.incoming <- testPacket(t, a)
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	id := s.Candidates()[0].ID
	a.Alias = "Peer B"
	tr.incoming <- testPacket(t, a)
	eventually(t, func() bool { return len(s.Candidates()) == 1 && s.Candidates()[0].ID != id })
	if _, err := s.Lookup(id); err == nil {
		t.Fatal("changed selection remains usable")
	}
	clock.advance(CandidateTTL)
	if len(s.Candidates()) != 0 {
		t.Fatal("candidate survived exact TTL")
	}
}
func TestStartNeverOpensForInvalidOrCancelledConfig(t *testing.T) {
	opened := 0
	open := func(Scope) (Transport, error) { opened++; return newFakeTransport(), nil }
	a := testAd(1)
	_, err := Start(context.Background(), Config{Scope: testScope(), Advertisement: a}, open)
	if err == nil || opened != 0 {
		t.Fatal("advertised wrong local address")
	}
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Start(ctx, Config{Scope: testScope(), Advertisement: a}, open)
	if !errors.Is(err, context.Canceled) || opened != 0 {
		t.Fatal("cancelled start opened socket")
	}
	_, err = Start(context.Background(), Config{Scope: testScope(), Advertisement: a}, nil)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("implicit native transport selected")
	}
}

func TestWindowsAncillaryCodecRejectsAmbiguity(t *testing.T) {
	for _, word := range []int{4, 8} {
		scope := testScope()
		info := winSendControl(scope, word)
		ttlBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(ttlBytes, 255)
		ttl := winControl(winIPRecvTTL, ttlBytes, word)
		oob := append(bytes.Clone(info), ttl...)
		dst, index, hops, ok := winReceiveControl(oob, word)
		if !ok || dst != scope.LocalAddress || index != scope.InterfaceIndex || hops != 255 {
			t.Fatal("valid ancillary metadata rejected")
		}
		for _, bad := range [][]byte{nil, info, oob[:len(oob)-5], append(bytes.Clone(oob), info...), make([]byte, 129)} {
			if _, _, _, ok := winReceiveControl(bad, word); ok {
				t.Fatal("malformed/duplicate metadata accepted")
			}
		}
		bad := bytes.Clone(oob)
		for i := 0; i < word; i++ {
			bad[i] = 255
		}
		if _, _, _, ok := winReceiveControl(bad, word); ok {
			t.Fatal("oversize ancillary length accepted")
		}
	}
}

func TestExpiryUnblocksBlockedWrite(t *testing.T) {
	clock, tr := newFakeClock(), newFakeTransport()
	tr.blockWrite = true
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), a, clock}, func(Scope) (Transport, error) { return tr, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	eventually(t, func() bool { return clock.active() == 2 })
	clock.advance(SessionLifetime)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expiry left blocked writer/reader")
	}
	if !errors.Is(s.Err(), ErrExpired) || clock.active() != 0 {
		t.Fatalf("expiry not joined: %v %d", s.Err(), clock.active())
	}
}
func TestParentCancellationImmediatelyHidesHints(t *testing.T) {
	clock, tr := newFakeClock(), newFakeTransport()
	a := testAd(99)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := Start(ctx, Config{testScope(), a, clock}, func(Scope) (Transport, error) { return tr, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tr.incoming <- testPacket(t, testAd(1))
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	id := s.Candidates()[0].ID
	cancel()
	if len(s.Candidates()) != 0 {
		t.Fatal("cancelled context exposed candidate")
	}
	if _, err := s.Lookup(id); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("cancelled context accepted selection")
	}
}
func TestSessionSendBudgetClosesQueryFlood(t *testing.T) {
	s, clock, tr := startFake(t)
	query := testPacket(t, testAd(1))
	query.Data = packQuery()
	for i := 0; i < MaxSentDatagrams; i++ {
		select {
		case <-s.Done():
			if !errors.Is(s.Err(), ErrSendBudget) {
				t.Fatal(s.Err())
			}
			if tr.count() != MaxSentDatagrams {
				t.Fatal("wrong send limit")
			}
			return
		default:
		}
		before := tr.count()
		clock.advance(time.Second)
		tr.incoming <- query
		eventually(t, func() bool { return tr.count() > before || s.Err() != nil })
	}
	t.Fatal("send flood did not stop")
}
func TestOwnAdvertisementsNeverBecomeCandidates(t *testing.T) {
	s, _, tr := startFake(t)
	tr.incoming <- testPacket(t, s.local)
	eventually(t, func() bool { return len(tr.incoming) == 0 })
	if len(s.Candidates()) != 0 {
		t.Fatal("self advertisement became candidate")
	}
}

func TestExpiredFactoryStartClosesWithoutAdvertising(t *testing.T) {
	clock, tr := newFakeClock(), newFakeTransport()
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), a, clock}, func(Scope) (Transport, error) { clock.advance(SessionLifetime); return tr, nil })
	if s != nil || !errors.Is(err, ErrExpired) || tr.count() != 0 {
		t.Fatal("expired startup emitted or returned session")
	}
	select {
	case <-tr.closed:
	default:
		t.Fatal("expired startup socket remained open")
	}
}

func FuzzWirePacketBounds(f *testing.F) {
	a := testAd(1)
	seed, _ := packAdvertisement(a)
	f.Add(seed)
	f.Add(packQuery())
	f.Add([]byte{0, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		p := Packet{Data: data, Source: netip.MustParseAddrPort("192.168.40.2:5353")}
		a, ttl, q, err := unpackPacket(p)
		if err == nil && !q {
			if a.Validate() != nil || a.Bootstrap.Addr() != p.Source.Addr() || ttl < 0 || ttl > CandidateTTL {
				t.Fatal("invalid data admitted")
			}
		}
	})
}
func FuzzWindowsAncillaryBounds(f *testing.F) {
	f.Add([]byte{}, uint8(8))
	f.Add(winSendControl(testScope(), 8), uint8(8))
	f.Fuzz(func(t *testing.T, data []byte, width uint8) {
		dst, index, ttl, ok := winReceiveControl(data, int(width))
		if ok && (!dst.Is4() || index <= 0 || ttl < 0 || ttl > 255) {
			t.Fatal("invalid ancillary admitted")
		}
	})
}

func TestCandidateIDsNeverReviveAcrossLocalSessions(t *testing.T) {
	first, _, tr1 := startFake(t)
	second, _, tr2 := startFake(t)
	a := testAd(1)
	tr1.incoming <- testPacket(t, a)
	tr2.incoming <- testPacket(t, a)
	eventually(t, func() bool { return len(first.Candidates()) == 1 && len(second.Candidates()) == 1 })
	old := first.Candidates()[0].ID
	if old == second.Candidates()[0].ID {
		t.Fatal("same remote hint revived ID in a new local session")
	}
	if _, err := second.Lookup(old); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("cross-session selection accepted")
	}
}

func TestPartialFactoryFailureClosesReturnedTransport(t *testing.T) {
	clock, tr := newFakeClock(), newFakeTransport()
	a := testAd(1)
	a.Bootstrap = netip.AddrPortFrom(testScope().LocalAddress, 42000)
	s, err := Start(context.Background(), Config{testScope(), a, clock}, func(Scope) (Transport, error) { return tr, ErrSocketUnavailable })
	if s != nil || !errors.Is(err, ErrSocketUnavailable) {
		t.Fatal("factory failure lost")
	}
	select {
	case <-tr.closed:
	default:
		t.Fatal("partial factory leaked returned transport")
	}
}
