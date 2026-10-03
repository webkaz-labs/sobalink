package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// RuleCommand uses the existing bounded current-user-only IPC. Owners partition
// cleanup, not OS users or security principals.
type RuleCommand struct {
	ExpectedRules string        `json:"expected_rules,omitempty"`
	Replace       bool          `json:"replace,omitempty"`
	Action        string        `json:"action"`
	Names         []string      `json:"names,omitempty"`
	Group         string        `json:"group,omitempty"`
	Owner         string        `json:"owner,omitempty"`
	TTLSeconds    int64         `json:"ttl_seconds,omitempty"`
	LeaseSeconds  int64         `json:"lease_seconds,omitempty"`
	Rule          *config.Rule  `json:"rule,omitempty"`
	GroupConfig   *config.Group `json:"group_config,omitempty"`
}
type RuleStatus struct {
	ScopeDigest   string           `json:"scope_digest,omitempty"`
	TTLSeconds    int64            `json:"ttl_seconds,omitempty"`
	LeaseSeconds  int64            `json:"lease_seconds,omitempty"`
	AllowedPeers  []config.PeerRef `json:"allowed_peers,omitempty"`
	Name          string           `json:"name"`
	Purpose       string           `json:"purpose,omitempty"`
	Direction     string           `json:"direction"`
	Network       string           `json:"network"`
	State         string           `json:"state"`
	ReasonCode    string           `json:"reason_code"`
	Reason        string           `json:"reason"`
	ListenAddress string           `json:"listen_address,omitempty"`
	Target        string           `json:"target"`
	PeerID        string           `json:"peer_id,omitempty"`
	Owner         string           `json:"owner,omitempty"`
	ExpiresAt     time.Time        `json:"expires_at,omitzero"`
	CheckedAt     time.Time        `json:"checked_at"`
	Application   string           `json:"application"`
}

// lifetime checks elapsed time AND wall time, so neither suspend nor a backwards
// clock adjustment can extend a grant. Process restart never restores grants.
type lifetime struct {
	failure        string
	mu             sync.RWMutex
	expires, lease time.Time
	stopped        bool
	ctx            context.Context
	cancel         context.CancelFunc
}

