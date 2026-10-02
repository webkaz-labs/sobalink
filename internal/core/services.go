package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"github.com/webkaz-labs/tsnet-bridge/internal/ranges"
	"github.com/webkaz-labs/tsnet-bridge/internal/transport"
)

type activeService struct {
	spec      ServiceSpec
	expires   time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	ready     atomic.Bool
	online    *atomic.Bool
	servers   []*transport.Server
	policies  []*policy.Policy
	address   netip.Addr
	effective ranges.Set
	reserved  ranges.Set
	error     string
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

func validateRemote(s RemoteService) error {
	if !config.ValidPeerID(s.ID) || (s.Network != "tcp" && s.Network != "udp") || s.Application != "unverified" || !time.Now().Before(s.ExpiresAt) || s.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return errors.New("invalid service metadata")
	}
	_, e := ranges.Parse(s.Ports)
	return e
}

func (c *Core) ensureRanges() error {
	if c.rangeState != nil {
		return nil
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
	if !a.ready.Load() || (a.online != nil && !a.online.Load()) || a.ctx.Err() != nil || !time.Now().Before(a.expires) {
		return errors.New("service permission is inactive or expired")
	}
	return nil
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
		RemotePort   int      `json:"remotePort"`
		TTLSeconds   int      `json:"ttlSeconds"`
		Purpose      string   `json:"purpose"`
		Discoverable bool     `json:"discoverable"`
		ServiceID    string   `json:"serviceId"`
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
	if in.TTLSeconds < 1 || in.TTLSeconds > 86400 {
		return nil, errors.New("permission lifetime must be 1 second to 24 hours")
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
	direction := "forward"
	if name == "service.share" {
		if in.LocalPort != 0 {
			return nil, errors.New("shares use the same local and shared port; localPort is only supported for connections")
		}
		direction = "share"
		reserved, _ := ranges.Parse("54543-54545")
		effective, e = effective.Excluding(reserved)
		if e != nil || effective.Empty() {
			return nil, errors.New("ports 54543 through 54545 are reserved")
		}
	}
	spec := ServiceSpec{ID: randomID(), Name: in.Name, Direction: direction, Network: in.Network, Ports: ports.String(), ExcludePorts: exclude.String(), LocalPort: in.LocalPort, PeerID: in.PeerID, PeerIDs: in.PeerIDs, TTLSeconds: in.TTLSeconds, Purpose: in.Purpose, Discoverable: in.Discoverable}
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
		backendReserved, e = ranges.NewSet(intervals)
		if e != nil {
			return nil, e
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
	p := c.profileCopy()
	for _, saved := range p.Services {
		if saved.Name == spec.Name {
			c.mu.RLock()
			active := c.active[saved.ID]
			c.mu.RUnlock()
			if active != nil {
				return nil, errors.New("a service with this name is active; stop it before editing")
			}
			spec.ID = saved.ID
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
	active := &activeService{spec: spec, expires: time.Now().Add(time.Duration(in.TTLSeconds) * time.Second), ctx: lifetime, cancel: cancel, address: self, effective: effective, online: &c.networkReady, reserved: backendReserved}
	udpBudget := transport.NewUDPBudget()
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
	c.mu.Unlock()
	rollback := func(err error) (any, error) {
		active.ready.Store(false)
		cancel()
		for _, s := range active.servers {
			_ = s.Close()
		}
		c.mu.Lock()
		c.serviceStates[spec.ID] = "failed"
		c.mu.Unlock()
		return nil, fmt.Errorf("saved but not started: %w", err)
	}
	if direction == "forward" {
		peer, _ := c.currentPeer(ctx, in.PeerID)
		for i, port := range expanded {
			local := int(port)
			if in.LocalPort != 0 {
				local = in.LocalPort + i
			}
			targetHost := peer.DNSName
			if targetHost == "" {
				targetHost = peer.IPs[0].String()
			}
			target := config.Address(targetHost, int(port))
			node := c.nodeCopy()
			pol := &policy.Policy{Rules: []policy.Rule{{PeerID: peer.ID, Host: targetHost, Port: int(port), Network: in.Network}}, Guard: active.guard, Source: func(ctx context.Context) (policy.Snapshot, error) { s, e := c.current(ctx); return s.Snapshot, e }, DialIP: node.DialIP}
			active.policies = append(active.policies, pol)
			var server *transport.Server
			if in.Network == "tcp" {
				server, e = transport.StartTCP(lifetime, transport.TCPConfig{ListenAddress: config.Loopback(local), Target: target}, pol.Dial)
			} else {
				server, e = transport.StartUDP(lifetime, transport.UDPConfig{ListenAddress: config.Loopback(local), Target: target, Validate: pol.Validate, Budget: udpBudget}, pol.Dial)
			}
			if e != nil {
				return rollback(fmt.Errorf("local port %d is unavailable; select a different starting port or narrower range", local))
			}
			active.servers = append(active.servers, server)
		}
	} else if in.Network == "udp" {
		for _, port := range expanded {
			packet, e := c.nodeCopy().ListenPacket("udp", config.Address(self.String(), int(port)))
			if e != nil {
				return rollback(e)
			}
			authorize := func(ctx context.Context, source netip.AddrPort) error {
				id, e := c.authenticated(ctx, source)
				if e != nil {
					return e
				}
				if !slices.Contains(spec.PeerIDs, id) {
					return errors.New("peer not allowed")
				}
				return active.guard()
			}
			server, e := transport.StartInboundUDP(lifetime, transport.UDPConfig{Target: config.Loopback(int(port)), Budget: udpBudget}, packet, authorize, active.guard)
			if e != nil {
				packet.Close()
				return rollback(e)
			}
			active.servers = append(active.servers, server)
		}
	}
	if ctx.Err() != nil || lifetime.Err() != nil || !time.Now().Before(active.expires) {
		return rollback(errors.New("start was cancelled or expired"))
	}
	c.mu.Lock()
	c.active[spec.ID] = active
	delete(c.serviceStates, spec.ID)
	c.mu.Unlock()
	active.ready.Store(true)
	if direction == "share" && in.Network == "tcp" {
		plan, e := c.rangePlan(st.IPs, nil)
		if e == nil {
			e = c.rangeState.engine.Apply(plan)
		}
		if e != nil {
			c.mu.Lock()
			delete(c.active, spec.ID)
			c.mu.Unlock()
			return rollback(e)
		}
	}
	return c.serviceView(active), nil
}

func (c *Core) rangePlan(ips []netip.Addr, extra *activeService) (*ranges.Plan, error) {
	c.mu.RLock()
	var entries []*activeService
	for _, a := range c.active {
		if a.spec.Direction == "share" && a.guard() == nil {
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
		policies = append(policies, ranges.Policy{ID: a.spec.ID, Network: a.spec.Network, Address: a.address, Ports: ports, Exclude: exclude, Loopback: netip.MustParseAddr("127.0.0.1"), PeerIDs: a.spec.PeerIDs, ExpiresAt: a.expires})
	}
	return ranges.BuildPlan(ips, policies)
}
func (c *Core) materializedCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, a := range c.active {
		n += len(a.servers)
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
			a.ready.Store(false)
			a.cancel()
			stopped = append(stopped, a)
			delete(c.active, id)
			if !time.Now().Before(a.expires) {
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
		if !time.Now().Before(a.expires) {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	c.stopServiceIDs(ids)
}
func (c *Core) suspendServices() {
	c.mu.RLock()
	var all []*activeService
	for _, a := range c.active {
		all = append(all, a)
	}
	c.mu.RUnlock()
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	for _, a := range all {
		for _, p := range a.policies {
			_ = p.RevalidateActive(ctx)
		}
	}
	if c.rangeState != nil {
		_ = c.rangeState.engine.Revalidate(ctx)
	}
}
func (c *Core) revalidateServices(st identity.State) {
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
				if a.effective.Contains(port) {
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
	} else if a.address.IsValid() {
		endpoint = a.address.String() + ":" + a.effective.String()
	}
	state := "active"
	if !c.networkReady.Load() {
		state = "reconnecting"
	}
	return map[string]any{"id": a.spec.ID, "name": a.spec.Name, "peerId": a.spec.PeerID, "peerIds": a.spec.PeerIDs, "network": a.spec.Network, "ports": a.effective.String(), "localPort": a.spec.LocalPort, "endpoint": endpoint, "expiresAt": a.expires, "status": state, "application": "unverified", "direction": a.spec.Direction}
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
			out = append(out, map[string]any{"id": s.ID, "name": s.Name, "peerId": s.PeerID, "peerIds": s.PeerIDs, "network": s.Network, "ports": s.Ports, "localPort": s.LocalPort, "status": state, "application": "unverified", "direction": s.Direction})
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
			if time.Now().Before(s.ExpiresAt) {
				out = append(out, map[string]any{"id": s.ID, "name": s.Network + " " + s.Ports, "peerId": id, "network": s.Network, "ports": s.Ports, "expiresAt": s.ExpiresAt, "status": "active", "application": "unverified"})
			}
		}
	}
	return out
}
