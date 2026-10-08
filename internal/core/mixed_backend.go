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
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// mixedBackend never joins unrelated transport identities by names or addresses.
// Every inbound address maps to an actual worker plus its authenticated source.
// Backend grants remain checked by that worker and parent resource checks.
type mixedBackend struct {
	closeOnce        sync.Once
	closeErr         error
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
	tcpPending       map[*mixedConn]struct{}
	closed           bool
	sourceLimit      int
	packetQueueLimit int
	workerLimits     backendworker.Limits
}
type mixedSource struct {
	backend           string
	remote            netip.AddrPort
	identity, logical string
	capturedPeer      interface{ PeerIdentity() (string, bool) }
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
	started := 0
	for _, name := range n.order {
		if e := n.ctx.Err(); e != nil {
			_ = n.Close()
			return e
		}
		if e := n.nodes[name].Start(); e != nil {
			if mixedStartupUnavailable(e) && n.ctx.Err() == nil {
				continue
			}
			_ = n.Close()
			return e
		}
		started++
	}
	if started == 0 {
		_ = n.Close()
		return connectionroute.ErrUnavailable
	}
	return nil
}
func (n *mixedBackend) routeSnapshot(ctx context.Context) (map[string][]mixedPeerRoute, map[string]identity.State, error) {
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return nil, nil, net.ErrClosed
	}
	out := map[string][]mixedPeerRoute{}
	states := map[string]identity.State{}
	for _, name := range n.order {
		state, e := n.nodes[name].State(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if !mixedUnavailable(e) {
				return nil, nil, e
			}
			n.mu.Lock()
			state = n.cached[name]
			n.mu.Unlock()
			state.Snapshot.Running = false
			if mixedReadiness(state) != mixedAuthRequired {
				state.Backend = "unavailable"
			}
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
	n.mu.Lock()
	closed = n.closed
	n.mu.Unlock()
	if closed {
		return nil, nil, net.ErrClosed
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
	attempted := map[string]bool{}
	// Each retry is a fresh unopened connection. Re-read identity/revocation state
	// after a positive availability race; never reuse a returned connection.
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		routes, states, e := n.routeSnapshot(ctx)
		if e != nil {
			return nil, e
		}
		var id string
		for candidate := range routes {
			if mixedIP(candidate) == ap.Addr() {
				id = candidate
				break
			}
		}
		if id == "" {
			return nil, connectionroute.ErrDenied
		}
		if e := n.routeReadiness(id, routes, states); e != nil {
			return nil, e
		}
		var selected *mixedPeerRoute
		for _, r := range routes[id] {
			if attempted[r.backend] || mixedReadiness(states[r.backend]) == mixedAbsent {
				continue
			}
			candidate := r
			selected = &candidate
			break
		}
		if selected == nil {
			return nil, connectionroute.ErrUnavailable
		}
		r := *selected
		attempted[r.backend] = true
		conn, e := n.nodes[r.backend].DialIP(ctx, network, netip.AddrPortFrom(r.address, ap.Port()))
		if ctx.Err() != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, ctx.Err()
		}
		if e != nil {
			if conn != nil {
				_ = conn.Close()
				return nil, e
			}
			if mixedUnavailable(e) && !transportorigin.Selected(ctx) {
				continue
			}
			return nil, e
		}
		if conn == nil {
			return nil, errMixedReadinessUnknown
		}
		observed, e := observedBackendPeer(ctx, n.nodes[r.backend], conn)
		if e != nil || observed != r.id {
			_ = conn.Close()
			return nil, connectionroute.ErrDenied
		}
		return conn, nil
	}
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
	l := &mixedListener{owner: n, addr: net.TCPAddrFromAddrPort(ap), owned: make(map[*mixedConn]*mixedAccept), wake: make(chan struct{}), done: make(chan struct{})}
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
	if peer, ok := source.capturedPeer.(*mixedTCPPeer); ok {
		return peer.conn.AuthenticatedPeer(ctx)
	}
	var actual string
	var e error
	if source.capturedPeer != nil {
		var valid bool
		actual, valid = source.capturedPeer.PeerIdentity()
		if !valid {
			return "", connectionroute.ErrDenied
		}
	} else {
		actual, e = n.nodes[source.backend].WhoIs(ctx, source.remote)
	}
	if e != nil || actual != source.identity {
		return "", connectionroute.ErrDenied
	}
	if n.logical(source.backend, actual) != source.logical {
		return "", connectionroute.ErrDenied
	}
	return source.logical, nil
}