func deadlinePassed(now, until time.Time) bool {
	return deadline.Passed(now, until)
}
func (l *lifetime) check() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := time.Now()
	if l.failure != "" {
		return errors.New(l.failure)
	}
	if l.stopped {
		return errors.New("stopped")
	}
	if deadlinePassed(now, l.expires) {
		return errors.New("expired")
	}
	if deadlinePassed(now, l.lease) {
		return errors.New("lease-expired")
	}
	return nil
}
func (l *lifetime) stop() {
	l.mu.Lock()
	l.stopped = true
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (l *lifetime) renew(d time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.failure != "" || l.stopped || deadlinePassed(now, l.expires) || deadlinePassed(now, l.lease) {
		return errors.New("lease already expired; start explicitly")
	}
	l.lease = now.Add(d)
	return nil
}

type ruleRuntime struct {
	discoveryID              string
	discoveryPins            map[netip.Addr]string
	ttlSeconds, leaseSeconds int64
	config                   config.Rule
	status                   RuleStatus
	server                   *transport.Server
	policy                   *policy.Policy
	life                     *lifetime
	desired                  bool
}
type ruleManager struct {
	s           *Service
	entries     map[string]*ruleRuntime
	writeConfig func(string, []byte) error
}

func newRuleManager(s *Service) *ruleManager {
	m := &ruleManager{s: s, entries: map[string]*ruleRuntime{}}
	for _, r := range s.Config.Rules {
		m.entries[r.Name] = newRuleRuntime(r)
	}
	return m
}
func newRuleRuntime(r config.Rule) *ruleRuntime {
	return &ruleRuntime{config: r, status: RuleStatus{AllowedPeers: append([]config.PeerRef(nil), r.AllowedPeers...), Name: r.Name, Purpose: r.Purpose, Direction: r.Direction, Network: r.Network, State: "stopped", ReasonCode: "explicit-start-required", Reason: "Saved only; start explicitly", Target: config.Address(r.TargetHost, r.TargetPort), PeerID: r.PeerID, CheckedAt: time.Now().UTC(), Application: "unverified"}}
}

// saveConfig applies only configurations that were published. A committed
// durability error still means the destination changed, but must be returned
// unchanged so callers can report the uncertain durability to the user.
func (m *ruleManager) saveConfig(next config.Config) error {
	var err error
	if m.writeConfig == nil {
		err = config.Save(m.s.Dir, next)
	} else {
		err = config.SaveWith(m.s.Dir, next, m.writeConfig)
	}
	if err != nil && !errors.Is(err, config.ErrAtomicCommitted) {
		return err
	}

	saved := next.Disabled()
	m.s.Config = saved
	for _, r := range saved.Rules {
		if entry := m.entries[r.Name]; entry != nil {
			entry.config = r
			continue
		}
		m.entries[r.Name] = newRuleRuntime(r)
	}
	return err
}

func (m *ruleManager) statuses() []RuleStatus {
	out := make([]RuleStatus, 0, len(m.entries))
	for _, r := range m.entries {
		out = append(out, r.status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (m *ruleManager) close(r *ruleRuntime) {
	if r.server != nil {
		_ = r.server.Close()
		r.server = nil
	}
	r.status.ListenAddress = ""
}
func (m *ruleManager) stop(r *ruleRuntime, state, code, reason string) {
	r.desired = false
	if r.life != nil {
		r.life.stop()
	}
	m.close(r)
	r.status.State = state
	r.status.ReasonCode = code
	r.status.Reason = reason
	r.status.CheckedAt = time.Now().UTC()
}
func (m *ruleManager) closeAll(state, code, reason string) {
	for _, r := range m.entries {
		if r.desired || r.server != nil {
			m.stop(r, state, code, reason)
		}
	}
}
func (m *ruleManager) refreshStatus(backend string) {
	state, reason := "idle", "Select a saved connection to start"
	ready, pending, failed := 0, 0, 0
	for _, r := range m.entries {
		if r.status.State == "ready" {
			ready++
		} else if r.desired {
			pending++
		} else if r.status.State == "failed" {
			failed++
		}
	}
	switch {
	case ready > 0 && (pending > 0 || failed > 0):
		state, reason = "partial", "Some connections are ready; check each rule"
	case pending > 0:
		state, reason = "recovering", "Waiting for selected connections; check each rule"
	case ready > 0:
		state, reason = "ready", "Connection listeners ready; application behavior is unverified"
	case failed > 0:
		state, reason = "blocked", "A selected connection failed; review and start explicitly"
	}
	m.s.set(state, reason, backend)
}
func parseRuleCommand(text string) (RuleCommand, error) {
	var q RuleCommand
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(text, "rules:")))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&q); e != nil {
		return q, errors.New("invalid rule command")
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return q, errors.New("invalid trailing command data")
	}
	if q.Owner != "" && !config.ValidName(q.Owner) {
		return q, errors.New("invalid task owner")
	}
	return q, nil
}
func (m *ruleManager) selectRules(q RuleCommand) ([]*ruleRuntime, error) {
	names := append([]string(nil), q.Names...)
	if q.Group != "" {
		if len(names) > 0 {
			return nil, errors.New("choose rules or one group")
		}
		found := false
		for _, g := range m.s.Config.Groups {
			if g.Name == q.Group {
				names = append(names, g.Rules...)
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("unknown group")
		}
	}
	if len(names) == 0 || len(names) > 64 {
		return nil, errors.New("select 1..64 rules")
	}
	seen := map[string]bool{}
	out := make([]*ruleRuntime, 0, len(names))
	for _, n := range names {
		if seen[n] {
			return nil, errors.New("duplicate rule selection")
		}
		seen[n] = true
		r := m.entries[n]
		if r == nil {
			return nil, fmt.Errorf("unknown rule %q", n)
		}
		out = append(out, r)
	}
	return out, nil
}
func (m *ruleManager) command(ctx context.Context, q RuleCommand) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch q.Action {
	case "save":
		if q.Rule == nil {
			return nil, errors.New("rule required")
		}
		r := *q.Rule
		r.Enabled = false
		if old := m.entries[r.Name]; old != nil && !q.Replace {
			return nil, errors.New("rule already exists; review replacement and use --replace")
		}
		if old := m.entries[r.Name]; old != nil && old.desired {
			return nil, errors.New("stop the rule before replacing it")
		}
		next := m.s.Config
		next.Rules = append([]config.Rule(nil), next.Rules...)
		found := false
		for i := range next.Rules {
			if next.Rules[i].Name == r.Name {
				next.Rules[i] = r
				found = true
				break
			}
		}
		if !found {
			next.Rules = append(next.Rules, r)
		}
		e := m.saveConfig(next)
		if e != nil && !errors.Is(e, config.ErrAtomicCommitted) {
			return nil, e
		}
		m.entries[r.Name] = newRuleRuntime(r)
		if e != nil {
			return nil, e
		}
	case "group-save":
		if q.GroupConfig == nil {
			return nil, errors.New("group required")
		}
		next := m.s.Config
		next.Groups = append([]config.Group(nil), next.Groups...)
		found := false
		for i := range next.Groups {
			if next.Groups[i].Name == q.GroupConfig.Name {
				if !q.Replace {
					return nil, errors.New("group already exists; review replacement and use --replace")
				}
				next.Groups[i] = *q.GroupConfig
				found = true
				break
			}
		}
		if !found {
			next.Groups = append(next.Groups, *q.GroupConfig)
		}
		if e := m.saveConfig(next); e != nil {
			return nil, e
		}
	case "start":
		m.expire()
		if q.TTLSeconds < 0 || q.TTLSeconds > 86400 || q.LeaseSeconds < 0 || q.LeaseSeconds > 300 {
			return nil, errors.New("TTL must be 0..86400 seconds; lease 0..300 seconds")
		}
		if q.LeaseSeconds > 0 && q.Owner == "" {
			return nil, errors.New("task lease requires owner")
		}
		selected, e := m.selectRules(q)
		if e != nil {
			return nil, e
		}
		reviewed := make([]config.Rule, 0, len(selected))
		for _, r := range selected {
			reviewed = append(reviewed, r.config)
		}
		if q.ExpectedRules == "" || q.ExpectedRules != config.RulesDigest(reviewed) {
			return nil, errors.New("selected rules changed or were not reviewed; show the current scope and confirm again")
		}
		for _, r := range selected {
			if r.desired && r.status.Owner != q.Owner {
				return nil, errors.New("selected rule belongs to another task")
			}
			if r.desired && (r.ttlSeconds != q.TTLSeconds || r.leaseSeconds != q.LeaseSeconds) {
				return nil, errors.New("rule is already active with a different lifetime; stop it before confirming a new lifetime")
			}
			if r.config.Direction == "share" && q.TTLSeconds == 0 {
				return nil, errors.New("sharing requires an explicit TTL of 1..86400 seconds")
			}
		}
		st, e := m.s.Node.State(ctx)
		if e != nil || !st.Snapshot.Running {
			return nil, errors.New("tailnet is not ready; sign in and retry")
		}
		var started []*ruleRuntime
		for _, r := range selected {
			if r.desired {
				continue
			}
			now := time.Now()
			r.life = &lifetime{}
			r.discoveryID = ""
			r.discoveryPins = nil
			r.ttlSeconds = q.TTLSeconds
			r.status.TTLSeconds = q.TTLSeconds
			r.status.LeaseSeconds = q.LeaseSeconds
			r.status.ScopeDigest = config.RulesDigest([]config.Rule{r.config})
			r.leaseSeconds = q.LeaseSeconds
			r.status.Owner = q.Owner
			r.status.ExpiresAt = time.Time{}
			if q.TTLSeconds > 0 {
				r.life.expires = now.Add(time.Duration(q.TTLSeconds) * time.Second)
				r.status.ExpiresAt = r.life.expires.UTC()
			}
			if q.LeaseSeconds > 0 {
				r.life.lease = now.Add(time.Duration(q.LeaseSeconds) * time.Second)
			}
			r.life.activate(m.s.runCtx)
			r.desired = true
			if e := m.start(ctx, r, st); e != nil {
				m.stop(r, "failed", startReasonCode(e), safeStartReason(e))
				for _, prev := range started {
					m.stop(prev, "stopped", "rolled-back", "Group start failed; newly started listener closed")
				}
				m.refreshStatus(st.Backend)
				return m.s.Status(), nil
			}
			started = append(started, r)
		}
	case "stop", "renew":
		selected, e := m.selectRules(q)
		if e != nil {
			return nil, e
		}
		for _, r := range selected {
			if r.status.Owner != q.Owner {
				return nil, errors.New("owner mismatch; another task's connection was left unchanged")
			}
		}
		if q.Action == "renew" {
			if q.Owner == "" || q.LeaseSeconds < 1 || q.LeaseSeconds > 300 {
				return nil, errors.New("renew needs task owner and lease 1..300 seconds")
			}
			for _, r := range selected {
				if !r.desired || r.life == nil {
					return nil, errors.New("cannot renew a stopped connection")
				}
				if e := r.life.check(); e != nil {
					return nil, e
				}
			}
			for _, r := range selected {
				if e := r.life.renew(time.Duration(q.LeaseSeconds) * time.Second); e != nil {
					return nil, e
				}
			}
		} else {
			for _, r := range selected {
				m.stop(r, "stopped", "explicit-stop", "Stopped; start explicitly to resume")
			}
		}
	case "stop-shares":
		if q.Owner != "" {
			return nil, errors.New("stop-shares is an explicit manual global stop")
		}
		for _, r := range m.entries {
			if r.config.Direction == "share" {
				m.stop(r, "stopped", "explicit-stop", "Sharing stopped; start explicitly to resume")
			}
		}
	default:
		return nil, errors.New("unknown rule action")
	}
	m.refreshStatus(m.s.Status().Backend)
	return m.s.Status(), nil
}

var errIdentity = errors.New("peer identity not permitted")
var errBind = errors.New("listener unavailable")

func safeStartReason(e error) string {
	if errors.Is(e, errIdentity) {
		return "Selected peer identity is unavailable or changed; select the peer again"
	}
	if errors.Is(e, errBind) {
		return "Selected port is unavailable; confirm a different port before retrying"
	}
	return "Connection could not start; check the selected service, peer and permissions"
}
func (m *ruleManager) source(ctx context.Context) (policy.Snapshot, error) {
	st, e := m.s.Node.State(ctx)
	return st.Snapshot, e
}
func (m *ruleManager) start(ctx context.Context, r *ruleRuntime, st identity.State) error {
	if e := r.life.check(); e != nil {
		return e
	}
	cfg := r.config
	var server *transport.Server
	var e error
	if cfg.Direction == "forward" {
		r.policy = &policy.Policy{Rules: []policy.Rule{{Host: cfg.TargetHost, Port: cfg.TargetPort, Network: cfg.Network, PeerID: cfg.PeerID}}, Source: m.source, DialIP: m.s.Node.DialIP, Guard: r.life.check, OnRevoked: func() { r.life.fail("peer-identity-changed") }}
		target := config.Address(cfg.TargetHost, cfg.TargetPort)
		if e = r.policy.Validate(ctx, cfg.Network, target); e != nil {
			return errIdentity
		}
		if cfg.Network == "tcp" {
			conn, err := r.policy.Dial(ctx, "tcp", target)
			if err != nil {
				return err
			}
			_ = conn.Close()
			server, e = transport.StartTCP(r.life.ctx, transport.TCPConfig{ListenAddress: config.Loopback(cfg.ListenPort), Target: target}, r.policy.Dial)
		} else {
			server, e = transport.StartUDP(r.life.ctx, transport.UDPConfig{ListenAddress: config.Loopback(cfg.ListenPort), Target: target, Validate: r.policy.Validate}, r.policy.Dial)
		}
		if e != nil {
			return errBind
		}
	} else {
		inbound, ok := m.s.Node.(identity.InboundBackend)
		if !ok {
			return errors.New("inbound backend unavailable")
		}
		if e = validateAllowed(st.Snapshot, cfg.AllowedPeers); e != nil {
			return errIdentity
		}
		self := netip.Addr{}
		for _, ip := range st.IPs {
			if config.TailnetIP(ip) {
				self = ip
				if ip.Is4() {
					break
				}
			}
		}
		if !self.IsValid() {
			return errors.New("local tailnet address unavailable")
		}
		address := config.Address(self.String(), cfg.ListenPort)
		target := config.Address(cfg.TargetHost, cfg.TargetPort)
		// Keep the original address/identity grant through automatic recovery.
		// New addresses require another explicit start, not a wider reconnect.
		if r.discoveryPins == nil {
			r.discoveryPins = pinAllowedSources(st.Snapshot, cfg.AllowedPeers)
		}
		pinned := r.discoveryPins
		if discoveryPinsRevoked(st.Snapshot, pinned) {
			r.life.fail("peer-identity-changed")
			return errIdentity
		}
		authorize := func(c context.Context, source netip.AddrPort) error {
			if err := r.life.check(); err != nil {
				return err
			}
			snap, err := m.source(c)
			if err != nil {
				return errIdentity
			}
			if snap.Running {
				if validateAllowed(snap, cfg.AllowedPeers) != nil {
					r.life.fail("peer-identity-changed")
					return errIdentity
				}
				if pinned[source.Addr()] != "" {
					if e := authorizePinnedSource(snap, cfg.AllowedPeers, pinned, source.Addr()); e != nil {
						r.life.fail("peer-identity-changed")
						return e
					}
				}
			}
			return authorizePinnedSource(snap, cfg.AllowedPeers, pinned, source.Addr())
		}
		if cfg.Network == "tcp" {
			listener, err := inbound.Listen("tcp", address)
			if err != nil {
				return errBind
			}
			server, e = transport.StartInboundTCP(r.life.ctx, transport.TCPConfig{Target: target}, listener, authorize, r.life.check)
			if e != nil {
				listener.Close()
			}
		} else {
			packet, err := inbound.ListenPacket("udp", address)
			if err != nil {
				return errBind
			}
			server, e = transport.StartInboundUDP(r.life.ctx, transport.UDPConfig{Target: target}, packet, authorize, r.life.check)
			if e != nil {
				packet.Close()
			}
		}
		if e != nil {
			return e
		}
	}
	if err := ctx.Err(); err != nil {
		server.Close()
		return err
	}
	r.server = server
	r.status.State = "ready"
	r.status.ReasonCode = "listener-ready"
	r.status.Reason = "Listener ready; application behavior remains unverified"
	r.status.ListenAddress = server.Addr().String()
	r.status.CheckedAt = time.Now().UTC()
	if cfg.Direction == "share" && cfg.Discoverable {
		m.s.ensureDiscovery(st)
	}
	return nil
}
func validateAllowed(s policy.Snapshot, allowed []config.PeerRef) error {
	if !s.Running || len(allowed) == 0 {
		return errIdentity
	}
	for _, a := range allowed {
		found := false
		for _, p := range s.Peers {
			if p.ID == a.ID && !p.Expired {
				found = true
				break
			}
		}
		if !found {
			return errIdentity
		}
	}
	return nil
}
func authorizeSource(s policy.Snapshot, allowed []config.PeerRef, ip netip.Addr) error {
	if !s.Running || !config.TailnetIP(ip) {
		return errIdentity
	}
	found := ""
	for _, p := range s.Peers {
		if p.Expired || p.ID == "" {
			continue
		}
		for _, a := range p.IPs {
			if a == ip {
				if found != "" && found != p.ID {
					return errIdentity
				}
				found = p.ID
			}
		}
	}
	if found == "" {
		return errIdentity
	}
	for _, p := range allowed {
		if p.ID == found {
			return nil
		}
	}
	return errIdentity
}
func (m *ruleManager) check(ctx context.Context, st identity.State, readErr error) bool {
	snapshotHealthy := readErr == nil && st.Snapshot.Running
	healthy := snapshotHealthy
	for _, r := range m.entries {
		if !r.desired {
			continue
		}
		if e := r.life.check(); e != nil {
			m.finishLifetime(r, e)
			continue
		}
		if !snapshotHealthy {
			m.close(r)
			r.status.State = "recovering"
			r.status.ReasonCode = "peer-information-unavailable"
			r.status.Reason = "Tailnet information unavailable; traffic closed"
			continue
		}
		if r.config.Direction == "share" {
			if r.server != nil {
				addr, err := netip.ParseAddrPort(r.server.Addr().String())
				found := false
				for _, ip := range st.IPs {
					if err == nil && ip == addr.Addr() {
						found = true
					}
				}
				if !found {
					m.close(r)
				}
			}
			if e := validateAllowed(st.Snapshot, r.config.AllowedPeers); e != nil {
				m.stop(r, "failed", "peer-identity-changed", "Allowed peer identity disappeared or expired; review before restarting")
				continue
			}
		}
		if r.policy != nil {
			target := config.Address(r.config.TargetHost, r.config.TargetPort)
			if e := r.policy.ValidateSnapshot(st.Snapshot, r.config.Network, target); e != nil {
				m.stop(r, "failed", "peer-identity-changed", "Selected peer identity disappeared or changed; review before restarting")
				continue
			}
			if e := r.policy.RevalidateActive(ctx); e != nil {
				m.close(r)
			}
		}
		if r.server != nil {
			select {
			case <-r.server.Done():
				m.close(r)
			default:
			}
		}
		if r.server == nil {
			if e := m.start(ctx, r, st); e != nil {
				r.status.State = "recovering"
				r.status.ReasonCode = "reconnect-pending"
				r.status.Reason = safeStartReason(e)
				healthy = false
			}
		}
		r.status.CheckedAt = time.Now().UTC()
	}
	m.refreshStatus(st.Backend)
	return healthy
}

func (m *ruleManager) expire() {
	changed := false
	for _, r := range m.entries {
		if r.desired && r.life != nil {
			if e := r.life.check(); e != nil {
				m.finishLifetime(r, e)
				changed = true
			}
		}
	}
	if changed {
		m.refreshStatus(m.s.Status().Backend)
	}
}

func pinAllowedSources(s policy.Snapshot, allowed []config.PeerRef) map[netip.Addr]string {
	pinned := map[netip.Addr]string{}
	for _, a := range allowed {
		for _, p := range s.Peers {
			if p.ID == a.ID && !p.Expired {
				for _, ip := range p.IPs {
					if config.TailnetIP(ip) {
						if old, ok := pinned[ip]; ok && old != p.ID {
							pinned[ip] = ""
						} else {
							pinned[ip] = p.ID
						}
					}
				}
			}
		}
	}
	return pinned
}
func authorizePinnedSource(s policy.Snapshot, allowed []config.PeerRef, pinned map[netip.Addr]string, ip netip.Addr) error {
	if e := authorizeSource(s, allowed, ip); e != nil {
		return e
	}
	id := pinned[ip]
	if id == "" {
		return errIdentity
	}
	for _, p := range s.Peers {
		for _, a := range p.IPs {
			if a == ip && (p.ID != id || p.Expired) {
				return errIdentity
			}
		}
	}
	return nil
}

func startReasonCode(err error) string {
	if errors.Is(err, errIdentity) {
		return "peer-identity-unavailable"
	}
	if errors.Is(err, errBind) {
		return "port-unavailable"
	}
	return "service-unreachable"
}

// diagnose probes only already-started TCP rules. UDP cannot be meaningfully
// proven reachable with an empty generic datagram, and remains unverified.
func (m *ruleManager) diagnose(ctx context.Context) {
	for _, r := range m.entries {
		if !r.desired || r.server == nil || r.config.Network != "tcp" {
			continue
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		var conn net.Conn
		var err error
		target := config.Address(r.config.TargetHost, r.config.TargetPort)
		if r.config.Direction == "forward" {
			conn, err = r.policy.Dial(check, "tcp", target)
		} else {
			if _, e := netip.ParseAddrPort(target); e != nil {
				err = e
			} else {
				conn, err = (&net.Dialer{}).DialContext(check, "tcp", target)
			}
		}
		if conn != nil {
			conn.Close()
		}
		cancel()
		if err != nil {
			m.close(r)
			r.status.State = "recovering"
			r.status.ReasonCode = "tcp-unreachable"
			r.status.Reason = "TCP service did not accept a connection; check application listener and permissions"
		} else {
			r.status.ReasonCode = "tcp-reachable"
			r.status.Reason = "TCP accepted a connection; application behavior remains unverified"
		}
		r.status.CheckedAt = time.Now().UTC()
	}
	m.refreshStatus(m.s.Status().Backend)
}

// Every grant has an independent canceler. A slow diagnostic or another rule's
// startup cannot delay closing established flows after expiry or caller death.
func (l *lifetime) activate(parent context.Context) {
	l.ctx, l.cancel = context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-l.ctx.Done():
				return
			case <-ticker.C:
				if l.check() != nil {
					l.cancel()
					return
				}
			}
		}
	}()
}

func (l *lifetime) fail(code string) {
	l.mu.Lock()
	if l.failure == "" {
		l.failure = code
	}
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (m *ruleManager) finishLifetime(r *ruleRuntime, err error) {
	if err.Error() == "peer-identity-changed" {
		m.stop(r, "failed", "peer-identity-changed", "A selected peer identity or address changed; review and start explicitly")
	} else {
		m.stop(r, "expired", err.Error(), "Connection expired; start explicitly to resume")
	}
}
