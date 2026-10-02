package lanlink

import (
	"context"
	"errors"
	"github.com/tailscale/tailcat"
	"net"
	"os"
	"sync"
	"time"
)

const maxPacketFlows = 128
const maxPacketQueue = 128
const maxPacketSize = tailcat.MaxUDPPayload // Pinned Tailcat inner IPv6 UDP payload limit.
type receivedPacket struct {
	data   []byte
	remote net.Addr
	flow   *packetFlow
}
type packetFlow struct {
	conn       ConnPacketConn
	mu         sync.Mutex
	deadlineMu sync.Mutex
	ready      chan struct{}
}

// packetListener adapts Tailcat's accepted per-peer UDP flows to PacketConn.
// It never creates an OS socket or dials an unobserved destination.
type packetListener struct {
	ln                          net.Listener
	mu                          sync.Mutex
	flows                       map[string]*packetFlow
	readDeadline, writeDeadline time.Time
	changed                     chan struct{}
	packets                     chan receivedPacket
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
	return newPacketListener(ctx, ln), nil
}
func newPacketListener(ctx context.Context, ln net.Listener) *packetListener {
	p := &packetListener{ln: ln, flows: make(map[string]*packetFlow), changed: make(chan struct{}), packets: make(chan receivedPacket, maxPacketQueue), closed: make(chan struct{})}
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
		if len(p.flows) >= maxPacketFlows || p.flows[key] != nil {
			p.mu.Unlock()
			c.Close()
			continue
		}
		flow := &packetFlow{conn: pc, ready: make(chan struct{})}
		p.flows[key] = flow
		p.mu.Unlock()
		p.applyWriteDeadline(flow)
		close(flow.ready)
		p.wg.Add(1)
		go p.readFlow(key, flow)
	}
}
func (p *packetListener) readFlow(key string, flow *packetFlow) {
	defer p.wg.Done()
	defer func() {
		flow.conn.Close()
		p.mu.Lock()
		if p.flows[key] == flow {
			delete(p.flows, key)
		}
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
		select {
		case p.packets <- packet:
		case <-p.closed:
			return
		default: /* Bounded memory: drop excess datagrams. */
		}
	}
}
func (p *packetListener) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		p.mu.Lock()
		deadline, changed := p.readDeadline, p.changed
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
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case packet := <-p.packets:
			if timer != nil {
				timer.Stop()
			}
			p.mu.Lock()
			active := p.flows[packet.remote.String()] == packet.flow
			p.mu.Unlock()
			if !active {
				continue
			}
			if v, ok := packet.flow.conn.(interface{ Valid() bool }); ok && !v.Valid() {
				continue
			}
			return copy(b, packet.data), packet.remote, nil
		}
	}
}
func (p *packetListener) WriteTo(b []byte, a net.Addr) (int, error) {
	if a == nil || len(b) > maxPacketSize {
		return 0, errors.New("invalid UDP destination or oversized datagram")
	}
	p.mu.Lock()
	flow := p.flows[a.String()]
	p.mu.Unlock()
	select {
	case <-p.closed:
		return 0, net.ErrClosed
	default:
	}
	if flow == nil {
		return 0, ErrUntrusted
	}
	select {
	case <-flow.ready:
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
