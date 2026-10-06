package core

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

type mixedTestDatagram struct {
	data   []byte
	remote netip.AddrPort
	err    error
}

type mixedTestPacket struct {
	mu            sync.Mutex
	incoming      chan mixedTestDatagram
	outgoing      chan mixedTestDatagram
	done          chan struct{}
	once          sync.Once
	address       netip.AddrPort
	writeDeadline time.Time
	writeCalls    int
}

func (p *mixedTestPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case <-p.done:
		return 0, nil, net.ErrClosed
	case d := <-p.incoming:
		if d.err != nil {
			return 0, nil, d.err
		}
		return copy(b, d.data), net.UDPAddrFromAddrPort(d.remote), nil
	}
}
func (p *mixedTestPacket) WriteTo(b []byte, remote net.Addr) (int, error) {
	p.mu.Lock()
	p.writeCalls++
	deadline := p.writeDeadline
	p.mu.Unlock()
	select {
	case <-p.done:
		return 0, net.ErrClosed
	default:
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if len(b) > 65507 {
		return 0, errors.New("synthetic oversized datagram")
	}
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return 0, err
	}
	p.outgoing <- mixedTestDatagram{data: append([]byte(nil), b...), remote: ap}
	return len(b), nil
}
func (p *mixedTestPacket) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *mixedTestPacket) LocalAddr() net.Addr             { return net.UDPAddrFromAddrPort(p.address) }
func (p *mixedTestPacket) SetDeadline(t time.Time) error   { return p.SetWriteDeadline(t) }
func (p *mixedTestPacket) SetReadDeadline(time.Time) error { return nil }
func (p *mixedTestPacket) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeDeadline = t
	return nil
}

type mixedTestPacketNode struct {
	*pipeNode
	packetMu    sync.Mutex
	stateErr    error
	listenErr   error
	listenCalls int
	packets     []*mixedTestPacket
}

func (n *mixedTestPacketNode) State(ctx context.Context) (identity.State, error) {
	n.packetMu.Lock()
	err := n.stateErr
	n.packetMu.Unlock()
	if err != nil {
		return identity.State{}, err
	}
	return n.pipeNode.State(ctx)
}
func (n *mixedTestPacketNode) ListenPacket(network, address string) (net.PacketConn, error) {
	n.packetMu.Lock()
	defer n.packetMu.Unlock()
	n.listenCalls++
	if n.listenErr != nil {
		return nil, n.listenErr
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || network != "udp" || ap.Addr() != n.ip {
		return nil, connectionroute.ErrDenied
	}
	p := &mixedTestPacket{address: ap, incoming: make(chan mixedTestDatagram, 32), outgoing: make(chan mixedTestDatagram, 32), done: make(chan struct{})}
	n.packets = append(n.packets, p)
	return p, nil
}
func (n *mixedTestPacketNode) latest(t *testing.T) *mixedTestPacket {
	t.Helper()
	n.packetMu.Lock()
	defer n.packetMu.Unlock()
	if len(n.packets) == 0 {
		t.Fatal("backend has no packet listener")
	}
	return n.packets[len(n.packets)-1]
}

func newMixedPacketFixture(t *testing.T) (*mixedBackend, map[string]*mixedTestPacketNode) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &mixedBackend{ctx: ctx, self: mixedIP("synthetic-local"), nodes: map[string]NetworkBackend{}, order: []string{"lan", "tailnet"}, sources: map[netip.AddrPort]mixedSource{}, sourceLimit: 3, packetQueueLimit: 2}
	nodes := map[string]*mixedTestPacketNode{}
	for _, name := range n.order {
		ip, remote := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
		node := &mixedTestPacketNode{pipeNode: &pipeNode{ip: ip, who: map[netip.Addr]string{remote: "approved-" + name}, state: identity.State{Backend: "Running", IPs: []netip.Addr{ip}, Snapshot: policy.Snapshot{Running: true}}}}
		n.nodes[name], nodes[name] = node, node
	}
	return n, nodes
}
func openMixedPacketFixture(t *testing.T, n *mixedBackend) *mixedPacket {
	t.Helper()
	conn, err := n.ListenPacket("udp", netip.AddrPortFrom(n.self, 7000).String())
	if err != nil {
		t.Fatal(err)
	}
	p := conn.(*mixedPacket)
	p.SetPeerFilter(func(id string) bool {
		return id == mixedID("lan", "approved-lan") || id == mixedID("tailnet", "approved-tailnet")
	})
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func mixedPacketReceive(t *testing.T, p *mixedPacket, want []byte) net.Addr {
	t.Helper()
	if err := p.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, remote, err := p.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], want) {
		t.Fatalf("ReadFrom = %d, %v; expected payload length %d", n, err, len(want))
	}
	_ = p.SetReadDeadline(time.Time{})
	return remote
}
func mixedPacketSourceCount(p *mixedPacket) int {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	return len(p.owner.sources)
}
func mixedPacketSend(packet *mixedTestPacket, port uint16, data []byte) {
	packet.incoming <- mixedTestDatagram{remote: netip.AddrPortFrom(netip.MustParseAddr("100.64.0.2"), port), data: data}
}

