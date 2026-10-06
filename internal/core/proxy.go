package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

// ProxyScope is an advanced, process-local permission. Credentials and live
// listeners are deliberately absent from saved profiles and profile exports.
type ProxyScope struct {
	Name         string        `json:"name"`
	Backend      string        `json:"backend"`
	LoopbackHost string        `json:"loopbackHost"`
	LocalPort    int           `json:"localPort"`
	Lifetime     string        `json:"lifetime"`
	TTLSeconds   int           `json:"ttlSeconds"`
	Targets      []ProxyTarget `json:"targets"`
}
type ProxyTarget struct {
	PeerID string `json:"peerId"`
	Port   int    `json:"port"`
}
type proxyPeerReview struct {
	PeerID string `json:"peerId"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}
type proxyReview struct {
	Scope          ProxyScope        `json:"scope"`
	Revision       string            `json:"revision"`
	Endpoint       string            `json:"endpoint"`
	Targets        []proxyPeerReview `json:"targets"`
	Authentication string            `json:"authentication"`
	Application    string            `json:"application"`
}
type proxyServer interface {
	Close() error
	Addr() net.Addr
	Done() <-chan struct{}
}
type proxyStarter func(context.Context, transport.SOCKSConfig, transport.Dialer) (proxyServer, error)
type activeProxy struct {
	id      string
	scope   ProxyScope
	expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	server  proxyServer
	policy  *policy.Policy
}

func (c *Core) reviewProxy(ctx context.Context, scope ProxyScope) (proxyReview, error) {
	return c.reviewProxyScope(ctx, scope, false)
}

// Retained admission bypasses only future capacity choices, never scope or
// current-identity validation. Only an existing suspended run uses it.
func (c *Core) reviewProxyScope(ctx context.Context, scope ProxyScope, retainedAdmission bool) (proxyReview, error) {
	bad := func(message string) (proxyReview, error) {
		return proxyReview{}, &localCommandError{"proxy_scope_invalid", message}
	}
	if !config.ValidName(scope.Name) {
		return bad("choose a proxy name with 1..64 letters, digits, hyphens or underscores")
	}
	host, err := serviceLoopback(scope.LoopbackHost)
	if err != nil {
		return bad("proxy listener must use 127.0.0.1 or ::1")
	}
	scope.LoopbackHost = host
	if scope.LocalPort < 1024 || scope.LocalPort > 65535 || (scope.LocalPort >= 54543 && scope.LocalPort <= 54545) {
		return bad("choose a proxy listener port in 1024..65535 outside reserved ports")
	}
	if scope.Lifetime == "" && scope.TTLSeconds == 0 {
		scope.Lifetime = "until-stopped"
	}
	scope.Lifetime, err = serviceLifetime(scope.Lifetime, scope.TTLSeconds, "forward")
	if err != nil {
		return bad("choose a positive finite lifetime or until-stopped without ttlSeconds")
	}
	if len(scope.Targets) < 1 {
		return bad("select at least one exact peer and TCP port target")
	}
	peerIDs := map[string]bool{}
	for _, target := range scope.Targets {
		peerIDs[target.PeerID] = true
	}
	if !retainedAdmission && int64(len(peerIDs)) > c.limit("logical", "sharePeers") {
		return bad("selected peer identities exceed the configured sharePeers limit")
	}
	// The authenticated local command envelope bounds encoded target metadata;
	// sharePeers counts unique identities, never their individual ports.
	mode := c.profileCopy().Settings.Network
	if scope.Backend == "" {
		scope.Backend = mode
	}
	if (scope.Backend != "tailnet" && scope.Backend != "lan" && scope.Backend != "direct-lan" && scope.Backend != "mixed") || scope.Backend != mode {
		return bad("review proxy targets in the selected active network")
	}
	st, err := c.current(ctx)
	if err != nil || !st.Snapshot.Running {
		return proxyReview{}, &localCommandError{"proxy_network_unavailable", "connect the selected network before reviewing a proxy"}
	}
	c.mu.RLock()
	reservedLocal := c.web != nil && int(c.web.Port()) == scope.LocalPort
	for _, service := range c.active {
		if service.spec.Direction == "share" && service.spec.Network == "tcp" && (service.effective.Contains(uint16(scope.LocalPort)) || service.spec.LocalPort == scope.LocalPort) {
			reservedLocal = true
		}
	}
	for _, active := range c.proxies {
		if active.scope.Name != scope.Name && active.scope.LocalPort == scope.LocalPort && active.scope.LoopbackHost == scope.LoopbackHost {
			reservedLocal = true
		}
	}
	for _, run := range c.savedProxyRuns {
		if run.Suspended && run.Scope.Name != scope.Name && run.Scope.LocalPort == scope.LocalPort && run.Scope.LoopbackHost == scope.LoopbackHost {
			reservedLocal = true
		}
	}
	c.mu.RUnlock()
	for _, port := range st.ReservedPorts {
		reservedLocal = reservedLocal || scope.LocalPort == int(port)
	}
	if reservedLocal {
		return bad("proxy listener port is reserved or covered by an active share; choose another port or stop the conflicting share")
	}
	scope.Targets = append([]ProxyTarget(nil), scope.Targets...)
	sort.Slice(scope.Targets, func(i, j int) bool {
		if scope.Targets[i].PeerID == scope.Targets[j].PeerID {
			return scope.Targets[i].Port < scope.Targets[j].Port
		}
		return scope.Targets[i].PeerID < scope.Targets[j].PeerID
	})
	targets := make([]proxyPeerReview, 0, len(scope.Targets))
	for i, target := range scope.Targets {
		if !config.ValidPeerID(target.PeerID) || target.Port < 1 || target.Port > 65535 || (target.Port >= 54543 && target.Port <= 54545) {
			return bad("each proxy target requires an exact peer identity and application TCP port")
		}
		if i > 0 && target == scope.Targets[i-1] {
			return bad("duplicate proxy target")
		}
		var peer *policy.Peer
		for j := range st.Snapshot.Peers {
			p := &st.Snapshot.Peers[j]
			if p.ID == target.PeerID && !p.Expired {
				peer = p
				break
			}
		}
		if peer == nil || len(peer.IPs) == 0 {
			return proxyReview{}, &localCommandError{"proxy_peer_unavailable", "a proxy target is no longer present with the approved identity; refresh and review the scope"}
		}
		host := peer.DNSName
		if host == "" {
			host = peer.IPs[0].String()
		}
		targets = append(targets, proxyPeerReview{PeerID: target.PeerID, Host: host, Port: target.Port})
	}
	review := proxyReview{Scope: scope, Endpoint: config.Address(scope.LoopbackHost, scope.LocalPort), Targets: targets, Authentication: "username-password-required", Application: "unverified"}
	ids := make([]string, 0, len(peerIDs))
	for id := range peerIDs {
		ids = append(ids, id)
	}
	b, _ := json.Marshal(struct {
		Review     proxyReview
		Hostname   string
		PeerEpochs map[string]string
	}{review, c.profileCopy().Settings.Hostname, c.reviewPeerEpochs(ids)})
	digest := sha256.Sum256(b)
	review.Revision = hex.EncodeToString(digest[:])
	return review, nil
}

func (c *Core) proxyCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "proxy.list":
		var input struct{}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		return c.proxyViews(), nil
	case "proxy.stop":
		var input struct {
			ID string `json:"id"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if input.ID == "" {
			return nil, &localCommandError{"proxy_id_required", "select the proxy ID to stop"}
		}
		c.cancelSavedProxyRun(input.ID)
		c.stopProxyIDs([]string{input.ID})
		return map[string]string{"id": input.ID, "status": "stopped"}, nil
	case "proxy.preview":
		var input struct {
			Scope ProxyScope `json:"scope"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		return c.reviewProxy(ctx, input.Scope)
	case "proxy.start":
		var input struct {
			Scope            ProxyScope `json:"scope"`
			ExpectedRevision string     `json:"expectedRevision"`
			Username         string     `json:"username"`
			Password         string     `json:"password"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		review, err := c.reviewProxy(ctx, input.Scope)
		if err != nil {
			return nil, err
		}
		if input.ExpectedRevision == "" || input.ExpectedRevision != review.Revision {
			return nil, &localCommandError{"proxy_review_required", "review the exact proxy name, targets, listener and lifetime before starting"}
		}
		if len(input.Username) < 1 || len(input.Username) > 255 || len(input.Password) < 1 || len(input.Password) > 255 {
			return nil, &localCommandError{"proxy_credentials_required", "supply username and password of 1..255 bytes through private runtime input"}
		}
		return c.startReviewedProxy(ctx, review, input.Username, input.Password, time.Time{})
	}
	return nil, errors.New("unknown proxy command")
}

