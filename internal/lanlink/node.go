package lanlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/logtail"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

const PairingPort uint16 = 54545

type NodeConfig struct {
	Identity Identity
	Relay    TrustedRelay
	Trust    *Book
	Remotes  []RemotePeer
	// Persist atomically saves both snapshots to the protected state file. It must
	// not call back into Node or Book, and must return only after durable commit.
	Persist       func(Snapshot, []RemotePeer) error
	EmbeddedRelay bool
}
type remoteClient struct {
	remote  RemotePeer
	address netip.Addr
	client  *tailcat.Client
	startMu sync.Mutex
	started bool
	closed  bool
	runCtx  context.Context
	cancel  context.CancelFunc
}
type Node struct {
	mu         sync.Mutex
	pairMu     sync.Mutex
	listenMu   sync.Mutex
	admissions map[string]time.Time
	bootstrap  map[string]relayBootstrap
	revoked    map[string]uint64
	attempts   map[string]map[*pairAttempt]struct{}
	cfg        NodeConfig
	server     *tailcat.Server
	clients    map[string]*remoteClient
	closed     bool
	pairing    *PairingServer
	fallback   func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)
}

// NewNode validates configuration without opening any listener, generating keys
// or enrolling an account. Only one network backend may run in this process;
// switching to/from tsnet requires process restart (upstream global netns state).
func NewNode(cfg NodeConfig) (*Node, error) {
	if e := cfg.Identity.Validate(); e != nil {
		return nil, e
	}
	if e := cfg.Relay.Validate(); e != nil {
		return nil, e
	}
	if len(cfg.Remotes) > 128 {
		return nil, errors.New("too many remotes")
	}
	if cfg.Trust == nil {
		return nil, errors.New("trust book required")
	}
	if e := ValidateBuild(); e != nil {
		return nil, e
	}
	if e := validateEnvironment(os.Environ()); e != nil {
		return nil, e
	}
	n := &Node{cfg: cfg, clients: make(map[string]*remoteClient), admissions: make(map[string]time.Time), bootstrap: make(map[string]relayBootstrap), revoked: make(map[string]uint64), attempts: make(map[string]map[*pairAttempt]struct{})}
	usedKeys := map[string]bool{cfg.Identity.PublicKey(): true}
	for _, r := range cfg.Remotes {
		if _, exists := n.clients[r.Peer.Key]; exists {
			return nil, errors.New("duplicate remote")
		}
		ap, e := validateRemote(r.Offer(), cfg.Relay)
		if e != nil {
			return nil, e
		}
		if e := distinctRoleKeys(r, usedKeys); e != nil {
			return nil, e
		}
		if _, e := cfg.Trust.Epoch(r.Peer.Key); e != nil {
			return nil, errors.New("remote and public trust snapshots disagree")
		}
		n.clients[r.Peer.Key] = &remoteClient{remote: r, address: ap}
	}
	if len(cfg.Trust.Snapshot().Peers) != len(cfg.Remotes) {
		return nil, errors.New("remote and public trust snapshots disagree")
	}
	// Match the existing identity adapter's process-wide no-upload policy.
	logtail.Disable()
	n.server = &tailcat.Server{Key: cfg.Identity.Key, PresharedKey: cfg.Identity.PSK, Region: cfg.Relay.region(), Logf: logger.Discard, UDPIdleTimeout: 5 * time.Minute, AllowClient: n.allowClient}
	n.server.OnTCP = n.onTCP
	return n, nil
}

