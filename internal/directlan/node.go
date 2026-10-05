package directlan

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tailscale/wireguard-go/device"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

const handshakeTimeout = 10 * time.Second

type peerState struct{ peer Peer }
type service struct {
	network string
	port    uint16
}
type wire struct {
	raw    net.Conn
	key    string
	peer   *peerState
	cancel context.CancelFunc
}

type pendingDial struct {
	key    string
	cancel context.CancelFunc
}

type Node struct {
	mu                        sync.Mutex
	cfg                       Config
	cert                      tls.Certificate
	peers                     map[string]*peerState
	invites                   map[string]pendingInvitation
	attempts                  map[string]context.CancelFunc
	wires                     map[*wire]struct{}
	dials                     map[*pendingDial]struct{}
	flows                     map[netip.AddrPort]map[*flow]struct{}
	listeners                 map[service]*listener
	fallback                  func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)
	underlay                  net.Listener
	started, closed, recovery bool
	ctx                       context.Context
	cancel                    context.CancelFunc
	stop                      func() bool
	wg                        sync.WaitGroup
	nextPort                  uint16
	tunnel                    *userspaceTunnel
	engine                    *device.Device
	bind                      *lanBind
	dispatchSlots             chan struct{}
	dispatchMu                sync.Mutex
	dispatchClosed            bool
	tcpIncoming               *tcpAdmissions
}

// NewNode validates all policy and saved state before doing any network I/O.
func NewNode(cfg Config) (*Node, error) {
	cfg = cfg.withDefaults()
	cfg.AllowedPrefixes = append([]netip.Prefix(nil), cfg.AllowedPrefixes...)
	cfg.Peers = append([]Peer(nil), cfg.Peers...)
	if e := cfg.Validate(); e != nil {
		return nil, e
	}
	cert, e := certificate(cfg.Identity, time.Now())
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{cfg: cfg, cert: cert, peers: map[string]*peerState{}, invites: map[string]pendingInvitation{}, attempts: map[string]context.CancelFunc{}, wires: map[*wire]struct{}{}, dials: map[*pendingDial]struct{}{}, flows: map[netip.AddrPort]map[*flow]struct{}{}, listeners: map[service]*listener{}, ctx: ctx, cancel: cancel, nextPort: 1024, dispatchSlots: make(chan struct{}, cfg.FlowLimit)}
	for _, p := range cfg.Peers {
		n.peers[p.Key] = &peerState{p}
	}
	return n, nil
}
func (n *Node) PublicKey() string        { return n.cfg.Identity.PublicKey() }
func (n *Node) OverlayAddr() netip.Addr  { a, _ := OverlayAddress(n.PublicKey()); return a }
func (n *Node) Endpoint() netip.AddrPort { return n.cfg.Listen }
func (n *Node) Peers() []Peer            { n.mu.Lock(); defer n.mu.Unlock(); return n.snapshotLocked() }
func (n *Node) snapshotLocked() []Peer {
	out := make([]Peer, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, p.peer)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (n *Node) readyLocked() error {
	if n.closed {
		return net.ErrClosed
	}
	if n.recovery {
		return ErrRecovery
	}
	if !n.started {
		return ErrUnavailable
	}
	return nil
}

// Start opens exactly the selected numeric TCP tunnel endpoint. Failure is
// returned without changing its address, touching firewall policy or fallback.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	if n.recovery {
		return ErrRecovery
	}
	if n.started {
		return nil
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	network := "tcp6"
	if n.cfg.Listen.Addr().Is4() {
		network = "tcp4"
	}
	ln, e := (&net.ListenConfig{}).Listen(ctx, network, n.cfg.Listen.String())
	if e != nil {
		return e
	}
	if e = n.startTunnelLocked(); e != nil {
		ln.Close()
		return e
	}
	n.underlay = ln
	n.started = true
	n.stop = context.AfterFunc(ctx, func() { n.Close() })
	n.wg.Add(1)
	go n.accept(ln)
	return nil
}
func (n *Node) accept(ln net.Listener) {
	defer n.wg.Done()
	for {
		raw, e := ln.Accept()
		if e != nil {
			return
		}
		ap, e := netip.ParseAddrPort(raw.RemoteAddr().String())
		if e != nil || !n.cfg.permits(ap, false) {
			raw.Close()
			continue
		}
		w := &wire{raw: raw}
		n.mu.Lock()
		if n.readyLocked() != nil || len(n.wires)+len(n.dials) >= n.cfg.FlowLimit {
			n.mu.Unlock()
			raw.Close()
			continue
		}
		n.wires[w] = struct{}{}
		n.wg.Add(1)
		n.mu.Unlock()
		go func() { defer n.wg.Done(); n.handle(w) }()
	}
}

type request struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Network   string `json:"network,omitempty"`
	Port      uint16 `json:"port,omitempty"`
	Token     string `json:"token,omitempty"`
	Peer      *Peer  `json:"peer,omitempty"`
}
type response struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Peer    *Peer  `json:"peer,omitempty"`
}

