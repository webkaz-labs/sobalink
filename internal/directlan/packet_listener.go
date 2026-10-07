package directlan

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

const maxPacketSize = MaxDatagram

type receivedPacket struct {
	data   []byte
	remote net.Addr
	flow   *packetFlow
}
type packetFlow struct {
	g          *runtimeGeneration
	work       *generationWork
	conn       ConnPacketConn
	mu         sync.Mutex
	deadlineMu sync.Mutex
	ready      chan struct{}
	identity   *transportorigin.Token
	done       chan struct{}
	stopped    bool
}

// packetListener adapts authenticated netstack per-peer UDP flows to PacketConn.
// It never creates an OS socket or dials an unobserved destination.
type packetListener struct {
	maxFlows                    int
	ln                          net.Listener
	mu                          sync.Mutex
	flows                       map[string]*packetFlow
	readDeadline, writeDeadline time.Time
	changed                     chan struct{}
	packets                     []receivedPacket
	packetLimit                 int
	packetChanged               chan struct{}
	closed                      chan struct{}
	once                        sync.Once
	wg                          sync.WaitGroup
	stop                        func() bool
}

func (n *Node) ListenPacket(ctx context.Context, port uint16) (net.PacketConn, error) {
	ln, e := n.ListenPeer(ctx, "udp", port)
	if e != nil {
		return nil, e
	}
	return newPacketListener(ctx, ln, n.cfg.FlowLimit, n.cfg.PacketQueueLimit), nil
}
func newPacketListener(ctx context.Context, ln net.Listener, flowLimit, queueLimit int) *packetListener {
	p := &packetListener{ln: ln, maxFlows: flowLimit, flows: make(map[string]*packetFlow), changed: make(chan struct{}), packetLimit: queueLimit, packetChanged: make(chan struct{}), closed: make(chan struct{})}
	if front, ok := ln.(*listener); ok {
		front.mu.Lock()
		front.packet = p
		front.mu.Unlock()
	}
	p.wg.Add(1)
	p.mu.Lock()
	p.stop = context.AfterFunc(ctx, func() { p.Close() })
	p.mu.Unlock()
	go p.accept()
	return p
}
func (p *packetListener) accept() {
	defer p.wg.Done()
	defer p.shutdown()
	for {
		c, e := p.ln.Accept()
		if e != nil {
			return
		}
		pc, ok := c.(ConnPacketConn)
		if !ok {
			c.Close()
			continue
		}
		key := c.RemoteAddr().String()
		p.mu.Lock()
		select {
		case <-p.closed:
			p.mu.Unlock()
			c.Close()
			return
		default:
		}
		if len(p.flows) >= p.maxFlows || p.flows[key] != nil {
			p.mu.Unlock()
			c.Close()
			continue
		}
		association := &packetFlow{conn: pc, ready: make(chan struct{}), identity: transportorigin.NewToken(), done: make(chan struct{})}
		if direct, ok := c.(*flow); ok {
			association.g = direct.g
			work, e := direct.g.acquireWork(nil, false)
			if e != nil {
				p.mu.Unlock()
				c.Close()
				continue
			}
			association.work = work
		}
		p.flows[key] = association
		p.mu.Unlock()
		p.applyWriteDeadline(association)
		close(association.ready)
		p.wg.Add(1)
		go p.readFlow(key, association)
	}
}
func (p *packetListener) readFlow(key string, flow *packetFlow) {
	defer p.wg.Done()
	defer close(flow.done)
	defer flow.work.finish()
	defer func() {
		closeErr := flow.conn.Close()
		if terminal, ok := flow.conn.(transportorigin.TerminalConnection); ok {
			if terminal.WaitClosed(context.Background()) != nil {
				select {}
			}
		} else if flow.g != nil || (closeErr != nil && !errors.Is(closeErr, net.ErrClosed)) {
			select {} // retain the association when physical cleanup is unproven
		}
		p.mu.Lock()
		if p.flows[key] == flow {
			delete(p.flows, key)
		}
		p.discardPacketsLocked(func(packet receivedPacket) bool { return packet.flow == flow })
		p.mu.Unlock()
	}()
	buf := make([]byte, maxPacketSize+1)
	for {
		flow.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		n, e := flow.conn.Read(buf)
		if e != nil {
			return
		}
		if n > maxPacketSize {
			continue
		}
		packet := receivedPacket{data: append([]byte(nil), buf[:n]...), remote: flow.conn.RemoteAddr(), flow: flow}
		p.mu.Lock()
		admit := func() bool {
			if p.flows[key] != flow || flow.stopped || len(p.packets) >= p.packetLimit {
				return false
			}
			p.packets = append(p.packets, packet)
			close(p.packetChanged)
			p.packetChanged = make(chan struct{})
			return true
		}
		if flow.g != nil {
			flow.g.admit(admit)
		} else {
			admit()
		}
		p.mu.Unlock()
	}
}

// packetAssociation is an exact old association capability. String remains
// the stable machine address, but a delayed WriteTo cannot resolve a new flow
// from that string after retirement.
type packetAssociation struct {
	address  net.Addr
	listener *packetListener
	flow     *packetFlow
}

func (a *packetAssociation) Network() string                             { return a.address.Network() }
func (a *packetAssociation) String() string                              { return a.address.String() }
func (a *packetAssociation) AssociationIdentity() *transportorigin.Token { return a.flow.identity }
func (a *packetAssociation) TransportOrigin() transportorigin.Origin {
	if a.flow.g != nil {
		return a.flow.g.origin
	}
	return nil
}
func (a *packetAssociation) PeerIdentity() (string, bool) {
	if peer, ok := a.flow.conn.(interface{ PeerIdentity() (string, bool) }); ok {
		return peer.PeerIdentity()
	}
	return "", false
}
func (a *packetAssociation) Close() error {
	p := a.listener
	p.mu.Lock()
	if p.flows[a.address.String()] == a.flow {
		a.flow.stopped = true
	}
	p.discardPacketsLocked(func(packet receivedPacket) bool { return packet.flow == a.flow })
	p.mu.Unlock()
	return a.flow.conn.Close()
}
func (a *packetAssociation) WaitClosed(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.flow.done:
		return nil
	}
}

