package app

import (
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"io"
	"net"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"
)

func ruleFixture(t *testing.T) (*Service, *fakeNode) {
	t.Helper()
	c, e := config.NewRules()
	if e != nil {
		t.Fatal(e)
	}
	n := &fakeNode{state: identity.State{Backend: "Running", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.1")}, Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "peer1", DNSName: "server.example.ts.net", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.2")}}}}}}
	ctx, cancel := context.WithCancel(t.Context())
	s := &Service{Dir: t.TempDir(), Config: c, Node: n, runCtx: ctx}
	s.rules = newRuleManager(s)
	s.set("idle", "", "Running")
	t.Cleanup(func() { cancel(); s.closeForwards() })
	return s, n
}
func freePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
func testRule(t *testing.T, name string) config.Rule {
	return config.Rule{Name: name, Purpose: "web", Direction: "forward", Network: "tcp", ListenPort: freePort(t), TargetHost: "server.example.ts.net", TargetPort: 80, PeerID: "peer1"}
}
func saveRule(t *testing.T, s *Service, r config.Rule) {
	t.Helper()
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "save", Rule: &r}); e != nil {
		t.Fatal(e)
	}
}
func startRules(t *testing.T, s *Service, q RuleCommand) {
	t.Helper()
	q.Action = "start"
	selected, e := s.rules.selectRules(q)
	if e != nil {
		t.Fatal(e)
	}
	rules := []config.Rule{}
	for _, r := range selected {
		rules = append(rules, r.config)
	}
	q.ExpectedRules = config.RulesDigest(rules)
	v, e := s.rules.command(t.Context(), q)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range v.(Status).Rules {
		if r.State == "failed" {
			t.Fatal(r)
		}
	}
}