// wrap consumes c on every outcome. Failed admission joins the same physical
// cleanup as a successful wrapper before releasing its source reservation.
func (n *mixedBackend) wrap(name string, c net.Conn) (_ net.Conn, err error) {
	if c == nil {
		return nil, connectionroute.ErrDenied
	}
	var origin transportorigin.Origin
	if carrier, ok := c.(transportorigin.Carrier); ok {
		origin = carrier.TransportOrigin()
	}
	wrapped := &mixedConn{raw: c, owner: n, origin: origin, ctx: n.ctx, borrows: 1, changed: make(chan struct{}), stop: make(chan struct{}), physicalDone: make(chan struct{}), done: make(chan struct{})}
	n.mu.Lock()
	if n.closed || len(n.sources) >= n.sourceBudget() {
		n.mu.Unlock()
		closeMixedUnadmitted(c, origin)
		return nil, connectionroute.ErrCapacity
	}
	if n.tcpPending == nil {
		n.tcpPending = make(map[*mixedConn]struct{})
	}
	n.tcpPending[wrapped] = struct{}{}
	n.mu.Unlock()
	var lease transportorigin.Lease
	if origin != nil {
		if origin.Identity() == nil {
			err = transportorigin.ErrMissingOrigin
		} else {
			lease, err = origin.Acquire(n.ctx)
		}
	}
	wrapped.lease = lease
	if lease != nil {
		wrapped.ctx = lease.Context()
	}
	go wrapped.own()
	defer func() {
		wrapped.returned()
		if err != nil {
			_ = wrapped.Close()
			_ = wrapped.WaitClosed(context.Background())
		}
	}()
	if err != nil {
		return nil, err
	}
	if origin != nil {
		if _, ok := c.(interface{ PeerIdentity() (string, bool) }); !ok {
			return nil, transportorigin.ErrMissingOrigin
		}
	}
	remote, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		return nil, err
	}
	local := c.LocalAddr()
	identityCtx, identityCancel := context.WithTimeout(wrapped.ctx, 3*time.Second)
	defer identityCancel()
	actual, err := mixedTCPIdentity(identityCtx, n.nodes[name], c, origin)
	if err != nil {
		return nil, err
	}
	logical := n.logical(name, actual)
	if err := n.admitMixedRoute(identityCtx, logical); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	wrapped.mu.Lock()
	defer wrapped.mu.Unlock()
	if n.closed || !wrapped.openLocked() || identityCtx.Err() != nil {
		return nil, net.ErrClosed
	}
	var alias netip.AddrPort
	for i := 0; i < 65535; i++ {
		n.next++
		if n.next == 0 {
			n.next++
		}
		candidate := netip.AddrPortFrom(mixedIP(logical), n.next)
		if _, exists := n.sources[candidate]; !exists {
			alias = candidate
			break
		}
	}
	if !alias.IsValid() {
		return nil, connectionroute.ErrCapacity
	}
	var publication transportorigin.Lease
	if origin != nil {
		publication, err = origin.AcquirePublication(identityCtx)
		if err != nil {
			return nil, err
		}
		defer publication.Release()
	}
	wrapped.alias, wrapped.local = alias, local
	wrapped.backend = name
	wrapped.identity, wrapped.logical = actual, logical
	wrapped.peer = &mixedTCPPeer{conn: wrapped}
	wrapped.published = true
	delete(n.tcpPending, wrapped)
	n.sources[alias] = mixedSource{backend: name, remote: remote, identity: actual, logical: logical, capturedPeer: wrapped.peer}
	return wrapped, nil
}
func (n *mixedBackend) Close() error {
	n.closeOnce.Do(func() {
		n.mu.Lock()
		n.closed = true
		n.mu.Unlock()
		var errs []error
		for _, node := range n.nodes {
			errs = append(errs, node.Close())
		}
		n.closeErr = errors.Join(errs...)
	})
	return n.closeErr
}

type mixedListener struct {
	owner     *mixedBackend
	addr      net.Addr
	listeners []net.Listener
	pending   []*mixedAccept
	owned     map[*mixedConn]*mixedAccept
	wake      chan struct{}
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
		c, err := base.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if closed {
			var origin transportorigin.Origin
			if carrier, ok := c.(transportorigin.Carrier); ok {
				origin = carrier.TransportOrigin()
			}
			closeMixedUnadmitted(c, origin)
			return
		}
		wrapped, err := l.owner.wrap(name, c)
		if err != nil {
			continue // wrap has already joined the rejected endpoint
		}
		owned := wrapped.(*mixedConn)
		if !l.enqueue(owned) {
			_ = owned.Close()
			_ = owned.WaitClosed(context.Background())
		}
	}
}
func (l *mixedListener) Addr() net.Addr { return l.addr }
func (l *mixedListener) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		for _, entry := range l.owned {
			l.retireLocked(entry)
		}
		listeners := append([]net.Listener(nil), l.listeners...)
		l.signalLocked()
		l.mu.Unlock()
		for _, base := range listeners {
			_ = base.Close()
		}
		l.wg.Wait()
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

// Caller holds n.mu. Authenticating TCP endpoints consume the same finite
// source budget as published TCP/UDP aliases until real cleanup acknowledges.
func (n *mixedBackend) sourceBudget() int {
	limit := n.sourceLimit
	if limit <= 0 {
		limit = 1024
	}
	return limit - len(n.tcpPending)
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
