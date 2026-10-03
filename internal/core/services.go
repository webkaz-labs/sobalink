package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

type activeService struct {
	spec            ServiceSpec
	expires         time.Time
	ctx             context.Context
	cancel          context.CancelFunc
	ready           atomic.Bool
	online          *atomic.Bool
	servers         []*transport.Server
	policies        []*policy.Policy
	transportCancel context.CancelFunc
	address         netip.Addr
	effective       ranges.Set
	reserved        ranges.Set
	error           string
}
type rangeState struct {
	engine     *ranges.Engine
	unregister func()
}
type RemoteService struct {
	ID          string    `json:"id"`
	Network     string    `json:"network"`
	Ports       string    `json:"ports"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Application string    `json:"application"`
}

// SavedServiceConfiguration is available only through the authenticated local
// command API. Peer discovery continues to expose only RemoteService metadata.
type SavedServiceConfiguration struct {
	Configuration ServiceSpec `json:"configuration"`
	Revision      string      `json:"revision"`
	Active        bool        `json:"active"`
}

type localCommandError struct{ code, message string }

func (e *localCommandError) Error() string     { return e.message }
func (e *localCommandError) ErrorCode() string { return e.code }

func serviceRevision(spec ServiceSpec) string {
	data, _ := json.Marshal(spec)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (c *Core) serviceConfiguration(raw json.RawMessage) (any, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	p := c.profileCopy()
	for _, saved := range p.Services {
		if saved.ID == input.ID {
			c.mu.RLock()
			active := c.active[saved.ID] != nil
			c.mu.RUnlock()
			return SavedServiceConfiguration{Configuration: saved, Revision: serviceRevision(saved), Active: active}, nil
		}
	}
	return nil, &localCommandError{"service_not_found", "saved service no longer exists; refresh the local service list"}
}

func validateRemote(s RemoteService) error {
	if !config.ValidPeerID(s.ID) || (s.Network != "tcp" && s.Network != "udp") || s.Application != "unverified" || !deadline.Active(time.Now(), s.ExpiresAt) || s.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return errors.New("invalid service metadata")
	}
	_, e := ranges.Parse(s.Ports)
	return e
}

func (c *Core) ensureRanges() error {
	if c.rangeState != nil {
		select {
		case <-c.rangeState.engine.Done():
			c.rangeState.unregister()
			c.rangeState = nil
		default:
			return nil
		}
	}
	n, ok := c.nodeCopy().(interface {
		RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error)
	})
	if !ok {
		return errors.New("network does not support compact TCP sharing")
	}
	engine, e := ranges.New(c.ctx, ranges.Options{AdmitTCP: transport.AdmitTCP, Authorize: func(ctx context.Context, r ranges.Request) (string, error) {
		c.mu.RLock()
		active := c.active[r.PolicyID]
		c.mu.RUnlock()
		if active == nil || active.guard() != nil {
			return "", errors.New("share is inactive")
		}
		id, e := c.authenticated(ctx, r.Source)
		if e != nil {
			return "", e
		}
		if r.PeerID != "" && r.PeerID != id {
			return "", errors.New("source identity changed")
		}
		if !slices.Contains(active.spec.PeerIDs, id) {
			return "", errors.New("peer is outside share scope")
		}
		return id, active.guard()
	}})
	if e != nil {
		return e
	}
	unregister, e := n.RegisterTCPFallback(engine.Handle)
	if e != nil {
		engine.Close()
		return e
	}
	c.rangeState = &rangeState{engine: engine, unregister: unregister}
	return nil
}
func (a *activeService) guard() error {
	return a.guardAt(time.Now())
}
func (a *activeService) guardAt(now time.Time) error {
	if !a.ready.Load() || (a.online != nil && !a.online.Load()) || !a.permissionActiveAt(now) {
		return errors.New("service permission is inactive or expired")
	}
	return nil
}

// A missing deadline remains invalid unless this exact outbound permission was
// explicitly started with until-stopped. Other grants keep deadline.Active's
// fail-closed semantics.
func (a *activeService) permissionActiveAt(now time.Time) bool {
	if a.ctx == nil || a.ctx.Err() != nil {
		return false
	}
	if a.spec.Lifetime == "until-stopped" {
		return a.spec.Direction == "forward" && a.spec.TTLSeconds == 0 && a.expires.IsZero()
	}
	return (a.spec.Lifetime == "" || a.spec.Lifetime == "finite") && deadline.Active(now, a.expires)
}

func serviceLifetime(mode string, ttl int, direction string) (string, error) {
	if mode == "" && ttl > 0 {
		mode = "finite"
	}
	switch mode {
	case "finite":
		if ttl >= 1 && ttl <= 86400 {
			return mode, nil
		}
	case "until-stopped":
		if direction == "forward" && ttl == 0 {
			return mode, nil
		}
	}
	return "", errors.New("choose a finite lifetime of 1..86400 seconds, or explicit until-stopped for an outbound connection")
}

func serviceLoopback(host string) (string, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", errors.New("loopback host must be exactly 127.0.0.1 or ::1")
	}
	return host, nil
}

func (c *Core) startServiceCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var in struct {
		Name         string   `json:"name"`
		PeerID       string   `json:"peerId"`
		PeerIDs      []string `json:"peerIds"`
		Network      string   `json:"network"`
		Ports        string   `json:"ports"`
		ExcludePorts string   `json:"excludePorts"`
		LocalPort    int      `json:"localPort"`
		LoopbackHost string   `json:"loopbackHost"`
		Lifetime     string   `json:"lifetime"`
		RemotePort   int      `json:"remotePort"`
		TTLSeconds   int      `json:"ttlSeconds"`
		Purpose      string   `json:"purpose"`
		Discoverable bool     `json:"discoverable"`
		ServiceID    string   `json:"serviceId"`
		Backend      string   `json:"backend"`
		ReplaceID    string   `json:"replaceId"`
		Revision     string   `json:"expectedRevision"`
	}
	if e := decodePayload(raw, &in); e != nil {
		return nil, e
	}
	if in.Ports == "" && in.RemotePort > 0 {
		in.Ports = strconv.Itoa(in.RemotePort)
	}
	if !config.ValidName(in.Name) {
		return nil, errors.New("choose a name with 1..64 letters, digits, hyphens or underscores")
	}
	if in.Network != "tcp" && in.Network != "udp" {
		return nil, errors.New("choose TCP or UDP")
	}
	direction := "forward"
	if name == "service.share" {
		direction = "share"
	}
	var e error
	in.Lifetime, e = serviceLifetime(in.Lifetime, in.TTLSeconds, direction)
	if e != nil {
		return nil, e
	}
	in.LoopbackHost, e = serviceLoopback(in.LoopbackHost)
	if e != nil {
		return nil, e
	}
	if in.LocalPort < 0 || in.LocalPort > 65535 {
		return nil, errors.New("local port must be 1..65535, or omitted for same-port mapping")
	}
	ports, e := ranges.Parse(in.Ports)
	if e != nil {
		return nil, e
	}
	var exclude ranges.Set
	if in.ExcludePorts != "" {
		exclude, e = ranges.Parse(in.ExcludePorts)
		if e != nil {
			return nil, e
		}
	}
	effective, e := ports.Excluding(exclude)
	if e != nil || effective.Empty() {
		return nil, errors.New("no ports remain after exclusions")
	}
	if direction == "share" {
		if in.LocalPort != 0 && effective.Count() != 1 {
			return nil, errors.New("a mapped share requires exactly one exposed port; omit localPort for same-port ranges")
		}
		reserved, _ := ranges.Parse("54543-54545")
		effective, e = effective.Excluding(reserved)
		if e != nil || effective.Empty() {
			return nil, errors.New("ports 54543 through 54545 are reserved")
		}
	}
	p := c.profileCopy()
	if in.Backend != "" && in.Backend != p.Settings.Network {
		return nil, &localCommandError{"service_backend_mismatch", "review this service in its selected network before starting it"}
	}
	if (in.ReplaceID == "") != (in.Revision == "") {
		return nil, &localCommandError{"service_revision_required", "explicit replacement requires the saved service ID and reviewed revision"}
	}
	id := randomID()
	if in.ReplaceID != "" {
		if in.Backend != "tailnet" && in.Backend != "lan" {
			return nil, &localCommandError{"service_backend_required", "review and explicitly select the backend for this saved service"}
		}
		found := false
		for _, saved := range p.Services {
			if saved.ID != in.ReplaceID {
				continue
			}
			found = true
			c.mu.RLock()
			active := c.active[saved.ID] != nil
			c.mu.RUnlock()
			if active {
				return nil, &localCommandError{"service_active", "stop this service explicitly before editing or restarting it"}
			}
			if in.Revision != serviceRevision(saved) {
				return nil, &localCommandError{"service_revision_conflict", "saved service changed; reload and review its complete configuration"}
			}
			if saved.Backend != "" && saved.Backend != in.Backend {
				return nil, &localCommandError{"service_backend_mismatch", "saved service belongs to another backend; review it in that network"}
			}
			id = saved.ID
			break
		}
		if !found {
			return nil, &localCommandError{"service_not_found", "saved service no longer exists; refresh the local service list"}
		}
	}
	for _, saved := range p.Services {
		if saved.Name == in.Name && saved.ID != in.ReplaceID {
			return nil, &localCommandError{"service_name_conflict", "a saved service already uses this name; choose a new name or explicitly review that service for replacement"}
		}
	}
	spec := ServiceSpec{ID: id, Backend: p.Settings.Network, Name: in.Name, Direction: direction, Network: in.Network, Ports: ports.String(), ExcludePorts: exclude.String(), LocalPort: in.LocalPort, LoopbackHost: in.LoopbackHost, Lifetime: in.Lifetime, PeerID: in.PeerID, PeerIDs: in.PeerIDs, TTLSeconds: in.TTLSeconds, Purpose: in.Purpose, Discoverable: in.Discoverable, ServiceID: in.ServiceID}
	st, e := c.current(ctx)
	if e != nil || !st.Snapshot.Running || len(st.IPs) == 0 {
		return nil, errors.New("connect the selected network before starting a service")
	}
	c.networkReady.Store(true)
	var backendReserved ranges.Set
	if direction == "share" {
		intervals := []ranges.Interval{}
		for _, port := range st.ReservedPorts {
			intervals = append(intervals, ranges.Interval{First: port, Last: port})
		}
		c.mu.RLock()
		if c.web != nil {
			port := c.web.Port()
			intervals = append(intervals, ranges.Interval{First: port, Last: port})
		}
		c.mu.RUnlock()
		intervals = append(intervals, ranges.Interval{First: 54543, Last: 54545})
		backendReserved, e = ranges.NewSet(intervals)
		if e != nil {
			return nil, e
		}
		if in.LocalPort != 0 && backendReserved.Contains(uint16(in.LocalPort)) {
			return nil, errors.New("mapped local target is reserved by the application or network backend")
		}
		effective, e = effective.Excluding(backendReserved)
		if e != nil || effective.Empty() {
			return nil, errors.New("all selected ports are reserved by the network backend")
		}
	}
	if direction == "share" {
		if len(in.PeerIDs) < 1 || len(in.PeerIDs) > 32 {
			return nil, errors.New("select 1..32 current peers for sharing")
		}
		seen := map[string]bool{}
		for _, id := range in.PeerIDs {
			if seen[id] {
				return nil, errors.New("duplicate allowed peer")
			}
			seen[id] = true
			if _, e := c.currentPeer(ctx, id); e != nil {
				return nil, e
			}
		}
	} else {
		if in.Discoverable {
			return nil, errors.New("only shares advertise service metadata")
		}
		if _, e := c.currentPeer(ctx, in.PeerID); e != nil {
			return nil, e
		}
		if in.ServiceID != "" {
			if e := c.probePeer(ctx, in.PeerID); e != nil {
				return nil, e
			}
			c.mu.RLock()
			found := false
			for _, s := range c.discovered[in.PeerID] {
				if s.ID == in.ServiceID && s.Network == in.Network && s.Ports == effective.String() {
					found = true
				}
			}
			c.mu.RUnlock()
			if !found {
				return nil, errors.New("shared service changed; review the current service before connecting")
			}
		}
	}
	var expanded []uint16
	if direction == "forward" || in.Network == "udp" {
		remaining := 64 - c.materializedCount()
		expanded, e = effective.Expand(remaining)
		if e != nil {
			return nil, errors.New("this plan exceeds the remaining OS/UDP listener capacity; narrow the selection")
		}
		if direction == "forward" {
			for i, port := range expanded {
				local := int(port)
				if in.LocalPort != 0 {
					local = in.LocalPort + i
				}
				if local < 1024 || local > 65535 {
					return nil, errors.New("local listener ports must be 1024..65535; choose a local starting port")
				}
			}
		}
	}
	if in.Network == "tcp" && direction == "share" {
		if e := c.ensureRanges(); e != nil {
			return nil, e
		}
	}
	self := st.IPs[0]
	for _, ip := range st.IPs {
		if ip.Is4() {
			self = ip
			break
		}
	}
	lifetime, cancel := context.WithCancel(c.ctx)
	active := &activeService{spec: spec, ctx: lifetime, cancel: cancel, address: self, effective: effective, online: &c.networkReady, reserved: backendReserved}
	if spec.Lifetime == "finite" {
		active.expires = time.Now().Add(time.Duration(spec.TTLSeconds) * time.Second)
	}
	// Validate the complete compact plan before saving or opening any listener.
	if direction == "share" {
		if _, e := c.rangePlan(st.IPs, active); e != nil {
			cancel()
			return nil, e
		}
	}
	replaced := false
	for i := range p.Services {
		if p.Services[i].ID == spec.ID {
			p.Services[i] = spec
			replaced = true
		}
	}
	if !replaced {
		p.Services = append(p.Services, spec)
	}
	if e := c.saveProfile(p); e != nil {
		cancel()
		return nil, e
	}
	c.mu.Lock()
	c.profile = p
	c.active[spec.ID] = active
	delete(c.serviceStates, spec.ID)
	c.mu.Unlock()
	if err := c.startServiceTransport(ctx, st, active); err != nil {
		active.cancel()
		c.mu.Lock()
		delete(c.active, spec.ID)
		c.serviceStates[spec.ID] = "failed"
		c.mu.Unlock()
		return nil, fmt.Errorf("saved but not started: %w", err)
	}
	return c.serviceView(active), nil
}

// Transport recreation uses the existing permission context and expiry. It
// never starts saved definitions or converts a revoked grant into a new one.
// Service mutations are serialized by c.op; resource slices use c.mu for views.
func (c *Core) startServiceTransport(ctx context.Context, st identity.State, active *activeService) error {
	if !active.permissionActiveAt(time.Now()) || ctx.Err() != nil {
		return errors.New("service permission is inactive or expired")
	}
	spec := active.spec
	loopback, err := serviceLoopback(spec.LoopbackHost)
	if err != nil {
		return err
	}
	lifetime, cancel := context.WithCancel(active.ctx)
	var servers []*transport.Server
	var policies []*policy.Policy
	rollback := func(err error) error {
		active.ready.Store(false)
		cancel()
		for _, server := range servers {
			_ = server.Close()
		}
		c.mu.Lock()
		active.error = err.Error()
		c.mu.Unlock()
		return err
	}
	udpBudget := transport.NewUDPBudget()
	if spec.Direction == "forward" || spec.Network == "udp" {
		expanded, err := active.effective.Expand(64)
		if err != nil {
			return rollback(err)
		}
		for i, port := range expanded {
			local := int(port)
			if spec.LocalPort != 0 {
				local = spec.LocalPort + i
			}
			var server *transport.Server
			if spec.Direction == "forward" {
				peer, err := c.currentPeer(ctx, spec.PeerID)
				if err != nil || len(peer.IPs) == 0 {
					return rollback(errors.New("peer has no current service address"))
				}
				targetHost := peer.DNSName
				if targetHost == "" {
					targetHost = peer.IPs[0].String()
				}
				target := config.Address(targetHost, int(port))
				node := c.nodeCopy()
				pol := &policy.Policy{Rules: []policy.Rule{{PeerID: peer.ID, Host: targetHost, Port: int(port), Network: spec.Network}}, Guard: active.guard, Source: func(ctx context.Context) (policy.Snapshot, error) { s, e := c.current(ctx); return s.Snapshot, e }, DialIP: node.DialIP}
				policies = append(policies, pol)
				if spec.Network == "tcp" {
					server, err = transport.StartTCP(lifetime, transport.TCPConfig{ListenAddress: config.Address(loopback, local), Target: target}, pol.Dial)
				} else {
					server, err = transport.StartUDP(lifetime, transport.UDPConfig{ListenAddress: config.Address(loopback, local), Target: target, Validate: pol.Validate, Budget: udpBudget}, pol.Dial)
				}
				if err != nil {
					return rollback(fmt.Errorf("local port %d is unavailable; select a different starting port or narrower range", local))
				}
			} else {
				packet, err := c.nodeCopy().ListenPacket("udp", config.Address(active.address.String(), int(port)))
				if err != nil {
					return rollback(err)
				}
				authorize := func(ctx context.Context, source netip.AddrPort) error {
					id, err := c.authenticated(ctx, source)
					if err != nil {
						return err
					}
					if !slices.Contains(spec.PeerIDs, id) {
						return errors.New("peer not allowed")
					}
					return active.guard()
				}
				server, err = transport.StartInboundUDP(lifetime, transport.UDPConfig{Target: config.Address(loopback, local), Budget: udpBudget}, packet, authorize, active.guard)
				if err != nil {
					packet.Close()
					return rollback(err)
				}
			}
			servers = append(servers, server)
		}
	}
	if ctx.Err() != nil || !active.permissionActiveAt(time.Now()) {
		return rollback(errors.New("start was cancelled or expired"))
	}
	c.mu.Lock()
	active.servers, active.policies, active.transportCancel = servers, policies, cancel
	active.error = ""
	active.ready.Store(true)
	c.mu.Unlock()
	if spec.Direction == "share" && spec.Network == "tcp" {
		err = c.ensureRanges()
		if err == nil {
			var plan *ranges.Plan
			plan, err = c.rangePlan(st.IPs, nil)
			if err == nil {
				err = c.rangeState.engine.Apply(plan)
			}
		}
		if err != nil {
			c.suspendServiceTransport(active)
			return rollback(err)
		}
	}
	return nil
}

func serviceLoopbackAddr(spec ServiceSpec) netip.Addr {
	host, _ := serviceLoopback(spec.LoopbackHost)
	return netip.MustParseAddr(host)
}

func (c *Core) rangePlan(ips []netip.Addr, extra *activeService) (*ranges.Plan, error) {
	c.mu.RLock()
	var entries []*activeService
	for _, a := range c.active {
		if a.spec.Direction == "share" && a.permissionActiveAt(time.Now()) {
			entries = append(entries, a)
		}
	}
	c.mu.RUnlock()
	if extra != nil {
		entries = append(entries, extra)
	}
	policies := make([]ranges.Policy, 0, len(entries))
	for _, a := range entries {
		ports, e := ranges.Parse(a.spec.Ports)
		if e != nil {
			return nil, e
		}
		var exclude ranges.Set
		if a.spec.ExcludePorts != "" {
			exclude, e = ranges.Parse(a.spec.ExcludePorts)
			if e != nil {
				return nil, e
			}
		}
		allExclusions := append(exclude.Intervals(), a.reserved.Intervals()...)
		exclude, e = ranges.NewSet(allExclusions)
		if e != nil {
			return nil, e
		}
		policies = append(policies, ranges.Policy{ID: a.spec.ID, Network: a.spec.Network, Address: a.address, Ports: ports, Exclude: exclude, Loopback: serviceLoopbackAddr(a.spec), TargetPort: uint16(a.spec.LocalPort), PeerIDs: a.spec.PeerIDs, ExpiresAt: a.expires})
	}
	return ranges.BuildPlan(ips, policies)
}
func (c *Core) materializedCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, a := range c.active {
		if a.spec.Direction == "forward" || a.spec.Network == "udp" {
			n += int(a.effective.Count())
		}
	}
	return n
}
func (c *Core) stopServiceCommand(raw json.RawMessage) (any, error) {
	var v struct {
		ID string `json:"id"`
	}
	if e := decodePayload(raw, &v); e != nil {
		return nil, e
	}
	c.stopServiceIDs([]string{v.ID})
	return nil, nil
}

func (c *Core) stopServiceIDs(ids []string) {
	c.mu.Lock()
	var stopped []*activeService
	for _, id := range ids {
		if a := c.active[id]; a != nil {
			expired := !a.permissionActiveAt(time.Now())
			a.ready.Store(false)
			a.cancel()
			stopped = append(stopped, a)
			delete(c.active, id)
			if expired {
				c.serviceStates[id] = "expired"
			} else {
				c.serviceStates[id] = "stopped"
			}
		}
	}
	c.mu.Unlock()
	if c.rangeState != nil {
		c.rangeState.engine.RevokeIDs(ids)
	}
	// Every selected permission is cancelled before potentially slow listener drain.
	for _, a := range stopped {
		for _, s := range a.servers {
			_ = s.Close()
		}
	}
}

// reserveServicePort closes shares that could expose a newly created local
// control endpoint, including mappings whose exposed port differs from it.
func (c *Core) reserveServicePort(network string, port uint16) {
	c.mu.RLock()
	var conflicts []string
	for id, active := range c.active {
		if active.spec.Direction == "share" && active.spec.Network == network && (active.effective.Contains(port) || active.spec.LocalPort == int(port)) {
			conflicts = append(conflicts, id)
		}
	}
	c.mu.RUnlock()
	c.stopServiceIDs(conflicts)
}

func (c *Core) stopAllServices() {
	c.mu.RLock()
	var ids []string
	for id := range c.active {
		ids = append(ids, id)
	}
	c.mu.RUnlock()
	c.stopServiceIDs(ids)
	if c.rangeState != nil {
		_ = c.rangeState.engine.Close()
		c.rangeState.unregister()
		c.rangeState = nil
	}
}
func (c *Core) stopPeerServices(id string) {
	c.mu.RLock()
	var ids []string
	for key, a := range c.active {
		if a.spec.PeerID == id || slices.Contains(a.spec.PeerIDs, id) {
			ids = append(ids, key)
		}
	}
	ps := c.peerServer
	c.mu.RUnlock()
	c.stopServiceIDs(ids)
	if ps != nil {
		ps.revoke(id)
	}
}
func (c *Core) expireServices() {
	c.mu.RLock()
	var ids []string
	for id, a := range c.active {
		if !a.permissionActiveAt(time.Now()) {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	c.stopServiceIDs(ids)
}
func (c *Core) suspendServiceTransport(a *activeService) {
	a.ready.Store(false)
	if c.rangeState != nil && a.spec.Direction == "share" && a.spec.Network == "tcp" {
		c.rangeState.engine.RevokeIDs([]string{a.spec.ID})
	}
	c.mu.Lock()
	cancel, servers := a.transportCancel, a.servers
	a.transportCancel, a.servers, a.policies = nil, nil, nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, server := range servers {
		_ = server.Close()
	}
}

func (c *Core) suspendServices() {
	c.mu.RLock()
	var all []*activeService
	for _, a := range c.active {
		all = append(all, a)
	}
	c.mu.RUnlock()
	// Disable every grant before draining individual transports.
	for _, a := range all {
		a.ready.Store(false)
	}
	for _, a := range all {
		c.suspendServiceTransport(a)
	}
}

func (c *Core) serviceTransportReady(a *activeService) bool {
	if !a.ready.Load() {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, server := range a.servers {
		select {
		case <-server.Done():
			return false
		default:
		}
	}
	if a.spec.Direction == "share" && a.spec.Network == "tcp" {
		if c.rangeState == nil {
			return false
		}
		select {
		case <-c.rangeState.engine.Done():
			return false
		default:
		}
	}
	return true
}

// Reconnect resets existing transport resources only. Ordinary peer services
// do not need a sobalink hello endpoint for their local forwards to recover.
func (c *Core) reconnectPeerServices(ctx context.Context, id string) error {
	st, err := c.current(ctx)
	if err != nil || !st.Snapshot.Running {
		return errors.New("network is not connected")
	}
	if _, err := c.currentPeer(ctx, id); err != nil {
		return err
	}
	c.networkReady.Store(true)
	c.expireServices()
	c.revalidateServiceScopes(st, false)
	c.mu.RLock()
	var selected []*activeService
	for _, active := range c.active {
		if active.spec.PeerID == id || slices.Contains(active.spec.PeerIDs, id) {
			selected = append(selected, active)
		}
	}
	c.mu.RUnlock()
	if len(selected) == 0 {
		return c.probePeer(ctx, id)
	}
	var recover []*activeService
	for _, active := range selected {
		// Resetting a healthy shared listener would interrupt other allowed
		// peers. Only its own transport failure authorizes shared recovery.
		if active.spec.Direction == "forward" || !c.serviceTransportReady(active) {
			active.ready.Store(false)
			recover = append(recover, active)
		}
	}
	var failures []error
	for _, active := range recover {
		c.suspendServiceTransport(active)
		if err := c.startServiceTransport(ctx, st, active); err != nil {
			failures = append(failures, err)
		}
	}
	// Refresh app capabilities too, without requiring an ordinary service
	// peer to run a sobalink API. Probe success never renews a service grant.
	_ = c.probePeer(ctx, id)
	return errors.Join(failures...)
}

func (c *Core) revalidateServices(st identity.State) {
	c.revalidateServiceScopes(st, true)
}

func (c *Core) revalidateServiceScopes(st identity.State, recoverTransport bool) {
	ids := map[string]bool{}
	for _, p := range st.Snapshot.Peers {
		if !p.Expired {
			ids[p.ID] = true
		}
	}
	c.mu.RLock()
	var revoke []string
	var active []*activeService
	for key, a := range c.active {
		active = append(active, a)
		valid := a.spec.PeerID == "" || ids[a.spec.PeerID]
		for _, id := range a.spec.PeerIDs {
			valid = valid && ids[id]
		}
		if !slices.Contains(st.IPs, a.address) {
			valid = false
		}
		if a.spec.Direction == "share" {
			for _, port := range st.ReservedPorts {
				if a.effective.Contains(port) || a.spec.LocalPort == int(port) {
					valid = false
				}
			}
		}
		if !valid {
			revoke = append(revoke, key)
		}
	}
	c.mu.RUnlock()
	c.stopServiceIDs(revoke)
	for _, a := range active {
		if !a.permissionActiveAt(time.Now()) {
			continue
		}
		if recoverTransport && !c.serviceTransportReady(a) {
			c.suspendServiceTransport(a)
			_ = c.startServiceTransport(c.ctx, st, a)
		}
		for _, p := range a.policies {
			ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
			_ = p.RevalidateActive(ctx)
			cancel()
		}
	}
}
func (c *Core) serviceView(a *activeService) map[string]any {
	endpoint := ""
	if len(a.servers) > 0 {
		endpoint = a.servers[0].Addr().String()
	} else if a.spec.Direction == "forward" {
		port := a.spec.LocalPort
		if port == 0 {
			intervals := a.effective.Intervals()
			if len(intervals) > 0 {
				port = int(intervals[0].First)
			}
		}
		host, _ := serviceLoopback(a.spec.LoopbackHost)
		endpoint = config.Address(host, port)
	} else if a.address.IsValid() {
		endpoint = net.JoinHostPort(a.address.String(), a.effective.String())
	}
	state := "active"
	if !c.networkReady.Load() || !a.ready.Load() {
		state = "reconnecting"
	}
	if a.error != "" {
		state = "failed"
	}
	var expiresAt any
	if !a.expires.IsZero() {
		expiresAt = a.expires
	}
	return map[string]any{"id": a.spec.ID, "name": a.spec.Name, "peerId": a.spec.PeerID, "peerIds": a.spec.PeerIDs, "network": a.spec.Network, "ports": a.effective.String(), "localPort": a.spec.LocalPort, "loopbackHost": a.spec.LoopbackHost, "lifetime": a.spec.Lifetime, "ttlSeconds": a.spec.TTLSeconds, "error": a.error, "endpoint": endpoint, "expiresAt": expiresAt, "status": state, "application": "unverified", "direction": a.spec.Direction}
}
func (c *Core) serviceViews() []map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []map[string]any{}
	for _, s := range c.profile.Services {
		if a := c.active[s.ID]; a != nil {
			out = append(out, c.serviceView(a))
		} else {
			state := c.serviceStates[s.ID]
			if state == "" {
				state = "saved"
			}
			out = append(out, map[string]any{"id": s.ID, "name": s.Name, "peerId": s.PeerID, "peerIds": s.PeerIDs, "network": s.Network, "ports": s.Ports, "localPort": s.LocalPort, "loopbackHost": s.LoopbackHost, "lifetime": s.Lifetime, "ttlSeconds": s.TTLSeconds, "status": state, "application": "unverified", "direction": s.Direction})
		}
	}
	return out
}
func (c *Core) permittedServices(id string) []RemoteService {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []RemoteService{}
	for _, a := range c.active {
		if a.spec.Direction == "share" && a.spec.Discoverable && slices.Contains(a.spec.PeerIDs, id) && a.guard() == nil {
			out = append(out, RemoteService{ID: a.spec.ID, Network: a.spec.Network, Ports: a.effective.String(), ExpiresAt: a.expires, Application: "unverified"})
		}
	}
	return out
}
func (c *Core) discoveredViews() []map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []map[string]any{}
	for id, services := range c.discovered {
		if time.Since(c.confirmed[id]) > 15*time.Second {
			continue
		}
		for _, s := range services {
			if deadline.Active(time.Now(), s.ExpiresAt) {
				out = append(out, map[string]any{"id": s.ID, "name": s.Network + " " + s.Ports, "peerId": id, "network": s.Network, "ports": s.Ports, "expiresAt": s.ExpiresAt, "status": "active", "application": "unverified"})
			}
		}
	}
	return out
}