func TestRuleSaveDoesNotStartOrRestore(t *testing.T) {
	s, _ := ruleFixture(t)
	r := testRule(t, "web")
	r.Enabled = true
	saveRule(t, s, r)
	if s.rules.entries[r.Name].desired || s.rules.entries[r.Name].server != nil {
		t.Fatal("saved rule started")
	}
	saved, e := config.Load(s.Dir)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Rules[0].Enabled {
		t.Fatal("saved grant restored")
	}
	s.Config = saved
	s.rules = newRuleManager(s)
	if s.rules.entries[r.Name].desired {
		t.Fatal("restart restored grant")
	}
}
func TestGroupRollbackPreservesExistingOwner(t *testing.T) {
	s, _ := ruleFixture(t)
	existing, a, b := testRule(t, "existing"), testRule(t, "first"), testRule(t, "blocked")
	for _, r := range []config.Rule{existing, a, b} {
		saveRule(t, s, r)
	}
	startRules(t, s, RuleCommand{Names: []string{"existing"}, Owner: "task-a"})
	old := s.rules.entries["existing"].server
	conflict, e := net.Listen("tcp4", config.Loopback(b.ListenPort))
	if e != nil {
		t.Fatal(e)
	}
	defer conflict.Close()
	v, e := s.rules.command(t.Context(), RuleCommand{Action: "start", ExpectedRules: config.RulesDigest([]config.Rule{a, b}), Names: []string{"first", "blocked"}, Owner: "task-b"})
	if e != nil {
		t.Fatal(e)
	}
	if s.rules.entries["first"].server != nil || s.rules.entries["first"].desired {
		t.Fatal("partial listener survived")
	}
	if s.rules.entries["blocked"].status.State != "failed" || s.rules.entries["first"].status.ReasonCode != "rolled-back" {
		t.Fatal(v)
	}
	if s.rules.entries["existing"].server != old {
		t.Fatal("unrelated work stopped")
	}
	if _, e = s.rules.command(t.Context(), RuleCommand{Action: "stop", Names: []string{"existing"}, Owner: "task-b"}); e == nil {
		t.Fatal("owner bypass")
	}
	for range 2 {
		if _, e = s.rules.command(t.Context(), RuleCommand{Action: "stop", Names: []string{"existing"}, Owner: "task-a"}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRuleIdentityReplacementAndReconnect(t *testing.T) {
	s, n := ruleFixture(t)
	r := testRule(t, "web")
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{"web"}})
	n.mu.Lock()
	n.state.Snapshot.Peers[0].ID = "replacement"
	n.mu.Unlock()
	s.check(t.Context())
	entry := s.rules.entries["web"]
	if entry.desired || entry.server != nil || entry.status.ReasonCode != "peer-identity-changed" {
		t.Fatal(entry.status)
	}
	n.mu.Lock()
	n.state.Snapshot.Peers[0].ID = "peer1"
	n.mu.Unlock()
	s.check(t.Context())
	if entry.server != nil {
		t.Fatal("revoked rule silently restarted")
	}
}
func TestCommonOutageRecoversSamePeer(t *testing.T) {
	s, n := ruleFixture(t)
	r := testRule(t, "web")
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{"web"}})
	n.mu.Lock()
	n.state.Snapshot.Running = false
	n.mu.Unlock()
	s.check(t.Context())
	entry := s.rules.entries["web"]
	if !entry.desired || entry.server != nil {
		t.Fatal(entry.status)
	}
	n.mu.Lock()
	n.state.Snapshot.Running = true
	n.mu.Unlock()
	s.check(t.Context())
	if entry.server == nil || entry.status.State != "ready" {
		t.Fatal(entry.status)
	}
	s.rules.command(t.Context(), RuleCommand{Action: "stop", Names: []string{"web"}})
	s.execute(t.Context(), "reconnect")
	if entry.server != nil {
		t.Fatal("explicit stop resurrected")
	}
}
func TestRuleTTLAndLeaseNeverRevive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		l := &lifetime{expires: now.Add(5 * time.Second), lease: now.Add(time.Second)}
		time.Sleep(time.Second)
		if l.check() == nil {
			t.Fatal("lease didn't expire")
		}
		if l.renew(time.Second) == nil {
			t.Fatal("expired lease revived")
		}
		l = &lifetime{expires: time.Now().Add(time.Second)}
		time.Sleep(time.Second)
		if l.check() == nil {
			t.Fatal("TTL didn't expire")
		}
		l = &lifetime{}
		l.stop()
		if l.check() == nil {
			t.Fatal("stop ignored")
		}
	})
	now := time.Now()
	if !deadlinePassed(now, now.Add(-time.Second).UTC()) {
		t.Fatal("wall expiry ignored")
	}
}
func TestShareRequiresTTLAndBoundedOwner(t *testing.T) {
	s, _ := ruleFixture(t)
	r := config.Rule{Name: "api", Purpose: "ai", Direction: "share", Network: "tcp", ListenPort: 8000, TargetHost: "127.0.0.1", TargetPort: 8000, AllowedPeers: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}}
	saveRule(t, s, r)
	for _, q := range []RuleCommand{{Action: "start", Names: []string{"api"}}, {Action: "start", Names: []string{"api"}, TTLSeconds: 86401}, {Action: "start", Names: []string{"api"}, TTLSeconds: 5, LeaseSeconds: 30}} {
		q.ExpectedRules = config.RulesDigest([]config.Rule{r})
		if _, e := s.rules.command(t.Context(), q); e == nil {
			t.Fatal("invalid lifetime accepted", q)
		}
	}
}
func TestIncomingSourcePinnedAgainstReassignment(t *testing.T) {
	ip := netip.MustParseAddr("100.64.1.2")
	s := policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "a", IPs: []netip.Addr{ip}}, {ID: "b", IPs: []netip.Addr{netip.MustParseAddr("100.64.1.3")}}}}
	allowed := []config.PeerRef{{ID: "a"}, {ID: "b"}}
	pinned := pinAllowedSources(s, allowed)
	if e := authorizePinnedSource(s, allowed, pinned, ip); e != nil {
		t.Fatal(e)
	}
	s.Peers[0].IPs = nil
	s.Peers[1].IPs = []netip.Addr{ip}
	if authorizePinnedSource(s, allowed, pinned, ip) == nil {
		t.Fatal("source tuple reassigned to another allowed peer")
	}
	if authorizeSource(s, allowed, netip.MustParseAddr("127.0.0.1")) == nil {
		t.Fatal("loopback source accepted")
	}
	s.Running = false
	if authorizeSource(s, allowed, ip) == nil {
		t.Fatal("offline source accepted")
	}
}
func TestRuleCommandRejectsUnknownAndTrailing(t *testing.T) {
	for _, text := range []string{`rules:{"action":"start","extra":true}`, `rules:{"action":"start"} {}`, `rules:{"owner":"../other"}`} {
		if _, e := parseRuleCommand(text); e == nil {
			t.Fatal(text)
		}
	}
	q, e := parseRuleCommand(`rules:{"action":"stop","names":["web"],"owner":"job-1"}`)
	if e != nil || q.Owner != "job-1" {
		t.Fatal(q, e)
	}
}
func TestRuleStatusNoAuthURLOrCredentials(t *testing.T) {
	s, _ := ruleFixture(t)
	s.authURL = "do-not-output"
	r := testRule(t, "web")
	saveRule(t, s, r)
	b, e := json.Marshal(s.Status())
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	json.Unmarshal(b, &fields)
	if _, ok := fields["auth_url"]; ok {
		t.Fatal("auth leaked")
	}
	if s.Status().Rules[0].Application != "unverified" {
		t.Fatal("application success invented")
	}
}
func TestRenewAfterExpireCannotReopen(t *testing.T) {
	s, _ := ruleFixture(t)
	r := testRule(t, "web")
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{"web"}, Owner: "job", LeaseSeconds: 30})
	entry := s.rules.entries["web"]
	entry.life.mu.Lock()
	entry.life.lease = time.Now().Add(-time.Second)
	entry.life.mu.Unlock()
	s.rules.expire()
	if entry.server != nil || entry.desired || entry.status.State != "expired" {
		t.Fatal(entry.status)
	}
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "renew", Names: []string{"web"}, Owner: "job", LeaseSeconds: 30}); e == nil {
		t.Fatal("renew resurrected")
	}
}

