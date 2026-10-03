package transport

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func liveController(initial Limits) (*Controller, func(Limits)) {
	var current atomic.Pointer[Limits]
	current.Store(&initial)
	return NewController(func() Limits { return *current.Load() }), func(next Limits) { current.Store(&next) }
}

func TestControllerTCPBudgetsAcrossPoliciesAndPeers(t *testing.T) {
	c := NewController(func() Limits { return Limits{TCPConnections: 3, TCPPerPolicy: 2, TCPPerPeer: 2} })
	var held []func()
	defer func() {
		for _, release := range held {
			release()
			release()
		}
	}()
	admit := func(policy, peer string, want bool) {
		t.Helper()
		release, ok := c.AdmitTCP(policy, peer)
		if ok {
			held = append(held, release)
		}
		if ok != want {
			t.Fatalf("admit %s/%s = %v, want %v", policy, peer, ok, want)
		}
	}
	admit("a", "p", true)
	admit("a", "p", true)
	admit("a", "q", false)
	admit("b", "p", false)
	admit("b", "q", true)
	admit("c", "r", false)
	if _, ok := c.AdmitTCPPeer("p"); ok {
		t.Fatal("late peer binding bypassed shared peer budget")
	}
	for _, release := range held {
		release()
		release()
	}
	if c.Usage() != (Usage{}) || len(c.policies) != 0 || len(c.peers) != 0 {
		t.Fatal("released accounting was retained")
	}
}

func TestControllerLiveTCPReductionDoesNotEvict(t *testing.T) {
	c, update := liveController(Limits{TCPConnections: 3, TCPPerPolicy: 3, TCPPerPeer: 3})
	var held []func()
	for range 3 {
		release, ok := c.AdmitTCP("policy", "peer")
		if !ok {
			t.Fatal("initial budget unavailable")
		}
		held = append(held, release)
	}
	update(Limits{TCPConnections: 1, TCPPerPolicy: 1, TCPPerPeer: 1})
	if c.Usage().TCPConnections != 3 {
		t.Fatal("lowering evicted existing reservations")
	}
	if _, ok := c.AdmitTCP("other", "other"); ok {
		t.Fatal("lowered aggregate budget was bypassed")
	}
	held[0]()
	held[1]()
	if _, ok := c.AdmitTCP("policy", "peer"); ok {
		t.Fatal("new flow admitted while budget remained full")
	}
	held[2]()
	update(Limits{TCPConnections: 1000, TCPPerPolicy: 1000, TCPPerPeer: 1000})
	held = nil
	for range 700 {
		release, ok := c.AdmitTCP("policy", "peer")
		if !ok {
			t.Fatal("old hard maximum still active", len(held))
		}
		held = append(held, release)
	}
	for _, release := range held {
		release()
	}
	if c.Usage() != (Usage{}) {
		t.Fatal("raised budget leaked")
	}
}

func TestControllerConcurrentAdmissionAndIdempotentRelease(t *testing.T) {
	c, update := liveController(Limits{TCPConnections: 17, TCPPerPolicy: 7, TCPPerPeer: 5})
	var admitted, workers sync.WaitGroup
	start, finish := make(chan struct{}), make(chan struct{})
	for i := range 200 {
		admitted.Add(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			release, ok := c.AdmitTCP(fmt.Sprint(i%3), fmt.Sprint(i%4))
			admitted.Done()
			<-finish
			if ok {
				var callers sync.WaitGroup
				for range 3 {
					callers.Add(1)
					go func() { defer callers.Done(); release() }()
				}
				callers.Wait()
			}
		}()
	}
	close(start)
	admitted.Wait()
	c.mu.Lock()
	if c.tcp > 17 {
		t.Error("aggregate oversubscribed", c.tcp)
	}
	for _, n := range c.policies {
		if n > 7 {
			t.Error("policy oversubscribed", n)
		}
	}
	for _, n := range c.peers {
		if n > 5 {
			t.Error("peer oversubscribed", n)
		}
	}
	c.mu.Unlock()
	update(Limits{TCPConnections: 1, TCPPerPolicy: 1, TCPPerPeer: 1})
	close(finish)
	workers.Wait()
	if c.Usage() != (Usage{}) || len(c.policies)+len(c.peers) != 0 {
		t.Fatal("concurrent release leaked or underflowed")
	}
}