// Address is a secret capability, constructed without starting the engine.
func (n *Node) Address() tailcat.Addr {
	return (&tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: n.cfg.Identity.Key.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(n.cfg.Identity.Key), PresharedKey: n.cfg.Identity.PSK, Region: []*tailcfg.DERPRegion{n.cfg.Relay.region()}}).Addr()
}
func (n *Node) client(peer string) (*remoteClient, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	r := n.clients[peer]
	if r == nil {
		return nil, ErrUntrusted
	}
	if r.client == nil {
		r.client = &tailcat.Client{Server: r.remote.Address, Key: r.remote.ClientPrivate, Logf: logger.Discard}
		r.runCtx, r.cancel = context.WithCancel(context.Background())
	}
	return r, nil
}
func (n *Node) DialPeer(ctx context.Context, peer, network string, port uint16) (net.Conn, error) {
	if network == "udp" {
		return n.DialPacketPeer(ctx, peer, port)
	}
	if network != "tcp" || port == 0 || port == PairingPort {
		return nil, errors.New("supported service TCP/UDP port required")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	epoch, e := n.cfg.Trust.Epoch(peer)
	if e != nil {
		return nil, e
	}
	r, e := n.client(peer)
	if e != nil {
		return nil, e
	}
	client, e := r.open(ctx)
	if e != nil {
		return nil, e
	}
	c, e := client.DialTCP(ctx, netip.AddrPortFrom(r.address, port))
	if e != nil {
		return nil, e
	}
	return n.track(peer, epoch, c)
}
func (n *Node) DialPacketPeer(ctx context.Context, peer string, port uint16) (ConnPacketConn, error) {
	if port == 0 || port == PairingPort {
		return nil, errors.New("supported service UDP port required")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	epoch, e := n.cfg.Trust.Epoch(peer)
	if e != nil {
		return nil, e
	}
	r, e := n.client(peer)
	if e != nil {
		return nil, e
	}
	client, e := r.open(ctx)
	if e != nil {
		return nil, e
	}
	c, e := client.DialUDP(ctx, netip.AddrPortFrom(r.address, port))
	if e != nil {
		return nil, e
	}
	wrapped, e := n.track(peer, epoch, c)
	if e != nil {
		return nil, e
	}
	return wrapped.(ConnPacketConn), nil
}
func (n *Node) ListenPeer(ctx context.Context, network string, port uint16) (net.Listener, error) {
	if (network != "tcp" && network != "udp") || port == 0 || port == PairingPort {
		return nil, errors.New("supported service TCP/UDP port required")
	}
	n.listenMu.Lock()
	defer n.listenMu.Unlock()
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	ln, e := n.server.Listen(ctx, network, fmt.Sprintf(":%d", port))
	if e != nil {
		return nil, e
	}
	n.mu.Lock()
	closed = n.closed
	n.mu.Unlock()
	if closed {
		ln.Close()
		return nil, net.ErrClosed
	}
	wrapped := &peerListener{Listener: ln, node: n}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	wrapped.stop = stop
	return wrapped, nil
}
func (n *Node) resolveRole(role string) (string, uint64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.cfg.Trust
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, r := range n.clients {
		if r.remote.IncomingClientKey == role {
			a := b.peers[id]
			if a != nil {
				return id, a.epoch, true
			}
			break
		}
	}
	return "", 0, false
}
func (n *Node) canonicalPeer(role string) (string, bool) {
	id, _, ok := n.resolveRole(role)
	return id, ok
}
func (n *Node) resolveConnection(remote net.Addr) (string, uint64, bool) {
	k, ok := n.server.PeerKey(remote)
	if !ok {
		return "", 0, false
	}
	return n.resolveRole(keyString(k))
}
func (n *Node) PeerKey(remote net.Addr) (string, bool) {
	id, _, ok := n.resolveConnection(remote)
	return id, ok
}
func (n *Node) allowClient(k key.NodePublic) bool {
	role := keyString(k)
	if _, ok := n.canonicalPeer(role); ok {
		return true
	}
	if !n.cfg.Trust.anyPending() {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return false
	}
	if _, ok := n.admissions[role]; ok {
		return true
	}
	if len(n.admissions) >= 16 {
		return false
	}
	n.admissions[role] = time.Now().Add(2 * time.Minute)
	return true
}

// Revoke stops local traffic even if persisting the removal fails. Such a
// failure means durable revocation is unconfirmed; the caller must not restart
// from old state until a successful save and must surface the error.
func (n *Node) Revoke(peer string) error {
	n.pairMu.Lock()
	locked := true
	defer func() {
		if locked {
			n.pairMu.Unlock()
		}
	}()
	n.cfg.Trust.Revoke(peer)
	n.mu.Lock()
	n.revoked[peer]++
	var cancellations []context.CancelFunc
	for a := range n.attempts[peer] {
		cancellations = append(cancellations, a.cancel)
	}
	r := n.clients[peer]
	delete(n.clients, peer)
	pairing := n.pairing
	records := n.remoteSnapshotLocked()
	n.mu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
	if r != nil {
		if pairing != nil {
			pairing.revoke(r.remote.IncomingClientKey)
		}
		if k, e := parseNodePublic(r.remote.IncomingClientKey); e == nil {
			n.server.DisconnectClient(k)
		}
		if r.client != nil {
			r.shutdown()
		}
	}
	if n.cfg.Persist == nil {
		return errors.New("peer revoked locally; durable persistence unavailable")
	}
	if e := n.cfg.Persist(n.cfg.Trust.Snapshot(), records); e != nil {
		locked = false
		n.pairMu.Unlock()
		n.Close()
		return fmt.Errorf("peer revoked locally; durable revocation unconfirmed: %w", e)
	}
	return nil
}
func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	var cancellations []context.CancelFunc
	for _, attempts := range n.attempts {
		for a := range attempts {
			cancellations = append(cancellations, a.cancel)
		}
	}
	pairing := n.pairing
	var clients []*remoteClient
	for _, r := range n.clients {
		if r.client != nil {
			clients = append(clients, r)
		}
	}
	n.mu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
	var errs []error
	if pairing != nil {
		errs = append(errs, pairing.Close())
	}
	for _, c := range clients {
		errs = append(errs, c.shutdown())
	}
	n.listenMu.Lock()
	errs = append(errs, n.server.Close())
	n.listenMu.Unlock()
	return errors.Join(errs...)
}

type peerListener struct {
	net.Listener
	node *Node
	stop func() bool
}

func (l *peerListener) Close() error {
	if l.stop != nil {
		l.stop()
	}
	return l.Listener.Close()
}
func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		peer, epoch, ok := l.node.resolveConnection(c.RemoteAddr())
		if !ok {
			c.Close()
			continue
		}
		wrapped, e := l.node.track(peer, epoch, c)
		if e != nil {
			continue
		}
		return wrapped, nil
	}
}
func (n *Node) track(peer string, epoch uint64, c net.Conn) (net.Conn, error) {
	release, e := n.cfg.Trust.Track(peer, epoch, c)
	if e != nil {
		return nil, e
	}
	if pc, ok := c.(ConnPacketConn); ok {
		return &trackedPacket{ConnPacketConn: pc, release: release, valid: func() bool { current, e := n.cfg.Trust.Epoch(peer); return e == nil && current == epoch }}, nil
	}
	return &trackedStream{Conn: c, release: release, valid: func() bool { current, e := n.cfg.Trust.Epoch(peer); return e == nil && current == epoch }}, nil
}

