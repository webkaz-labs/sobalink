package lanlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

func testNode() *Node {
	return &Node{cfg: NodeConfig{Identity: GenerateIdentity(), Relay: TrustedRelay{netip.MustParseAddrPort("127.0.0.1:54446"), strings.Repeat("a", 64)}, Trust: NewBook(), Persist: func(Snapshot, []RemotePeer) error { return nil }}, clients: make(map[string]*remoteClient), admissions: make(map[string]time.Time), revoked: make(map[string]uint64), attempts: make(map[string]map[*pairAttempt]struct{}), server: &tailcat.Server{}}
}
func pairFixture(t *testing.T) (*Node, *Node, Invitation, key.NodePrivate, []byte, []byte, pairRequest) {
	t.Helper()
	host, client := testNode(), testNode()
	inv, e := host.IssueInvitation(context.Background(), Peer{client.PublicKey(), "client"}, "host", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	role := key.NewNode()
	if !host.allowClient(role.Public()) {
		t.Fatal("pairing transport admission denied")
	}
	frame, plain, req, e := client.makeRequest(inv.Host, inv.Token, role)
	if e != nil {
		t.Fatal(e)
	}
	return host, client, inv, role, frame, plain, req
}
func TestAuthenticatedRoleBindingRoundTrip(t *testing.T) {
	host, client, inv, clientRole, frame, plain, req := pairFixture(t)
	verified, peer, opened, e := host.readRequest(frame, keyString(clientRole.Public()))
	if e != nil || peer.Key != client.PublicKey() || string(opened) != string(plain) {
		t.Fatal("request proof failed", e)
	}
	hostRole := key.NewNode()
	replyFrame, e := host.makeReply(verified, opened, hostRole)
	if e != nil {
		t.Fatal(e)
	}
	reply, e := client.readReply(replyFrame, inv.Host, req, plain)
	if e != nil {
		t.Fatal(e)
	}
	if e = host.commitPair(context.Background(), RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: hostRole, IncomingClientKey: req.RoleKey}, req.Token, 0); e != nil {
		t.Fatal(e)
	}
	if e = client.commitPair(context.Background(), RemotePeer{Peer: inv.Host.Peer, Address: inv.Host.Address, ClientPrivate: clientRole, IncomingClientKey: reply.RoleKey}, "", 0); e != nil {
		t.Fatal(e)
	}
	if canonical, ok := host.canonicalPeer(req.RoleKey); !ok || canonical != client.PublicKey() {
		t.Fatal("role not mapped to device")
	}
	if _, _, _, e = host.readRequest(frame, keyString(clientRole.Public())); e == nil {
		t.Fatal("one-time token replay accepted")
	}
	saved := host.RemoteSnapshot()
	if len(saved) != 1 || keyString(saved[0].ClientPrivate.Public()) == host.PublicKey() {
		t.Fatal("server key reused for client role")
	}
	public := host.PublicPeers()
	if len(public) != 1 || len(public[0].SourceIPs) != 2 || public[0].SourceIPs[0] == public[0].SourceIPs[1] {
		t.Fatal("missing authenticated source mapping")
	}
}
func TestRequestSubstitutionAndReflectionRejected(t *testing.T) {
	host, client, inv, role, frame, plain, req := pairFixture(t)
	if _, _, _, e := host.readRequest(frame, keyString(key.NewNode().Public())); e == nil {
		t.Fatal("wrong transport role accepted")
	}
	other := testNode()
	if _, _, _, e := other.readRequest(frame, keyString(role.Public())); e == nil {
		t.Fatal("wrong recipient accepted")
	}
	mutations := []func(*pairRequest){func(r *pairRequest) { r.Domain = replyDomain }, func(r *pairRequest) { r.Version = 2 }, func(r *pairRequest) { r.Recipient = client.PublicKey() }, func(r *pairRequest) { r.Sender = host.PublicKey() }, func(r *pairRequest) { r.Nonce = []byte{1} }, func(r *pairRequest) { r.RoleKey = client.PublicKey() }, func(r *pairRequest) { r.TokenHash = strings.Repeat("0", 64) }, func(r *pairRequest) { r.CapabilityHash = strings.Repeat("0", 64) }, func(r *pairRequest) { r.Relay.Address = netip.MustParseAddrPort("127.0.0.2:54446") }, func(r *pairRequest) { r.Address = host.Address() }}
	for i, change := range mutations {
		changed := req
		change(&changed)
		bad, _, e := sealMessage(client.cfg.Identity, host.PublicKey(), changed)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, _, e = host.readRequest(bad, keyString(role.Public())); e == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
	reply, _ := host.makeReply(req, plain, key.NewNode())
	if _, _, _, e := host.readRequest(reply, keyString(role.Public())); e == nil {
		t.Fatal("reply reflected as request")
	}
	if _, e := client.readReply(frame, inv.Host, req, plain); e == nil {
		t.Fatal("request reflected as reply")
	}
}
func TestReplyTranscriptBinding(t *testing.T) {
	host, client, inv, _, _, plain, req := pairFixture(t)
	frame, e := host.makeReply(req, plain, key.NewNode())
	if e != nil {
		t.Fatal(e)
	}
	var reply pairReply
	if _, e = openMessage(client.cfg.Identity, frame, host.PublicKey(), &reply); e != nil {
		t.Fatal(e)
	}
	mutations := []func(*pairReply){func(r *pairReply) { r.Domain = requestDomain }, func(r *pairReply) { r.Recipient = host.PublicKey() }, func(r *pairReply) { r.RequestHash = "wrong" }, func(r *pairReply) { r.TokenHash = "wrong" }, func(r *pairReply) { r.Nonce = []byte("wrong") }, func(r *pairReply) { r.CapabilityHash = "wrong" }, func(r *pairReply) { r.PeerCapabilityHash = "wrong" }, func(r *pairReply) { r.RoleKey = host.PublicKey() }}
	for i, change := range mutations {
		bad := reply
		change(&bad)
		frame, _, e := sealMessage(host.cfg.Identity, client.PublicKey(), bad)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = client.readReply(frame, inv.Host, req, plain); e == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
}
func TestPersistFailureDoesNotPublishApproval(t *testing.T) {
	host, client, _, role, frame, _, _ := pairFixture(t)
	req, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	host.cfg.Persist = func(s Snapshot, rs []RemotePeer) error {
		calls++
		if len(s.Peers) != 1 || len(rs) != 1 {
			t.Fatal("incomplete transaction")
		}
		return errors.New("simulated save failure")
	}
	record := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: key.NewNode(), IncomingClientKey: req.RoleKey}
	if host.commitPair(context.Background(), record, req.Token, 0) == nil {
		t.Fatal("save failure hidden")
	}
	if calls != 1 || len(host.RemoteSnapshot()) != 0 {
		t.Fatal("partial remote state")
	}
	if _, e = host.cfg.Trust.Epoch(client.PublicKey()); e == nil {
		t.Fatal("approval published before save")
	}
	if _, e = host.cfg.Trust.invitedPeer(req.Token, client.PublicKey(), host.cfg.Relay.Address, time.Now()); e != nil {
		t.Fatal("failed transaction consumed invitation")
	}
}
func TestRevokeRaceRejectsLatePairCommit(t *testing.T) {
	host, client, inv, role, _, _, _ := pairFixture(t)
	if e := client.Revoke(host.PublicKey()); e != nil {
		t.Fatal(e)
	}
	record := RemotePeer{Peer: inv.Host.Peer, Address: inv.Host.Address, ClientPrivate: role, IncomingClientKey: keyString(key.NewNode().Public())}
	if e := client.commitPair(context.Background(), record, "", 0); !errors.Is(e, ErrUntrusted) {
		t.Fatal("late pair re-approved revoked device", e)
	}
}
func TestExplicitRelayValidationAndNoDefaultMap(t *testing.T) {
	n := testNode()
	offer := n.Offer("peer")
	if _, e := validateRemote(offer, n.cfg.Relay); e != nil {
		t.Fatal(e)
	}
	ci, e := tailcat.ParseAddr(offer.Address)
	if e != nil {
		t.Fatal(e)
	}
	mutations := []func(*tailcat.ConnInfo){func(c *tailcat.ConnInfo) { c.RegionID = 1; c.Region = nil }, func(c *tailcat.ConnInfo) { c.Region[0].Nodes[0].InsecureForTests = true }, func(c *tailcat.ConnInfo) { c.Region[0].Nodes[0].IPv4 = "8.8.8.8" }, func(c *tailcat.ConnInfo) { c.Region[0].Nodes[0].IPv6 = "" }, func(c *tailcat.ConnInfo) { c.Region[0].Nodes[0].STUNPort = 3478 }, func(c *tailcat.ConnInfo) { c.Region[0].Nodes[0].CertName = "" }}
	for i, mutate := range mutations {
		cloned, e := tailcat.ParseAddr(ci.Addr())
		if e != nil {
			t.Fatal(e)
		}
		mutate(&cloned)
		bad := offer
		bad.Address = cloned.Addr()
		if _, e = validateRemote(bad, n.cfg.Relay); e == nil {
			t.Fatalf("relay mutation %d admitted", i)
		}
	}
}
func TestEnvironmentOverridesRejected(t *testing.T) {
	for _, s := range []string{"HTTP_PROXY=http://example.invalid", "ALL_PROXY=socks5://example.invalid", "TS_DEBUG_USE_DERP_ADDR=example.invalid"} {
		if validateEnvironment([]string{s}) == nil {
			t.Fatal(s)
		}
	}
	if e := validateEnvironment([]string{"TS_NO_LOGS_NO_SUPPORT=true", "LANG=ja_JP.UTF-8"}); e != nil {
		t.Fatal(e)
	}
}
func TestPairMessageBoundsUnknownFields(t *testing.T) {
	n := testNode()
	var out pairEnvelope
	if strictJSON([]byte(`{"sender":"x","box":"","extra":1}`), &out) == nil {
		t.Fatal("unknown fields accepted")
	}
	if _, e := openMessage(n.cfg.Identity, make([]byte, maxPairMessage+1), n.PublicKey(), &out); e == nil {
		t.Fatal("oversize accepted")
	}
	data, _ := json.Marshal(Invitation{Version: 1})
	if len(data) == 0 {
		t.Fatal("bad invitation encoding")
	}
}

func TestCryptoTamperingAndFreshTranscriptReplay(t *testing.T) {
	host, client, inv, role, frame, plain, req := pairFixture(t)
	var envelope pairEnvelope
	if e := json.Unmarshal(frame, &envelope); e != nil {
		t.Fatal(e)
	}
	envelope.Box[len(envelope.Box)-1] ^= 1
	bad, _ := json.Marshal(envelope)
	if _, _, _, e := host.readRequest(bad, keyString(role.Public())); e == nil {
		t.Fatal("ciphertext bitflip accepted")
	}
	json.Unmarshal(frame, &envelope)
	envelope.Sender = host.PublicKey()
	bad, _ = json.Marshal(envelope)
	if _, _, _, e := host.readRequest(bad, keyString(role.Public())); e == nil {
		t.Fatal("sender substitution accepted")
	}
	changed := req
	changed.Address = host.Address()
	changed.CapabilityHash = digest([]byte(changed.Address))
	bad, _, _ = sealMessage(client.cfg.Identity, host.PublicKey(), changed)
	if _, _, _, e := host.readRequest(bad, keyString(role.Public())); e == nil {
		t.Fatal("foreign server capability accepted with matching digest")
	}
	changed = req
	changed.RoleKey = client.PublicKey()
	bad, _, _ = sealMessage(client.cfg.Identity, host.PublicKey(), changed)
	if _, _, _, e := host.readRequest(bad, client.PublicKey()); e == nil {
		t.Fatal("server key reused as client role")
	}
	reply, _ := host.makeReply(req, plain, key.NewNode())
	_, freshPlain, freshReq, _ := client.makeRequest(inv.Host, inv.Token, role)
	if _, e := client.readReply(reply, inv.Host, freshReq, freshPlain); e == nil {
		t.Fatal("old reply accepted for new transcript")
	}
}
func TestRoleNamespaceAndRevocationEpoch(t *testing.T) {
	host, client, _, role, frame, _, _ := pairFixture(t)
	req, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	record := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: key.NewNode(), IncomingClientKey: req.RoleKey}
	bad := record
	bad.IncomingClientKey = host.PublicKey()
	if host.commitPair(context.Background(), bad, req.Token, 0) == nil {
		t.Fatal("local server key used as incoming role")
	}
	bad = record
	bad.IncomingClientKey = keyString(bad.ClientPrivate.Public())
	if host.commitPair(context.Background(), bad, req.Token, 0) == nil {
		t.Fatal("same inbound/outbound role accepted")
	}
	if e = host.commitPair(context.Background(), record, req.Token, 0); e != nil {
		t.Fatal(e)
	}
	id, epoch, ok := host.resolveRole(req.RoleKey)
	if !ok {
		t.Fatal("missing role")
	}
	if e = host.Revoke(client.PublicKey()); e != nil {
		t.Fatal(e)
	}
	replacement := record
	replacement.ClientPrivate = key.NewNode()
	replacement.IncomingClientKey = keyString(key.NewNode().Public())
	if e = host.commitPair(context.Background(), replacement, "", host.revoked[client.PublicKey()]); e != nil {
		t.Fatal(e)
	}
	oldFlow := newFakeDatagrams(12345)
	if _, e = host.track(id, epoch, oldFlow); !errors.Is(e, ErrUntrusted) {
		t.Fatal("stale epoch acquired new approval", e)
	}
	select {
	case <-oldFlow.closed:
	default:
		t.Fatal("stale flow not closed")
	}
	if _, _, ok = host.resolveRole(req.RoleKey); ok {
		t.Fatal("old role still mapped")
	}
}
func TestPostACKLocalSaveFailureIsExplicit(t *testing.T) {
	host, client, inv, role, frame, plain, req := pairFixture(t)
	_, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	hostRole := key.NewNode()
	if e = host.commitPair(context.Background(), RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: hostRole, IncomingClientKey: req.RoleKey}, req.Token, 0); e != nil {
		t.Fatal(e)
	}
	reply, e := host.makeReply(req, plain, hostRole)
	if e != nil {
		t.Fatal(e)
	}
	client.cfg.Persist = func(Snapshot, []RemotePeer) error { return errors.New("disk error") }
	if e = client.acceptPairReply(context.Background(), reply, inv.Host, req, plain, role, 0); !errors.Is(e, ErrRemotePairedLocalSave) {
		t.Fatal(e)
	}
	if _, e = client.cfg.Trust.Epoch(host.PublicKey()); e == nil {
		t.Fatal("unsaved local trust became active")
	}
	if _, e = host.cfg.Trust.Epoch(client.PublicKey()); e != nil {
		t.Fatal("remote committed trust unexpectedly changed")
	}
}