type inboundFake struct {
	*fakeNode
	bound net.Listener
}
type advertisedListener struct {
	net.Listener
	address net.Addr
}

func (l advertisedListener) Addr() net.Addr { return l.address }
func (l advertisedListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return advertisedConn{Conn: c}, nil
}

type advertisedConn struct{ net.Conn }

func (c advertisedConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("100.64.1.2"), Port: 12345}
}
func (c advertisedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return nil
}
func (n *inboundFake) Listen(network, address string) (net.Listener, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	n.bound = l
	ap, e := netip.ParseAddrPort(address)
	if e != nil {
		l.Close()
		return nil, e
	}
	return advertisedListener{Listener: l, address: net.TCPAddrFromAddrPort(ap)}, nil
}
func (n *inboundFake) ListenPacket(network, address string) (net.PacketConn, error) {
	return nil, net.ErrClosed
}
func TestShareEndToEndMockIdentityAndExpiry(t *testing.T) {
	s, n := ruleFixture(t)
	node := &inboundFake{fakeNode: n}
	s.Node = node
	service, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := service.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()
	r := config.Rule{Name: "api", Purpose: "ai", Direction: "share", Network: "tcp", ListenPort: 8000, TargetHost: "127.0.0.1", TargetPort: service.Addr().(*net.TCPAddr).Port, AllowedPeers: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}}
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{"api"}, TTLSeconds: 60})
	entry := s.rules.entries["api"]
	if len(s.Status().Rules[0].AllowedPeers) != 1 {
		t.Fatal("share recipients missing")
	}
	c, e := net.Dial("tcp4", node.bound.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	c.Write([]byte("hello"))
	b := make([]byte, 5)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "hello" {
		t.Fatal(string(b), e)
	}
	entry.life.mu.Lock()
	entry.life.expires = time.Now().Add(-time.Second)
	entry.life.mu.Unlock()
	s.rules.expire()
	if entry.server != nil || entry.desired || entry.status.State != "expired" {
		t.Fatal(entry.status)
	}
	if n, e := c.Read(b); n != 0 || e == nil {
		t.Fatal("expired connection survived", n, e)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("local service flow not closed")
	}
	s.execute(t.Context(), "reconnect")
	if entry.server != nil {
		t.Fatal("expiry resurrected")
	}
}
func TestRuleStatusOwnsAllowedPeersSnapshot(t *testing.T) {
	s, _ := ruleFixture(t)
	r := config.Rule{Name: "api", Purpose: "custom", Direction: "share", Network: "udp", ListenPort: 9000, TargetHost: "127.0.0.1", TargetPort: 9000, AllowedPeers: []config.PeerRef{{ID: "peer1", Host: "server.example.ts.net"}}}
	saveRule(t, s, r)
	st := s.Status()
	st.Rules[0].AllowedPeers[0].ID = "changed"
	if s.Status().Rules[0].AllowedPeers[0].ID != "peer1" {
		t.Fatal("status mutated grant")
	}
}
func TestSaveCannotOverwriteWithoutExplicitReplace(t *testing.T) {
	s, _ := ruleFixture(t)
	r := testRule(t, "web")
	saveRule(t, s, r)
	r.TargetPort = 443
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "save", Rule: &r}); e == nil {
		t.Fatal("implicit replacement")
	}
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "save", Rule: &r, Replace: true}); e != nil {
		t.Fatal(e)
	}
}

func TestLifetimeCancellationIndependentOfManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &lifetime{expires: time.Now().Add(time.Second)}
		l.activate(t.Context())
		time.Sleep(time.Second)
		synctest.Wait()
		if l.ctx.Err() == nil {
			t.Fatal("expiration waited for manager commands")
		}
		l.stop()
	})
}
func TestLifetimeRenewalDoesNotCancelEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &lifetime{lease: time.Now().Add(time.Second)}
		l.activate(t.Context())
		time.Sleep(500 * time.Millisecond)
		if e := l.renew(time.Second); e != nil {
			t.Fatal(e)
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if l.ctx.Err() != nil {
			t.Fatal("renewal ignored")
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if l.ctx.Err() == nil {
			t.Fatal("renewed expiry ignored")
		}
		l.stop()
	})
}

func TestStartRejectsChangedReviewedShareOrGroup(t *testing.T) {
	s, _ := ruleFixture(t)
	r := testRule(t, "reviewed")
	saveRule(t, s, r)
	q := RuleCommand{Action: "start", Names: []string{r.Name}, ExpectedRules: config.RulesDigest([]config.Rule{r})}
	r.TargetPort = 443
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "save", Rule: &r, Replace: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.rules.command(t.Context(), q); e == nil {
		t.Fatal("changed rule started")
	}
	if s.rules.entries[r.Name].desired {
		t.Fatal("rejected consent mutated runtime")
	}
	other := testRule(t, "other")
	saveRule(t, s, other)
	g := config.Group{Name: "group", Rules: []string{r.Name, other.Name}}
	if _, e := s.rules.command(t.Context(), RuleCommand{Action: "group-save", GroupConfig: &g}); e != nil {
		t.Fatal(e)
	}
	q.Names = nil
	q.Group = "group"
	q.ExpectedRules = config.RulesDigest([]config.Rule{r})
	if _, e := s.rules.command(t.Context(), q); e == nil {
		t.Fatal("expanded group started")
	}
	q.Group = ""
	q.Names = []string{r.Name}
	q.ExpectedRules = ""
	if _, e := s.rules.command(t.Context(), q); e == nil {
		t.Fatal("unreviewed start accepted")
	}
}

func TestRepeatedStartCannotSilentlyIgnoreNewLifetime(t *testing.T) {
	s, _ := ruleFixture(t)
	r := testRule(t, "web")
	saveRule(t, s, r)
	startRules(t, s, RuleCommand{Names: []string{r.Name}, Owner: "job", TTLSeconds: 3600, LeaseSeconds: 30})
	entry := s.rules.entries[r.Name]
	old := entry.status.ExpiresAt
	q := RuleCommand{Action: "start", Names: []string{r.Name}, Owner: "job", TTLSeconds: 5, LeaseSeconds: 30, ExpectedRules: config.RulesDigest([]config.Rule{r})}
	if _, e := s.rules.command(t.Context(), q); e == nil {
		t.Fatal("new shorter TTL silently ignored")
	}
	if entry.status.ExpiresAt != old {
		t.Fatal("rejected start changed expiry")
	}
	q.TTLSeconds = 3600
	q.LeaseSeconds = 10
	if _, e := s.rules.command(t.Context(), q); e == nil {
		t.Fatal("changed lease silently ignored")
	}
	q.LeaseSeconds = 30
	if _, e := s.rules.command(t.Context(), q); e != nil {
		t.Fatal(e)
	}
	if entry.status.ExpiresAt != old {
		t.Fatal("idempotent start extended grant")
	}
}