func TestMixedPacketUnavailableBackendAndLateAttachment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		nodes["lan"].stateErr = backendworker.ErrClosed
		p := openMixedPacketFixture(t, n)
		healthy := nodes["tailnet"].latest(t)
		mixedPacketSend(healthy, 41000, []byte("healthy"))
		mixedPacketReceive(t, p, []byte("healthy"))
		nodes["lan"].packetMu.Lock()
		nodes["lan"].stateErr = nil
		nodes["lan"].packetMu.Unlock()
		time.Sleep(mixedPacketWatchInterval)
		synctest.Wait()
		late := nodes["lan"].latest(t)
		mixedPacketSend(late, 42000, nil)
		lostAlias := mixedPacketReceive(t, p, nil)
		late.incoming <- mixedTestDatagram{err: backendworker.ErrClosed}
		synctest.Wait()
		mixedPacketSend(healthy, 41000, []byte("still healthy"))
		mixedPacketReceive(t, p, []byte("still healthy"))
		if _, err := p.WriteTo([]byte("stale"), lostAlias); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("lost backend mapping stayed usable: %v", err)
		}
		time.Sleep(mixedPacketWatchInterval)
		synctest.Wait()
		replacement := nodes["lan"].latest(t)
		if replacement == late {
			t.Fatal("backend did not attach a fresh listener")
		}
		mixedPacketSend(replacement, 42000, []byte("new"))
		newAlias := mixedPacketReceive(t, p, []byte("new"))
		if newAlias.String() == lostAlias.String() {
			t.Fatal("backend replacement retargeted a stale alias")
		}
		time.Sleep(2 * mixedPacketWatchInterval)
		synctest.Wait()
		nodes["lan"].packetMu.Lock()
		calls := nodes["lan"].listenCalls
		nodes["lan"].packetMu.Unlock()
		if calls != 2 {
			t.Fatalf("ready backend listener recreated: %d attaches", calls)
		}
	})
}

func TestMixedPacketKnownUnavailableListenDoesNotStopHealthyBackend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		nodes["lan"].listenErr = connectionroute.ErrUnavailable
		p := openMixedPacketFixture(t, n)
		mixedPacketSend(nodes["tailnet"].latest(t), 43000, []byte("ready"))
		mixedPacketReceive(t, p, []byte("ready"))
	})
}

func TestMixedPacketUnknownAndAuthenticationFailuresAreTerminal(t *testing.T) {
	for _, phase := range []string{"listen", "read", "state", "authentication"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n, nodes := newMixedPacketFixture(t)
				failure := errors.New("synthetic unknown backend failure")
				if phase == "listen" {
					nodes["tailnet"].listenErr = failure
					conn, err := n.ListenPacket("udp", netip.AddrPortFrom(n.self, 7000).String())
					if conn != nil || !errors.Is(err, failure) {
						t.Fatalf("unknown listen error was hidden: %v", err)
					}
				} else {
					p := openMixedPacketFixture(t, n)
					if phase == "read" {
						nodes["tailnet"].latest(t).incoming <- mixedTestDatagram{err: failure}
					} else if phase == "state" {
						nodes["tailnet"].packetMu.Lock()
						nodes["tailnet"].stateErr = failure
						nodes["tailnet"].packetMu.Unlock()
						time.Sleep(mixedPacketWatchInterval)
					} else {
						nodes["tailnet"].mu.Lock()
						nodes["tailnet"].state.Backend = "NeedsLogin"
						nodes["tailnet"].mu.Unlock()
						failure = connectionroute.ErrDenied
						time.Sleep(mixedPacketWatchInterval)
					}
					synctest.Wait()
					if _, _, err := p.ReadFrom(make([]byte, 10)); !errors.Is(err, failure) {
						t.Fatalf("terminal %s error was hidden: %v", phase, err)
					}
				}
				select {
				case <-nodes["lan"].latest(t).done:
				default:
					t.Fatal("terminal failure left another listener running")
				}
			})
		})
	}
}

