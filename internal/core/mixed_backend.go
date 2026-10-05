package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

// mixedBackend never joins unrelated transport identities by names or addresses.
// Every inbound address maps to an actual worker plus its authenticated source.
// Backend grants remain checked by that worker and parent resource checks.
type mixedBackend struct {
	ctx              context.Context
	mu               sync.Mutex
	self             netip.Addr
	nodes            map[string]NetworkBackend
	order            []string
	bindings         []connectionroute.Binding
	sources          map[netip.AddrPort]mixedSource
	scopes           map[string][]backendworker.TCPPolicy
	cached           map[string]identity.State
	next             uint16
	closed           bool
	sourceLimit      int
	packetQueueLimit int
	workerLimits     backendworker.Limits
}
type mixedSource struct {
	backend           string
	remote            netip.AddrPort
	identity, logical string
}
type mixedPeerRoute struct {
	backend, id string
	address     netip.Addr
	peer        policy.Peer
}

func mixedID(backend, id string) string {
	sum := sha256.Sum256([]byte(backend + "\x00" + id))
	return "mixed:" + hex.EncodeToString(sum[:])
}
func mixedIP(id string) netip.Addr {
	sum := sha256.Sum256([]byte("sobalink.mixed-overlay.v1\x00" + id))
	var raw [16]byte
	copy(raw[:], sum[:16])
	copy(raw[:6], []byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0})
	return netip.AddrFrom16(raw)
}
func (n *mixedBackend) logical(backend, id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, b := range n.bindings {
		for _, v := range b.Identities {
			if v.Backend == backend && v.ID == id {
				return b.PeerID
			}
		}
	}
	return mixedID(backend, id)
}
func (n *mixedBackend) Start() error {
	for _, name := range n.order {
		if e := n.nodes[name].Start(); e != nil {
			_ = n.Close()
			return e
		}
	}
	return nil
}
func (n *mixedBackend) routeSnapshot(ctx context.Context) (map[string][]mixedPeerRoute, map[string]identity.State, error) {
	out := map[string][]mixedPeerRoute{}
	states := map[string]identity.State{}
	for _, name := range n.order {
		state, e := n.nodes[name].State(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if !errors.Is(e, net.ErrClosed) && !errors.Is(e, backendworker.ErrClosed) && !errors.Is(e, connectionroute.ErrUnavailable) {
				return nil, nil, e
			}
			n.mu.Lock()
			state = n.cached[name]
			n.mu.Unlock()
			state.Snapshot.Running = false
			state.Backend = "unavailable"
		} else {
			n.mu.Lock()
			if n.cached == nil {
				n.cached = map[string]identity.State{}
			}
			n.cached[name] = state
			n.mu.Unlock()
		}
		states[name] = state
		for _, p := range state.Snapshot.Peers {
			if len(p.IPs) == 0 {
				continue
			}
			id := n.logical(name, p.ID)
			out[id] = append(out[id], mixedPeerRoute{name, p.ID, p.IPs[0], p})
		}
	}
	return out, states, nil
}
func (n *mixedBackend) State(ctx context.Context) (identity.State, error) {
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil {
		return identity.State{}, e
	}
	out := identity.State{Backend: "ready", IPs: []netip.Addr{n.self}, Snapshot: policy.Snapshot{Running: false}}
	for _, s := range states {
		out.Snapshot.Running = out.Snapshot.Running || s.Snapshot.Running
		out.ReservedPorts = append(out.ReservedPorts, s.ReservedPorts...)
		if out.AuthURL == "" {
			out.AuthURL = s.AuthURL
		}
	}
	if !out.Snapshot.Running {
		out.Backend = "starting"
	}
	for id, rs := range routes {
		p := policy.Peer{Expired: n.bindingDenied(id, routes, states), ID: id, DNSName: id + ".mixed.sobalink", IPs: []netip.Addr{mixedIP(id)}}
		n.mu.Lock()
		for alias, source := range n.sources {
			if source.logical == id && alias.Addr() != p.IPs[0] {
				p.IPs = append(p.IPs, alias.Addr())
			}
		}
		n.mu.Unlock()
		for _, r := range rs {
			p.Expired = p.Expired || r.peer.Expired
			p.Online = p.Online || r.peer.Online
		}
		out.Snapshot.Peers = append(out.Snapshot.Peers, p)
	}
	sort.Slice(out.Snapshot.Peers, func(i, j int) bool { return out.Snapshot.Peers[i].ID < out.Snapshot.Peers[j].ID })
	return out, nil
}
func (n *mixedBackend) Login(ctx context.Context) error {
	if node := n.nodes["tailnet"]; node != nil {
		return node.Login(ctx)
	}
	return errors.New("Tailnet is not enabled")
}
func (n *mixedBackend) Logout(context.Context) error {
	return errors.New("stop mixed mode and explicitly review Tailnet logout")
}
func (n *mixedBackend) DialIP(ctx context.Context, network string, ap netip.AddrPort) (net.Conn, error) {
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	for id, rs := range routes {
		if mixedIP(id) != ap.Addr() {
			continue
		}
		if n.bindingDenied(id, routes, states) {
			return nil, connectionroute.ErrDenied
		}
		for _, r := range rs {
			if r.peer.Expired {
				return nil, connectionroute.ErrDenied
			}
		}
		for _, r := range rs {
			if !states[r.backend].Snapshot.Running {
				continue
			}
			conn, e := n.nodes[r.backend].DialIP(ctx, network, netip.AddrPortFrom(r.address, ap.Port()))
			if e != nil {
				return nil, e
			}
			observed, e := observedBackendPeer(ctx, n.nodes[r.backend], conn)
			if e != nil || observed != r.id {
				_ = conn.Close()
				return nil, connectionroute.ErrDenied
			}
			return conn, nil
		}
		return nil, connectionroute.ErrUnavailable
	}
	return nil, connectionroute.ErrDenied
}
func (n *mixedBackend) Listen(network, address string) (net.Listener, error) {
	ap, e := netip.ParseAddrPort(address)
	if e != nil || network != "tcp" || ap.Addr() != n.self || ap.Port() == 0 {
		return nil, connectionroute.ErrDenied
	}
	_, states, e := n.routeSnapshot(n.ctx)
	if e != nil {
		return nil, e
	}
	l := &mixedListener{owner: n, addr: net.TCPAddrFromAddrPort(ap), accepts: make(chan net.Conn, 16), done: make(chan struct{})}
	for _, name := range n.order {
		state := states[name]
		if !state.Snapshot.Running || len(state.IPs) == 0 {
			continue
		}
		if e := l.attach(name, network, ap.Port(), state); e != nil {
			_ = l.Close()
			return nil, e
		}
	}
	l.wg.Add(1)
	go l.watch(network, ap.Port())

	return l, nil
}
func (n *mixedBackend) WhoIs(ctx context.Context, remote netip.AddrPort) (string, error) {
	n.mu.Lock()
	source, ok := n.sources[remote]
	closed := n.closed
	n.mu.Unlock()
	if !ok || closed {
		return "", connectionroute.ErrDenied
	}
	actual, e := n.nodes[source.backend].WhoIs(ctx, source.remote)
	if e != nil || actual != source.identity {
		return "", connectionroute.ErrDenied
	}
	if n.logical(source.backend, actual) != source.logical {
		return "", connectionroute.ErrDenied
	}
	return source.logical, nil
}
func (n *mixedBackend) wrap(name string, c net.Conn) (net.Conn, error) {
	remote, e := netip.ParseAddrPort(c.RemoteAddr().String())
	if e != nil {
		return nil, e
	}
	identityCtx, identityCancel := context.WithTimeout(n.ctx, 3*time.Second)
	defer identityCancel()
	actual, e := n.nodes[name].WhoIs(identityCtx, remote)
	if e != nil {
		return nil, e
	}
	logical := n.logical(name, actual)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || len(n.sources) >= n.sourceBudget() {
		return nil, connectionroute.ErrCapacity
	}
	for i := 0; i < 65535; i++ {
		n.next++
		if n.next == 0 {
			n.next++
		}
		alias := netip.AddrPortFrom(mixedIP(logical), n.next)
		if _, exists := n.sources[alias]; exists {
			continue
		}
		n.sources[alias] = mixedSource{name, remote, actual, logical}
		return &mixedConn{Conn: c, owner: n, alias: alias}, nil
	}
	return nil, connectionroute.ErrCapacity
}
func (n *mixedBackend) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	clear(n.sources)
	n.mu.Unlock()
	var errs []error
	for _, node := range n.nodes {
		errs = append(errs, node.Close())
	}
	return errors.Join(errs...)
}