func (n *Node) handle(w *wire) {
	retained := false
	defer func() {
		if !retained {
			n.removeWire(w)
		}
	}()
	c := tls.Server(w.raw, tlsConfig(n.cert, "", true))
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	ctx, cancel := context.WithTimeout(n.ctx, handshakeTimeout)
	defer cancel()
	if c.HandshakeContext(ctx) != nil {
		return
	}
	key, e := certificateKey([][]byte{c.ConnectionState().PeerCertificates[0].Raw}, time.Now())
	if e != nil || key == n.PublicKey() {
		return
	}
	n.mu.Lock()
	if n.readyLocked() != nil {
		n.mu.Unlock()
		return
	}
	w.key = key
	w.peer = n.peers[key]
	n.mu.Unlock()
	var req request
	if readJSON(c, &req) != nil || req.Version != 1 {
		return
	}
	if req.Operation == "pair" {
		if req.Network != "" || req.Port != 0 || req.Peer == nil || req.Peer.Key != key {
			return
		}
		if n.acceptPair(ctx, w, req) != nil {
			writeJSON(c, response{Version: 1, Code: "pair_rejected"})
			return
		}
		own := Peer{Key: n.PublicKey(), Endpoint: n.cfg.Listen, TunnelKey: n.cfg.Identity.TunnelKey()}
		writeJSON(c, response{Version: 1, OK: true, Peer: &own})
		return
	}

	// TLS underlay is pairing/control only. Application data uses WireGuard netstack.
}

func validService(network string, port uint16) bool {
	return (network == "tcp" || network == "udp") && port != 0 && port != 54545
}

func (n *Node) removeWire(w *wire) {
	w.raw.Close()
	if w.cancel != nil {
		w.cancel()
	}
	n.mu.Lock()
	delete(n.wires, w)
	n.mu.Unlock()
}
func (n *Node) connect(ctx context.Context, p Peer, expected *peerState) (*tls.Conn, *wire, error) {
	bounded, cancel := context.WithTimeout(ctx, handshakeTimeout)
	n.mu.Lock()
	if e := n.readyLocked(); e != nil {
		n.mu.Unlock()
		cancel()
		return nil, nil, e
	}
	if expected != nil && n.peers[p.Key] != expected {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrUntrusted
	}
	if !n.cfg.permits(p.Endpoint, true) {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrPolicy
	}
	if len(n.wires)+len(n.dials) >= n.cfg.FlowLimit {
		n.mu.Unlock()
		cancel()
		return nil, nil, ErrCapacity
	}
	pending := &pendingDial{key: p.Key, cancel: cancel}
	n.dials[pending] = struct{}{}
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.dials, pending); n.mu.Unlock() }()
	// Bind to the user's selected interface address. No resolver or proxy can
	// influence this exact numeric peer-tunnel dial; no application address enters.
	d := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(n.cfg.Listen.Addr(), 0))}
	network := "tcp6"
	if p.Endpoint.Addr().Is4() {
		network = "tcp4"
	}
	raw, e := d.DialContext(bounded, network, p.Endpoint.String())
	if e != nil {
		cancel()
		return nil, nil, e
	}
	w := &wire{raw: raw, key: p.Key, peer: expected, cancel: cancel}
	n.mu.Lock()
	if n.readyLocked() != nil || bounded.Err() != nil || (expected != nil && n.peers[p.Key] != expected) || len(n.wires) >= n.cfg.FlowLimit {
		n.mu.Unlock()
		raw.Close()
		cancel()
		return nil, nil, ErrUntrusted
	}
	n.wires[w] = struct{}{}
	delete(n.dials, pending)
	n.mu.Unlock()
	stop := context.AfterFunc(bounded, func() { raw.Close() })
	c := tls.Client(raw, tlsConfig(n.cert, p.Key, false))
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	if e = c.HandshakeContext(bounded); e != nil {
		stop()
		n.removeWire(w)
		return nil, nil, e
	}
	// The request/reply deadline remains active until the caller finishes opening.
	// Caller cancellation is also watched by the opening operation itself.
	stop()
	return c, w, nil
}

type ConnPacketConn interface {
	net.Conn
	net.PacketConn
}

// PeerKey accepts only addresses of live authenticated flows. Synthesizing an
// overlay IP from a known public key is never enough to obtain authorization.
func (n *Node) PeerKey(remote net.Addr) (string, bool) {
	if remote == nil {
		return "", false
	}
	ap, e := netip.ParseAddrPort(remote.String())
	if e != nil {
		return "", false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	key := ""
	for f := range n.flows[ap] {
		if n.validFlowLocked(f) {
			if key != "" && key != f.w.key {
				return "", false
			}
			key = f.w.key
		}
	}
	return key, key != ""
}
func (n *Node) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	if f == nil {
		return nil, errors.New("scoped dispatcher required")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	if n.fallback != nil {
		return nil, errors.New("dispatcher already registered")
	}
	n.fallback = f
	var once sync.Once
	return func() { once.Do(func() { n.mu.Lock(); n.fallback = nil; n.mu.Unlock() }) }, nil
}
func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.dispatchMu.Lock()
	n.dispatchClosed = true
	n.dispatchMu.Unlock()
	n.cancel()
	if n.stop != nil {
		n.stop()
	}
	ln := n.underlay
	ws := make([]*wire, 0, len(n.wires))
	for w := range n.wires {
		ws = append(ws, w)
	}
	ls := make([]*listener, 0, len(n.listeners))
	for _, l := range n.listeners {
		ls = append(ls, l)
	}
	for p := range n.dials {
		p.cancel()
	}
	for _, cancel := range n.attempts {
		cancel()
	}
	n.invites = map[string]pendingInvitation{}
	n.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, w := range ws {
		n.removeWire(w)
	}
	for _, l := range ls {
		l.Close()
	}
	if n.engine != nil {
		n.engine.Close()
	}
	n.wg.Wait()
	return nil
}
func (n *Node) String() string { return fmt.Sprintf("directlan(%s)", n.cfg.Listen) }

// Ready reports local listener/engine admission state without claiming remote
// reachability. Failed persistence or engine admission is reported as recovery.
func (n *Node) Ready() error {
	n.mu.Lock()
	e := n.readyLocked()
	n.mu.Unlock()
	if e != nil {
		return e
	}
	return localAddressReady(n.cfg.Listen.Addr())
}