type trackedStream struct {
	net.Conn
	once    sync.Once
	release func()
	valid   func() bool
}

func (c *trackedStream) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
func (c *trackedStream) CloseWrite() error {
	if h, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return h.CloseWrite()
	}
	return errors.New("half-close unsupported")
}

type trackedPacket struct {
	ConnPacketConn
	once    sync.Once
	release func()
	valid   func() bool
}

func (c *trackedPacket) Close() error { e := c.ConnPacketConn.Close(); c.once.Do(c.release); return e }

var _ DataPlane = (*Node)(nil)

func parseNodePublic(s string) (key.NodePublic, error) {
	var k key.NodePublic
	if !validKey(s) {
		return k, ErrUntrusted
	}
	e := k.UnmarshalText([]byte("nodekey:" + s))
	return k, e
}
func (n *Node) PublicKey() string       { return n.cfg.Identity.PublicKey() }
func (n *Node) OverlayAddr() netip.Addr { return overlayAddress(n.cfg.Identity.Key.Public()) }
func (n *Node) remoteSnapshotLocked() []RemotePeer {
	out := make([]RemotePeer, 0, len(n.clients))
	for _, r := range n.clients {
		out = append(out, r.remote)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer.Key < out[j].Peer.Key })
	return out
}
func (n *Node) RemoteSnapshot() []RemotePeer {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.remoteSnapshotLocked()
}