// SOCKS reservations begin before authentication. Once the approved destination
// is known, bind its peer budget without reserving global/policy capacity twice.
func (c *Core) proxyDial(a *activeProxy) transport.Dialer {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			return nil, errors.New("proxy supports TCP CONNECT only")
		}
		endpoint, err := a.policy.Resolve(ctx, network, address)
		if err != nil {
			return nil, err
		}
		st, err := c.current(ctx)
		if err != nil {
			return nil, err
		}
		peerID := ""
		for _, peer := range st.Snapshot.Peers {
			if peer.Expired {
				continue
			}
			for _, ip := range peer.IPs {
				if ip == endpoint.Addr() {
					if peerID != "" && peerID != peer.ID {
						return nil, errors.New("ambiguous current peer address")
					}
					peerID = peer.ID
				}
			}
		}
		release, ok := c.serviceResources().AdmitTCPPeer(peerID)
		if !ok {
			return nil, errors.New("proxy peer connection capacity reached")
		}
		conn, err := a.policy.Dial(ctx, network, address)
		if err != nil {
			release()
			return nil, err
		}
		return &proxyBudgetConn{Conn: conn, release: release}, nil
	}
}

type proxyBudgetConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *proxyBudgetConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (c *proxyBudgetConn) CloseWrite() error {
	if c, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return errors.ErrUnsupported
}
func (c *proxyBudgetConn) CloseRead() error {
	if c, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return c.CloseRead()
	}
	return errors.ErrUnsupported
}
func proxyView(a *activeProxy) map[string]any {
	status := "active"
	if a.server != nil {
		select {
		case <-a.server.Done():
			status = "failed"
		default:
		}
	}
	if a.ctx.Err() != nil {
		status = "stopped"
		if !a.expires.IsZero() && !time.Now().Before(a.expires) {
			status = "expired"
		}
	}
	return map[string]any{"id": a.id, "name": a.scope.Name, "backend": a.scope.Backend, "endpoint": config.Address(a.scope.LoopbackHost, a.scope.LocalPort), "targets": append([]ProxyTarget(nil), a.scope.Targets...), "expiresAt": proxyExpiry(a.expires), "lifetime": a.scope.Lifetime, "ttlSeconds": a.scope.TTLSeconds, "status": status, "protocol": "socks5-tcp-connect", "authentication": "required", "application": "unverified"}
}
func (c *Core) proxyViews() []map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	views := []map[string]any{}
	for _, a := range c.proxies {
		views = append(views, proxyView(a))
	}
	for _, run := range c.savedProxyRuns {
		if run.Suspended && c.proxies[run.ID] == nil {
			view := proxyView(&activeProxy{id: run.ID, scope: run.Scope, expires: run.Expires, ctx: c.ctx})
			view["status"] = "reconnecting"
			views = append(views, view)
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i]["name"].(string) < views[j]["name"].(string) })
	return views
}
func (c *Core) stopProxyIDs(ids []string) {
	c.mu.Lock()
	var stopped []*activeProxy
	for _, id := range ids {
		if a := c.proxies[id]; a != nil {
			a.cancel()
			stopped = append(stopped, a)
			delete(c.proxies, id)
		}
	}
	c.mu.Unlock()
	for _, a := range stopped {
		if a.server != nil {
			_ = a.server.Close()
		}
	}
}
func (c *Core) stopAllProxies() {
	c.mu.RLock()
	ids := make([]string, 0, len(c.proxies))
	for id := range c.proxies {
		ids = append(ids, id)
	}
	c.mu.RUnlock()
	c.stopProxyIDs(ids)
}
func (c *Core) stopPeerProxies(id string) {
	c.cancelSavedPeerProxies(id)
	c.mu.RLock()
	var ids []string
	for key, a := range c.proxies {
		for _, target := range a.scope.Targets {
			if target.PeerID == id {
				ids = append(ids, key)
				break
			}
		}
	}
	c.mu.RUnlock()
	c.stopProxyIDs(ids)
}
func (c *Core) expireProxies() {
	c.expireSavedProxyRuns()
	c.mu.RLock()
	var ids []string
	for id, a := range c.proxies {
		if a.ctx.Err() != nil || (!a.expires.IsZero() && !time.Now().Before(a.expires)) {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	c.stopProxyIDs(ids)
}
func (c *Core) revalidateProxies(st identity.State) {
	c.mu.RLock()
	var all []*activeProxy
	for _, a := range c.proxies {
		all = append(all, a)
	}
	c.mu.RUnlock()
	var stopped []string
	for _, a := range all {
		invalid := a.ctx.Err() != nil || !st.Snapshot.Running
		if a.server != nil {
			select {
			case <-a.server.Done():
				invalid = true
			default:
			}
		}
		for _, rule := range a.policy.Rules {
			if a.policy.ValidateSnapshot(st.Snapshot, "tcp", net.JoinHostPort(rule.Host, strconv.Itoa(rule.Port))) != nil {
				invalid = true
				break
			}
		}
		if !invalid {
			ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
			invalid = a.policy.RevalidateActive(ctx) != nil
			cancel()
		}
		if invalid {
			stopped = append(stopped, a.id)
		}
	}
	c.stopProxyIDs(stopped)
}

// proxyReservedPorts is called with c.mu held by service scope validation.
func (c *Core) proxyReservedPorts() []netip.AddrPort {
	ports := make([]netip.AddrPort, 0, len(c.proxies))
	for _, a := range c.proxies {
		ports = append(ports, netip.AddrPortFrom(netip.MustParseAddr(a.scope.LoopbackHost), uint16(a.scope.LocalPort)))
	}
	for _, run := range c.savedProxyRuns {
		if run.Suspended {
			ports = append(ports, netip.AddrPortFrom(netip.MustParseAddr(run.Scope.LoopbackHost), uint16(run.Scope.LocalPort)))
		}
	}
	return ports
}

func proxyExpiry(expires time.Time) any {
	if expires.IsZero() {
		return nil
	}
	return expires
}