func TestControllerVirtualTCPUsesSharedLivePolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, update := liveController(Limits{TCPConnections: 3, TCPPerPolicy: 2, TCPPerPeer: 3})
		var servers []*Server
		var listeners []*pipeListener
		for range 2 {
			l := &pipeListener{queue: make(chan net.Conn, 2), done: make(chan struct{})}
			server := startServer(t.Context(), l, l.Addr(), func(s *Server) {
				acceptConnections(s, l, func(net.Conn) { <-s.ctx.Done() }, TCPConfig{Controller: c, PolicyID: "shared", PeerID: "peer"})
			})
			servers = append(servers, server)
			listeners = append(listeners, l)
			defer closeServer(t, server)
		}
		a, aClosed := virtualStreamClient(t, listeners[0])
		defer a.Close()
		b, bClosed := virtualStreamClient(t, listeners[1])
		defer b.Close()
		synctest.Wait()
		update(Limits{TCPConnections: 1, TCPPerPolicy: 1, TCPPerPeer: 1})
		x, xClosed := virtualStreamClient(t, listeners[1])
		defer x.Close()
		synctest.Wait()
		assertChannelClosed(t, xClosed, "lowered budget admitted a new stream")
		assertChannelOpen(t, aClosed, "lowered budget killed an existing stream")
		assertChannelOpen(t, bClosed, "lowered budget killed an existing stream")
		update(Limits{TCPConnections: 4, TCPPerPolicy: 4, TCPPerPeer: 4})
		y, yClosed := virtualStreamClient(t, listeners[1])
		defer y.Close()
		synctest.Wait()
		assertChannelOpen(t, yClosed, "increased budget did not admit stream")
		for _, s := range servers {
			closeServer(t, s)
		}
		if c.Usage() != (Usage{}) {
			t.Fatal("listener shutdown leaked reservations")
		}
	})
}

func TestControllerUDPQueueIsLazyDynamicAndChargesEmptyPackets(t *testing.T) {
	const huge = 1<<53 - 1
	c, update := liveController(Limits{UDPQueuePackets: huge, UDPQueuedBytes: huge, UDPPolicyQueuedBytes: huge})
	ctx, cancel := context.WithCancel(t.Context())
	s := &udpSession{ctx: ctx, cancel: cancel, table: &udpTable{cfg: UDPConfig{Budget: c.NewUDPBudget()}}}
	defer s.stop()
	if s.queue != nil || s.ready != nil || c.Usage() != (Usage{}) {
		t.Fatal("unused queue allocated storage")
	}
	for range 100 {
		s.offer(nil)
	}
	if s.queuedPackets != 100 || c.Usage().UDPQueuedBytes != int64(100*packetStorage(0)) {
		t.Fatal("large or empty datagrams bypassed accounting", s.queuedPackets, c.Usage())
	}
	update(Limits{UDPQueuePackets: 1, UDPQueuedBytes: 1, UDPPolicyQueuedBytes: 1})
	s.offer([]byte("blocked"))
	if s.queuedPackets != 100 || s.ctx.Err() != nil {
		t.Fatal("reduction evicted data or admitted new data")
	}
	s.stop()
	s.stop()
	if c.Usage() != (Usage{}) {
		t.Fatal("queue stop leaked storage")
	}
	if err := normalizeUDPConfig(&UDPConfig{QueueSize: 1000, MaxSessions: 1000}); err != nil {
		t.Fatal("old fixed maximum still enforced", err)
	}
}

func TestControllerUDPSharedBytesAndInFlightRetention(t *testing.T) {
	unit := packetStorage(3)
	c, update := liveController(Limits{UDPQueuedBytes: int64(unit * 3), UDPPolicyQueuedBytes: int64(unit * 2)})
	budget := c.NewUDPBudget()
	var sessions []*udpSession
	for range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		s := &udpSession{ctx: ctx, cancel: cancel, table: &udpTable{cfg: UDPConfig{Budget: budget}}}
		sessions = append(sessions, s)
		defer s.stop()
		s.offer([]byte("one"))
	}
	if sessions[2].queuedPackets != 0 {
		t.Fatal("separate ports amplified one policy's storage")
	}
	packet, ok := sessions[0].takePacket()
	if !ok {
		t.Fatal("packet missing")
	}
	sessions[0].stop()
	if c.Usage().UDPQueuedBytes != int64(unit*2) {
		t.Fatal("in-flight bytes released before write finished")
	}
	update(Limits{UDPQueuedBytes: 1, UDPPolicyQueuedBytes: 1})
	sessions[0].table.releaseBytes(packetStorage(len(packet)))
	sessions[1].stop()
	if c.Usage() != (Usage{}) {
		t.Fatal("over-budget drain failed")
	}
}