// Offer returns the pairing capability without private client-role keys.
func (n *Node) Offer(name string) PeerOffer {
	return PeerOffer{Peer: Peer{Key: n.PublicKey(), Name: name}, Address: n.Address()}
}
func (n *Node) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	if f == nil {
		return nil, errors.New("fallback handler required")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	if n.fallback != nil {
		return nil, errors.New("fallback handler already registered")
	}
	n.fallback = f
	var once sync.Once
	return func() { once.Do(func() { n.mu.Lock(); n.fallback = nil; n.mu.Unlock() }) }, nil
}
func (n *Node) onTCP(port uint16) func(net.Conn) {
	if port == PairingPort {
		return nil
	}
	n.mu.Lock()
	f := n.fallback
	n.mu.Unlock()
	if f == nil {
		return nil
	}
	return func(c net.Conn) {
		defer c.Close()
		id, epoch, ok := n.resolveConnection(c.RemoteAddr())
		if !ok {
			return
		}
		wrapped, e := n.track(id, epoch, c)
		if e != nil {
			return
		}
		defer wrapped.Close()
		src, e := netip.ParseAddrPort(c.RemoteAddr().String())
		if e != nil {
			return
		}
		handler, intercept := f(src, netip.AddrPortFrom(n.OverlayAddr(), port))
		if !intercept || handler == nil {
			return
		}
		handler(wrapped)
	}
}

// PublicPeerSnapshot is identity metadata, not a connectivity/health claim.
type PublicPeerSnapshot struct {
	Key       string
	Name      string
	Endpoint  netip.Addr
	SourceIPs []netip.Addr
}

func RemoteEndpoint(remote RemotePeer) (netip.Addr, error) {
	pub, e := parseNodePublic(remote.Peer.Key)
	if e != nil {
		return netip.Addr{}, e
	}
	return overlayAddress(pub), nil
}
func IncomingSource(remote RemotePeer) (netip.Addr, error) {
	pub, e := parseNodePublic(remote.IncomingClientKey)
	if e != nil {
		return netip.Addr{}, e
	}
	return overlayAddress(pub), nil
}
func (n *Node) PublicPeers() []PublicPeerSnapshot {
	records := n.RemoteSnapshot()
	out := make([]PublicPeerSnapshot, 0, len(records))
	for _, r := range records {
		if _, e := n.cfg.Trust.Epoch(r.Peer.Key); e != nil {
			continue
		}
		endpoint, e := RemoteEndpoint(r)
		if e != nil {
			continue
		}
		source, e := IncomingSource(r)
		if e != nil {
			continue
		}
		out = append(out, PublicPeerSnapshot{r.Peer.Key, r.Peer.Name, endpoint, []netip.Addr{endpoint, source}})
	}
	return out
}

func (r *remoteClient) open(ctx context.Context) (*tailcat.Client, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.closed {
		return nil, net.ErrClosed
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !r.started {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		stop := context.AfterFunc(r.runCtx, cancel)
		defer stop()
		defer cancel()
		if _, e := r.client.Ping(bounded); e != nil {
			return nil, e
		}
		r.started = true
	}
	return r.client, nil
}
func (r *remoteClient) shutdown() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.closed = true
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

func (c *trackedStream) Valid() bool { return c.valid != nil && c.valid() }
func (c *trackedStream) Read(b []byte) (int, error) {
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	n, e := c.Conn.Read(b)
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	return n, e
}
func (c *trackedStream) Write(b []byte) (int, error) {
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	return c.Conn.Write(b)
}
func (c *trackedPacket) Valid() bool { return c.valid != nil && c.valid() }
func (c *trackedPacket) Read(b []byte) (int, error) {
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	n, e := c.ConnPacketConn.Read(b)
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	return n, e
}
func (c *trackedPacket) Write(b []byte) (int, error) {
	if len(b) > maxPacketSize {
		return 0, errors.New("datagram exceeds Tailcat payload limit")
	}
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	return c.ConnPacketConn.Write(b)
}
func (c *trackedPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	if !c.Valid() {
		return 0, nil, ErrUntrusted
	}
	n, a, e := c.ConnPacketConn.ReadFrom(b)
	if !c.Valid() {
		return 0, nil, ErrUntrusted
	}
	return n, a, e
}
func (c *trackedPacket) WriteTo(b []byte, a net.Addr) (int, error) {
	if len(b) > maxPacketSize {
		return 0, errors.New("datagram exceeds Tailcat payload limit")
	}
	if !c.Valid() {
		return 0, ErrUntrusted
	}
	return c.ConnPacketConn.WriteTo(b, a)
}
