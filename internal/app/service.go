package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/control"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"github.com/webkaz-labs/tsnet-bridge/internal/transport"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Status struct {
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	Backend   string    `json:"tailnet_state"`
	Mode      string    `json:"mode"`
	Listeners []string  `json:"listeners,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	NextCheck time.Time `json:"next_check,omitempty"`
	RustDesk  string    `json:"rustdesk"`
}
type command struct {
	ctx    context.Context
	name   string
	result chan result
}
type result struct {
	value any
	err   error
}
type Service struct {
	Dir      string
	Config   config.Config
	Node     identity.Backend
	mu       sync.RWMutex
	status   Status
	authURL  string
	runCtx   context.Context
	commands chan command
	forwards []*transport.Server
	p        *policy.Policy
}

func (s *Service) set(state, reason, backend string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = state
	s.status.Reason = reason
	s.status.Backend = backend
	s.status.CheckedAt = time.Now().UTC()
	s.status.RustDesk = "unverified"
	s.status.Mode = s.Config.Mode
	s.status.Listeners = nil
	for _, f := range s.forwards {
		s.status.Listeners = append(s.status.Listeners, f.Addr().String())
	}
}
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.status
	v.Listeners = append([]string(nil), v.Listeners...)
	return v
}
func (s *Service) Run(ctx context.Context) error {
	lock, e := config.AcquireLock(s.Dir)
	if e != nil {
		return e
	}
	defer lock.Close()
	s.runCtx = ctx
	s.commands = make(chan command)
	s.set("starting", "Starting embedded tailnet node", "Starting")
	ipc, e := control.Serve(ctx, s.Dir, s.handle)
	if e != nil {
		return e
	}
	defer ipc.Close()
	defer s.Node.Close()
	if e = s.Node.Start(); e != nil {
		s.set("error", "Node startup failed; state retained. Check permissions and retry.", "Error")
		return errors.New("node startup failed; state retained")
	}
	defer s.closeForwards()
	rules := []policy.Rule{{Host: s.Config.IDHost, Port: s.Config.IDPort - 1, Network: "tcp"}, {Host: s.Config.IDHost, Port: s.Config.IDPort, Network: "tcp"}, {Host: s.Config.RelayHost, Port: s.Config.RelayPort, Network: "tcp"}}
	if s.Config.Mode == "forward" {
		rules = append(rules, policy.Rule{Host: s.Config.IDHost, Port: s.Config.IDPort, Network: "udp"})
	}
	s.p = &policy.Policy{Rules: rules, Source: func(c context.Context) (policy.Snapshot, error) { st, e := s.Node.State(c); return st.Snapshot, e }, DialIP: s.Node.DialIP}
	tick := time.NewTimer(0)
	defer tick.Stop()
	retry := time.Second
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-s.commands:
			out, err, exit := s.execute(c.ctx, c.name)
			c.result <- result{out, err}
			if exit {
				return nil
			}
		case <-tick.C:
			healthy := s.check(ctx)
			delay := 5 * time.Second
			if !healthy {
				delay = time.Duration(float64(retry) * (0.8 + rand.Float64()*0.4))
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
				retry *= 2
				if retry > 30*time.Second {
					retry = 30 * time.Second
				}
			} else {
				retry = time.Second
			}
			s.mu.Lock()
			s.status.NextCheck = time.Now().Add(delay).UTC()
			s.mu.Unlock()
			tick.Reset(delay)
		}
	}
}
func (s *Service) handle(ctx context.Context, name string) (any, error) {
	if name == "status" {
		return s.Status(), nil
	}
	if name == "auth-url" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return map[string]string{"url": s.authURL}, nil
	}
	switch name {
	case "stop", "logout", "login", "doctor", "reconnect":
	default:
		return nil, errors.New("unknown command")
	}
	c := command{ctx: ctx, name: name, result: make(chan result, 1)}
	select {
	case s.commands <- c:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-c.result:
		return r.value, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Service) execute(ctx context.Context, name string) (any, error, bool) {
	switch name {
	case "stop":
		s.closeForwards()
		s.set("stopped", "Stopped; saved login retained", "")
		return s.Status(), nil, true
	case "logout":
		s.closeForwards()
		e := s.Node.Logout(ctx)
		if e != nil {
			s.set("stopped", "Local forwarding stopped; server-side logout unconfirmed. Retry logout when connected.", "")
			return nil, errors.New("local forwarding stopped; server-side logout unconfirmed"), true
		}
		s.set("logged-out", "Logout confirmed by local API; node removal is a separate admin operation", "")
		return s.Status(), nil, true
	case "login":
		if e := s.Node.Login(ctx); e != nil {
			return nil, errors.New("could not start interactive login"), false
		}
		return s.Status(), nil, false
	case "reconnect":
		s.closeForwards()
		s.check(ctx)
		return s.Status(), nil, false
	case "doctor":
		s.check(ctx)
		return s.Status(), nil, false
	}
	return nil, errors.New("unknown command"), false
}
func (s *Service) check(ctx context.Context) bool {
	check, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	st, e := s.Node.State(check)
	if e != nil {
		s.closeForwards()
		s.set("recovering", "Cannot read node status; forwarding is closed", "")
		return false
	}
	s.mu.Lock()
	s.authURL = st.AuthURL
	s.mu.Unlock()
	if !st.Snapshot.Running {
		s.closeForwards()
		state := "recovering"
		reason := "Waiting for tailnet connection"
		if st.Backend == "NeedsLogin" {
			state = "needs-login"
			reason = "Run tsnet-bridge login to sign in"
		}
		if st.Backend == "NeedsMachineAuth" {
			state = "approval-required"
			reason = "Approve this node in the Tailscale admin console"
		}
		s.set(state, reason, st.Backend)
		return false
	}
	// Every health pass revalidates all identities; peer removal closes existing flows.
	for _, r := range s.p.Rules {
		if e = s.p.Validate(check, r.Network, config.Address(r.Host, r.Port)); e != nil {
			s.closeForwards()
			s.set("blocked", "Configured server is not a current permitted peer; check host and tailnet policy", st.Backend)
			return false
		}
	}
	// Reachability is deliberately a TCP check, not a RustDesk end-to-end result.
	for _, r := range s.p.Rules {
		if r.Network != "tcp" {
			continue
		}
		c, e := s.p.Dial(check, r.Network, config.Address(r.Host, r.Port))
		if e != nil {
			s.closeForwards()
			s.set("recovering", fmt.Sprintf("TCP %d unreachable; check server listener and tailnet policy", r.Port), st.Backend)
			return false
		}
		c.Close()
	}
	for _, f := range s.forwards {
		select {
		case <-f.Done():
			s.closeForwards()
		default:
		}
		if len(s.forwards) == 0 {
			break
		}
	}
	if e = s.p.RevalidateActive(check); e != nil {
		s.closeForwards()
		s.set("blocked", "An active peer identity changed; forwarding closed", st.Backend)
		return false
	}
	if len(s.forwards) == 0 {
		if e = s.startForwards(s.runCtx); e != nil {
			s.closeForwards()
			s.set("blocked", "Could not bind saved loopback ports; stop the conflicting app or change the shared profile", st.Backend)
			return false
		}
	}
	s.set("ready", "TCP server reachability checked; RustDesk screen/control remains unverified", st.Backend)
	return true
}
func (s *Service) startForwards(ctx context.Context) error {
	add := func(v *transport.Server, e error) error {
		if e == nil {
			s.forwards = append(s.forwards, v)
		}
		return e
	}
	if s.Config.Mode == "socks" {
		var creds config.Credentials
		if e := config.ReadJSON(filepath.Join(s.Dir, "credentials.json"), &creds); e != nil {
			return e
		}
		return add(transport.StartSOCKS(ctx, transport.SOCKSConfig{ListenAddress: config.Loopback(s.Config.SOCKSPort), Username: creds.Username, Password: creds.Password}, s.p.Dial))
	}
	for _, f := range []struct {
		local, remote int
		host          string
	}{{s.Config.LocalIDPort - 1, s.Config.IDPort - 1, s.Config.IDHost}, {s.Config.LocalIDPort, s.Config.IDPort, s.Config.IDHost}, {s.Config.LocalRelayPort, s.Config.RelayPort, s.Config.RelayHost}} {
		if e := add(transport.StartTCP(ctx, transport.TCPConfig{ListenAddress: config.Loopback(f.local), Target: config.Address(f.host, f.remote)}, s.p.Dial)); e != nil {
			return e
		}
	}
	return add(transport.StartUDP(ctx, transport.UDPConfig{ListenAddress: config.Loopback(s.Config.LocalIDPort), Target: config.Address(s.Config.IDHost, s.Config.IDPort), Validate: s.p.Validate}, s.p.Dial))
}
func (s *Service) closeForwards() {
	for _, f := range s.forwards {
		_ = f.Close()
	}
	s.forwards = nil
}

// Preflight claims every saved port before enrollment and immediately releases it.
func Preflight(c config.Config) error {
	var closers []interface{ Close() error }
	defer func() {
		for _, v := range closers {
			v.Close()
		}
	}()
	ports := []int{c.SOCKSPort}
	if c.Mode == "forward" {
		ports = []int{c.LocalIDPort - 1, c.LocalIDPort, c.LocalRelayPort}
	}
	for _, p := range ports {
		l, e := net.Listen("tcp4", config.Loopback(p))
		if e != nil {
			return fmt.Errorf("loopback TCP %d unavailable", p)
		}
		closers = append(closers, l)
	}
	if c.Mode == "forward" {
		l, e := net.ListenPacket("udp4", config.Loopback(c.LocalIDPort))
		if e != nil {
			return fmt.Errorf("loopback UDP %d unavailable", c.LocalIDPort)
		}
		closers = append(closers, l)
	}
	return nil
}
func EnsureCredentials(dir string) error {
	p := filepath.Join(dir, "credentials.json")
	var c config.Credentials
	e := config.ReadJSON(p, &c)
	if e == nil {
		if len(c.Username) < 8 || len(c.Password) < 24 {
			return errors.New("invalid saved SOCKS credentials")
		}
		return config.Protect(p, false)
	}
	if !os.IsNotExist(e) {
		return e
	}
	c, e = config.NewCredentials()
	if e != nil {
		return e
	}
	return config.WriteJSON(p, c)
}