func TestControllerUDPVirtualSessionsFollowRaisedAndLoweredBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, update := liveController(Limits{UDPSessions: 700, UDPPerPolicy: 700})
		budget := c.NewUDPBudget()
		local := &virtualPacketSocket{incoming: make(chan virtualDatagram), outgoing: make(chan virtualDatagram), closed: make(chan struct{})}
		server := startServer(t.Context(), local, local.LocalAddr(), func(s *Server) {
			table := &udpTable{sessions: make(map[netip.AddrPort]*udpSession), server: s, local: local, cfg: UDPConfig{Budget: budget, Target: "127.0.0.1:9", IdleTimeout: time.Minute, DialTimeout: time.Minute, WriteTimeout: time.Second}, dial: func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}
			table.readLocal()
		})
		defer closeServer(t, server)
		for p := 1; p <= 600; p++ {
			local.incoming <- virtualDatagram{data: []byte("x"), address: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(p))}
		}
		synctest.Wait()
		if c.Usage().UDPSessions != 600 {
			t.Fatal("old fixed session maximum remains", c.Usage())
		}
		update(Limits{UDPSessions: 1, UDPPerPolicy: 1})
		local.incoming <- virtualDatagram{data: []byte("x"), address: netip.MustParseAddrPort("127.0.0.1:5000")}
		synctest.Wait()
		if c.Usage().UDPSessions != 600 {
			t.Fatal("reduction evicted active sessions or admitted extra", c.Usage())
		}
		closeServer(t, server)
		if c.Usage() != (Usage{}) {
			t.Fatal("session shutdown leaked", c.Usage())
		}
	})
}

func TestControllerUDPConcurrentQueueDrainAndPolicyChanges(t *testing.T) {
	initial := Limits{UDPQueuePackets: 32, UDPQueuedBytes: 4096, UDPPolicyQueuedBytes: 4096}
	c, update := liveController(initial)
	ctx, cancel := context.WithCancel(t.Context())
	s := &udpSession{ctx: ctx, cancel: cancel, ready: make(chan struct{}, 1), table: &udpTable{cfg: UDPConfig{Budget: c.NewUDPBudget()}}}
	defer s.stop()
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			if packet, ok := s.takePacket(); ok {
				runtime.Gosched() // A concurrent stop must retain this in-flight reservation.
				s.table.releaseBytes(packetStorage(len(packet)))
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-s.ready:
			}
		}
	}()
	var producers sync.WaitGroup
	for range 8 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for range 500 {
				s.offer([]byte("packet"))
			}
		}()
	}
	for range 100 {
		update(Limits{UDPQueuePackets: 1, UDPQueuedBytes: 1, UDPPolicyQueuedBytes: 1})
		runtime.Gosched()
		update(initial)
	}
	producers.Wait()
	s.stop()
	<-consumerDone
	if c.Usage() != (Usage{}) || s.table.queuedBytes != 0 {
		t.Fatal("concurrent queue drain leaked or underflowed", c.Usage())
	}
}

func TestControllerIndependentOwnersDoNotShareBudgets(t *testing.T) {
	a := NewController(func() Limits { return Limits{TCPConnections: 1, UDPSessions: 1} })
	b := NewController(func() Limits { return Limits{TCPConnections: 1, UDPSessions: 1} })
	for _, c := range []*Controller{a, b} {
		release, ok := c.AdmitTCP("same-policy", "same-peer")
		if !ok {
			t.Fatal("independent owner lost its capacity")
		}
		defer release()
		budget := c.NewUDPBudget()
		if !budget.reserveSession() {
			t.Fatal("independent owner lost UDP capacity")
		}
		defer budget.releaseSession()
		if _, ok := c.AdmitTCP("other", "other"); ok {
			t.Fatal("owner exceeded its own budget")
		}
		if c.NewUDPBudget().reserveSession() {
			t.Fatal("UDP policy bypassed owner budget")
		}
	}
}