type mixedConn struct {
	net.Conn
	owner *mixedBackend
	alias netip.AddrPort
	once  sync.Once
	err   error
}

func (c *mixedConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.alias) }
func (c *mixedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.sources, c.alias)
		c.owner.mu.Unlock()
	})
	return c.err
}
func (c *mixedConn) CloseWrite() error {
	if h, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return h.CloseWrite()
	}
	return errors.New("transport does not support half-close")
}

type mixedListener struct {
	owner     *mixedBackend
	addr      net.Addr
	listeners []net.Listener
	accepts   chan net.Conn
	done      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
	mu        sync.Mutex
	attached  map[string]bool
	closed    bool
}

func (l *mixedListener) accept(name string, base net.Listener) {
	defer l.wg.Done()
	for {
		c, e := base.Accept()
		if e != nil {
			return
		}
		wrapped, e := l.owner.wrap(name, c)
		if e != nil {
			_ = c.Close()
			continue
		}
		select {
		case l.accepts <- wrapped:
		case <-l.done:
			_ = wrapped.Close()
			return
		default:
			_ = wrapped.Close()
		}
	}
}
func (l *mixedListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.accepts:
		select {
		case <-l.done:
			_ = c.Close()
			return nil, net.ErrClosed
		default:
		}
		return c, nil
	}
}
func (l *mixedListener) Addr() net.Addr { return l.addr }
func (l *mixedListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		l.closed = true
		listeners := append([]net.Listener(nil), l.listeners...)
		l.mu.Unlock()
		for _, base := range listeners {
			_ = base.Close()
		}
		l.wg.Wait()
		for {
			select {
			case c := <-l.accepts:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (l *mixedListener) attach(name, network string, port uint16, state identity.State) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	if l.attached == nil {
		l.attached = map[string]bool{}
	}
	if l.attached[name] {
		return nil
	}
	base, e := l.owner.nodes[name].Listen(network, netip.AddrPortFrom(state.IPs[0], port).String())
	if e != nil {
		return e
	}
	l.listeners = append(l.listeners, base)
	l.attached[name] = true
	l.wg.Add(1)
	go l.accept(name, base)
	return nil
}
func (l *mixedListener) watch(network string, port uint16) {
	defer l.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-l.owner.ctx.Done():
			return
		case <-tick.C:
		}
		_, states, e := l.owner.routeSnapshot(l.owner.ctx)
		if e != nil {
			continue
		}
		for _, name := range l.owner.order {
			s := states[name]
			if !s.Snapshot.Running || len(s.IPs) == 0 {
				continue
			}
			_ = l.attach(name, network, port, s)
		}
	}
}

func (n *mixedBackend) sourceBudget() int {
	if n.sourceLimit > 0 {
		return n.sourceLimit
	}
	return 1024
}

func (n *mixedBackend) bindingDenied(id string, routes map[string][]mixedPeerRoute, states map[string]identity.State) bool {
	n.mu.Lock()
	var required []connectionroute.TransportIdentity
	for _, b := range n.bindings {
		if b.PeerID == id {
			required = append(required, b.Identities...)
			break
		}
	}
	n.mu.Unlock()
	for _, claim := range required {
		state := states[claim.Backend]
		if state.Backend == "NeedsLogin" || state.Backend == "NeedsMachineAuth" {
			return true
		}
		if !state.Snapshot.Running {
			continue
		}
		found := false
		for _, r := range routes[id] {
			if r.backend == claim.Backend && r.id == claim.ID && !r.peer.Expired {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}