func TestMixedPacketRequiresApplicationApprovalBeforeAllocating(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		p := openMixedPacketFixture(t, n)
		packet := nodes["lan"].latest(t)
		for _, filter := range []func(string) bool{nil, func(string) bool { return false }} {
			p.SetPeerFilter(filter)
			for port := uint16(44000); port < 44010; port++ {
				mixedPacketSend(packet, port, []byte("unapproved"))
			}
			synctest.Wait()
			if count := mixedPacketSourceCount(p); count != 0 {
				t.Fatalf("unapproved peers consumed %d source handles", count)
			}
		}
		p.SetPeerFilter(func(id string) bool { return id == mixedID("lan", "approved-lan") })
		mixedPacketSend(packet, 44000, []byte("allowed"))
		mixedPacketReceive(t, p, []byte("allowed"))
		if count := mixedPacketSourceCount(p); count != 1 {
			t.Fatalf("approved alias count = %d", count)
		}
	})
}

func TestMixedPacketIdleRetirementPreservesLiveAliasesAndSelectedBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		n.sourceLimit = 2
		p := openMixedPacketFixture(t, n)
		packet := nodes["lan"].latest(t)
		mixedPacketSend(packet, 45000, []byte("old"))
		oldAlias := mixedPacketReceive(t, p, []byte("old"))
		mixedPacketSend(packet, 45001, []byte("live"))
		liveAlias := mixedPacketReceive(t, p, []byte("live"))
		mixedPacketSend(packet, 45002, []byte("over budget"))
		synctest.Wait()
		if count := mixedPacketSourceCount(p); count != 2 || len(p.in) != 0 {
			t.Fatal("selected source budget was exceeded")
		}
		time.Sleep(transport.DefaultUDPIdleTimeout / 2)
		if _, err := p.WriteTo([]byte("reply"), liveAlias); err != nil {
			t.Fatal(err)
		}
		<-packet.outgoing
		time.Sleep(transport.DefaultUDPIdleTimeout/2 + mixedPacketWatchInterval)
		synctest.Wait()
		if count := mixedPacketSourceCount(p); count != 1 {
			t.Fatalf("idle handles not released, or live mapping retired: %d", count)
		}
		if _, err := p.WriteTo([]byte("stale"), oldAlias); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("retired alias still routed: %v", err)
		}
		mixedPacketSend(packet, 45002, []byte("new"))
		newAlias := mixedPacketReceive(t, p, []byte("new"))
		if newAlias.String() == oldAlias.String() || newAlias.String() == liveAlias.String() {
			t.Fatal("admission reused an old or live alias")
		}
		mixedPacketSend(packet, 45001, []byte("same live"))
		if got := mixedPacketReceive(t, p, []byte("same live")); got.String() != liveAlias.String() {
			t.Fatal("active alias changed")
		}
		_ = p.Close()
		if count := mixedPacketSourceCount(p); count != 0 {
			t.Fatal("Close leaked source handles")
		}
	})
}

func TestMixedPacketQueueBoundRetainsDatagramBoundaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		p := openMixedPacketFixture(t, n)
		packet := nodes["lan"].latest(t)
		mixedPacketSend(packet, 46000, []byte("truncate"))
		mixedPacketSend(packet, 46001, nil)
		mixedPacketSend(packet, 46002, []byte("drop"))
		synctest.Wait()
		if len(p.in) != 2 || mixedPacketSourceCount(p) != 2 {
			t.Fatal("selected queue capacity did not bound queued packets and admission")
		}
		time.Sleep(transport.DefaultUDPIdleTimeout + mixedPacketWatchInterval)
		synctest.Wait()
		if mixedPacketSourceCount(p) != 2 {
			t.Fatal("queued packet aliases retired before delivery")
		}
		buf := make([]byte, 3)
		count, _, err := p.ReadFrom(buf)
		if err != nil || count != 3 || string(buf) != "tru" {
			t.Fatalf("truncated read = %q, %v", buf, err)
		}
		alias := mixedPacketReceive(t, p, nil)
		for _, payload := range [][]byte{nil, []byte("one"), bytes.Repeat([]byte{7}, 65507)} {
			written, err := p.WriteTo(payload, alias)
			if err != nil || written != len(payload) {
				t.Fatalf("WriteTo length %d: %d, %v", len(payload), written, err)
			}
			got := <-packet.outgoing
			if !bytes.Equal(got.data, payload) || got.remote.Port() != 46001 {
				t.Fatal("datagram content, boundary, or authenticated destination changed")
			}
		}
		if _, err := p.WriteTo(make([]byte, 65508), alias); err == nil {
			t.Fatal("oversized datagram was accepted")
		}
		if len(nodes["tailnet"].latest(t).outgoing) != 0 || len(packet.outgoing) != 0 {
			t.Fatal("oversized datagram was replayed or split")
		}
	})
}