func TestActiveInvitationBlocksSimultaneousOppositePair(t *testing.T) {
	host, client, inv, _, _, _, _ := pairFixture(t)
	_, e := client.IssueInvitation(context.Background(), Peer{host.PublicKey(), "host"}, "client", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = client.PairInvitation(context.Background(), inv); e == nil || !strings.Contains(e.Error(), "cancel the invitation") {
		t.Fatal("reciprocal pairing was not stopped before dial", e)
	}
}

func TestSuccessfulPairPromotesExistingClientObject(t *testing.T) {
	host, client, inv, role, _, plain, req := pairFixture(t)
	reply, e := host.makeReply(req, plain, key.NewNode())
	if e != nil {
		t.Fatal(e)
	}
	live := &tailcat.Client{Key: role, Server: inv.Host.Address}
	if e = client.acceptPairReply(context.Background(), reply, inv.Host, req, plain, role, 0, live); e != nil {
		t.Fatal(e)
	}
	r, e := client.client(host.PublicKey())
	if e != nil {
		t.Fatal(e)
	}
	if r.client != live || !r.started || r.runCtx == nil {
		t.Fatal("pair engine was not atomically promoted")
	}
	client.Close()
}
func TestExpiredTransportAdmissionCannotCommitPair(t *testing.T) {
	host, _, _, role, frame, _, _ := pairFixture(t)
	req, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	host.mu.Lock()
	host.admissions[req.RoleKey] = time.Now().Add(-time.Second)
	host.mu.Unlock()
	if e = host.commitPair(context.Background(), RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: key.NewNode(), IncomingClientKey: req.RoleKey}, req.Token, 0); !errors.Is(e, ErrUntrusted) {
		t.Fatal("expired transport committed", e)
	}
	if _, e = host.cfg.Trust.Epoch(peer.Key); e == nil {
		t.Fatal("expired transport gained trust")
	}
}