var _ transportorigin.PacketAssociation = (*packetAssociation)(nil)

func (p *packetListener) discardPacketsLocked(discard func(receivedPacket) bool) {
	kept := p.packets[:0]
	for _, packet := range p.packets {
		if !discard(packet) {
			kept = append(kept, packet)
		}
	}
	clear(p.packets[len(kept):])
	p.packets = kept
	close(p.packetChanged)
	p.packetChanged = make(chan struct{})
}
func (p *packetListener) retireGeneration(g *runtimeGeneration) {
	p.mu.Lock()
	var retired []*packetFlow
	for _, flow := range p.flows {
		if flow.g == g {
			flow.stopped = true
			retired = append(retired, flow)
		}
	}
	p.discardPacketsLocked(func(packet receivedPacket) bool { return packet.flow.g == g })
	p.mu.Unlock()
	for _, flow := range retired {
		flow.conn.Close()
	}
}
func (p *packetListener) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		p.mu.Lock()
		deadline, changed, packetsChanged := p.readDeadline, p.changed, p.packetChanged
		select {
		case <-p.closed:
			p.mu.Unlock()
			return 0, nil, net.ErrClosed
		default:
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			p.mu.Unlock()
			return 0, nil, os.ErrDeadlineExceeded
		}
		if len(p.packets) != 0 {
			packet := p.packets[0]
			p.packets[0] = receivedPacket{}
			p.packets = p.packets[1:]
			active := p.flows[packet.remote.String()] == packet.flow && !packet.flow.stopped
			var work *generationWork
			if active && packet.flow.g != nil {
				var e error
				work, e = packet.flow.g.acquireWork(nil, false)
				active = e == nil
			}
			p.mu.Unlock()
			if !active {
				continue
			}
			if v, ok := packet.flow.conn.(interface{ Valid() bool }); ok && !v.Valid() {
				work.finish()
				continue
			}
			n := copy(b, packet.data)
			remote := &packetAssociation{address: packet.remote, listener: p, flow: packet.flow}
			defer work.finish()
			return n, remote, nil
		}
		p.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		select {
		case <-p.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-changed:
		case <-packetsChanged:
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
func (p *packetListener) WriteTo(b []byte, a net.Addr) (int, error) {
	if a == nil || len(b) > maxPacketSize {
		return 0, errors.New("invalid UDP destination or oversized datagram")
	}
	association, ok := a.(*packetAssociation)
	if !ok || association.listener != p {
		return 0, ErrUntrusted
	}
	p.mu.Lock()
	flow := p.flows[a.String()]
	if flow != association.flow || flow.stopped {
		flow = nil
	}
	p.mu.Unlock()
	select {
	case <-p.closed:
		return 0, net.ErrClosed
	default:
	}
	if flow == nil {
		return 0, ErrUntrusted
	}
	var work *generationWork
	var stopped <-chan struct{}
	if flow.g != nil {
		var e error
		work, e = flow.g.acquireWork(nil, false)
		if e != nil {
			return 0, net.ErrClosed
		}
		stopped = flow.g.stop
	}
	defer work.finish()
	select {
	case <-flow.ready:
	case <-stopped:
		return 0, net.ErrClosed
	case <-p.closed:
		return 0, net.ErrClosed
	}
	if v, ok := flow.conn.(interface{ Valid() bool }); ok && !v.Valid() {
		return 0, ErrUntrusted
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	n, e := flow.conn.Write(b)
	if e == nil {
		flow.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
	return n, e
}
func (p *packetListener) shutdown() {
	p.once.Do(func() {
		close(p.closed)
		p.ln.Close()
		p.mu.Lock()
		var cs []ConnPacketConn
		for _, f := range p.flows {
			cs = append(cs, f.conn)
		}
		p.discardPacketsLocked(func(receivedPacket) bool { return true })
		p.mu.Unlock()
		for _, c := range cs {
			c.Close()
		}
	})
}
func (p *packetListener) Close() error {
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	if stop != nil {
		stop()
	}
	p.shutdown()
	p.wg.Wait()
	return nil
}
func (p *packetListener) LocalAddr() net.Addr { return p.ln.Addr() }
func (p *packetListener) deadlineFlowsLocked() []*packetFlow {
	out := make([]*packetFlow, 0, len(p.flows))
	for _, flow := range p.flows {
		out = append(out, flow)
	}
	return out
}
func (p *packetListener) applyWriteDeadline(flow *packetFlow) {
	flow.deadlineMu.Lock()
	defer flow.deadlineMu.Unlock()
	p.mu.Lock()
	deadline := p.writeDeadline
	p.mu.Unlock()
	flow.conn.SetWriteDeadline(deadline)
}
func (p *packetListener) SetDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDeadline = t
	p.writeDeadline = t
	close(p.changed)
	p.changed = make(chan struct{})
	flows := p.deadlineFlowsLocked()
	p.mu.Unlock()
	for _, flow := range flows {
		p.applyWriteDeadline(flow)
	}
	return nil
}
func (p *packetListener) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDeadline = t
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	return nil
}
func (p *packetListener) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	p.writeDeadline = t
	flows := p.deadlineFlowsLocked()
	p.mu.Unlock()
	for _, flow := range flows {
		p.applyWriteDeadline(flow)
	}
	return nil
}

var _ net.PacketConn = (*packetListener)(nil)