func TestMixedPacketIdentityChangeCannotRetargetSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		p := openMixedPacketFixture(t, n)
		p.SetPeerFilter(func(string) bool { return true })
		packet := nodes["lan"].latest(t)
		mixedPacketSend(packet, 47000, []byte("before"))
		before := mixedPacketReceive(t, p, []byte("before"))
		nodes["lan"].mu.Lock()
		nodes["lan"].who[netip.MustParseAddr("100.64.0.2")] = "different-authenticated-peer"
		nodes["lan"].mu.Unlock()
		if _, err := p.WriteTo([]byte("old reply"), before); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("WhoIs was not rechecked: %v", err)
		}
		mixedPacketSend(packet, 47000, []byte("after"))
		after := mixedPacketReceive(t, p, []byte("after"))
		if before.String() == after.String() {
			t.Fatal("identity change retargeted a live alias")
		}
		if _, err := p.WriteTo([]byte("stale reply"), before); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("old identity alias survived replacement: %v", err)
		}
	})
}

func (n *mixedTestPacketNode) Close() error {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.packetMu.Lock()
	packets := append([]*mixedTestPacket(nil), n.packets...)
	n.packetMu.Unlock()
	for _, packet := range packets {
		_ = packet.Close()
	}
	return nil
}

func TestMixedPacketLateBackendInheritsWriteDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		nodes["lan"].state.Snapshot.Running = false
		nodes["lan"].state.Backend = "unavailable"
		p := openMixedPacketFixture(t, n)
		deadline := time.Now().Add(time.Minute)
		if err := p.SetWriteDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		nodes["lan"].mu.Lock()
		nodes["lan"].state.Snapshot.Running = true
		nodes["lan"].state.Backend = "Running"
		nodes["lan"].mu.Unlock()
		time.Sleep(mixedPacketWatchInterval)
		synctest.Wait()
		packet := nodes["lan"].latest(t)
		packet.mu.Lock()
		actual := packet.writeDeadline
		packet.mu.Unlock()
		if !actual.Equal(deadline) {
			t.Fatalf("late backend write deadline = %v, want %v", actual, deadline)
		}
		if err := p.SetReadDeadline(time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.ReadFrom(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read deadline not applied: %v", err)
		}
		if err := n.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(mixedPacketWatchInterval)
		synctest.Wait()
		if _, _, err := p.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed owner left a live listener: %v", err)
		}
	})
}

func TestMixedPacketAliasesAreScopedToTheirListener(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, nodes := newMixedPacketFixture(t)
		first := openMixedPacketFixture(t, n)
		mixedPacketSend(nodes["lan"].latest(t), 48000, []byte("first"))
		oldAlias := mixedPacketReceive(t, first, []byte("first"))
		conn, err := n.ListenPacket("udp", netip.AddrPortFrom(n.self, 7001).String())
		if err != nil {
			t.Fatal(err)
		}
		second := conn.(*mixedPacket)
		t.Cleanup(func() { _ = second.Close() })
		second.SetPeerFilter(func(string) bool { return true })
		if _, err := second.WriteTo([]byte("wrong listener"), oldAlias); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("another listener's alias accepted: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		mixedPacketSend(nodes["lan"].latest(t), 48000, []byte("second"))
		newAlias := mixedPacketReceive(t, second, []byte("second"))
		if newAlias.String() == oldAlias.String() {
			t.Fatal("listener close allowed stale alias reuse")
		}
		second.SetPeerFilter(nil)
		if mixedPacketSourceCount(second) != 0 {
			t.Fatal("removing application admission retained source handles")
		}
		if _, err := second.WriteTo([]byte("revoked"), newAlias); !errors.Is(err, connectionroute.ErrDenied) {
			t.Fatalf("revoked alias accepted: %v", err)
		}
	})
}
