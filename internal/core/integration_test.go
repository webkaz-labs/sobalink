package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestReviewedShareMappingAndClearingReceiveDefault(t *testing.T) {
	p := newCorePair(t)
	value := mustCommand(t, p.a, "service.share", map[string]any{"name": "mapped", "network": "tcp", "ports": "8080", "localPort": 9000, "peerIds": []string{"peer-b"}, "ttlSeconds": 60}).(map[string]any)
	if value["localPort"] != 9000 || len(p.a.profileCopy().Services) != 1 {
		t.Fatal("explicit share mapping was not preserved")
	}
	dir := t.TempDir()
	mustCommand(t, p.a, "settings.update", map[string]string{"receiveDirectory": dir})
	mustCommand(t, p.a, "settings.update", map[string]string{"theme": "dark"})
	if p.a.profileCopy().Settings.ReceiveDirectory != dir {
		t.Fatal("omitted directory was cleared")
	}
	mustCommand(t, p.a, "settings.update", map[string]string{"receiveDirectory": ""})
	if p.a.profileCopy().Settings.ReceiveDirectory != "" {
		t.Fatal("explicit empty directory was not cleared")
	}
}

// All peer traffic stays inside net.Pipe. These tests do not create or enroll
// a real embedded node, listen on LAN interfaces, or alter OS network settings.
type pipeNetwork struct {
	mu        sync.Mutex
	listeners map[netip.AddrPort]*pipeListener
	wire      bytes.Buffer
}
type pipeNode struct {
	mu       sync.Mutex
	hub      *pipeNetwork
	ip       netip.Addr
	state    identity.State
	who      map[netip.Addr]string
	fallback func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)
	closed   bool
}
type pipeListener struct {
	address netip.AddrPort
	queue   chan net.Conn
	done    chan struct{}
	once    sync.Once
}
type pipeConn struct {
	net.Conn
	local, remote netip.AddrPort
	hub           *pipeNetwork
}

func (c *pipeConn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(c.local) }
func (c *pipeConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }
func (c *pipeConn) Write(p []byte) (int, error) {
	c.hub.mu.Lock()
	_, _ = c.hub.wire.Write(p)
	c.hub.mu.Unlock()
	return c.Conn.Write(p)
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.queue:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Addr() net.Addr { return net.TCPAddrFromAddrPort(l.address) }
func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}
func (n *pipeNode) Start() error                 { return nil }
func (n *pipeNode) Login(context.Context) error  { return nil }
func (n *pipeNode) Logout(context.Context) error { return nil }
func (n *pipeNode) State(context.Context) (identity.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return identity.State{}, net.ErrClosed
	}
	s := n.state
	s.IPs = append([]netip.Addr(nil), s.IPs...)
	s.Snapshot.Peers = append([]policy.Peer(nil), s.Snapshot.Peers...)
	for i := range s.Snapshot.Peers {
		s.Snapshot.Peers[i].IPs = append([]netip.Addr(nil), s.Snapshot.Peers[i].IPs...)
	}
	return s, nil
}
func (n *pipeNode) WhoIs(_ context.Context, source netip.AddrPort) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id := n.who[source.Addr()]; id != "" {
		return id, nil
	}
	return "", errors.New("no current identity")
}
func (n *pipeNode) Listen(network, address string) (net.Listener, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || network != "tcp" || ap.Addr() != n.ip {
		return nil, errors.New("unexpected listener scope")
	}
	n.hub.mu.Lock()
	defer n.hub.mu.Unlock()
	if old := n.hub.listeners[ap]; old != nil {
		select {
		case <-old.done:
		default:
			return nil, errors.New("listener already exists")
		}
	}
	l := &pipeListener{address: ap, queue: make(chan net.Conn), done: make(chan struct{})}
	n.hub.listeners[ap] = l
	return l, nil
}
func (n *pipeNode) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("simulated datagram listener failure")
}
func (n *pipeNode) DialIP(ctx context.Context, network string, target netip.AddrPort) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("test transport requires TCP")
	}
	n.hub.mu.Lock()
	l := n.hub.listeners[target]
	n.hub.mu.Unlock()
	if l == nil {
		return nil, errors.New("no in-memory peer listener")
	}
	left, right := net.Pipe()
	source := netip.AddrPortFrom(n.ip, 40000)
	client := &pipeConn{Conn: left, local: source, remote: target, hub: n.hub}
	server := &pipeConn{Conn: right, local: target, remote: source, hub: n.hub}
	select {
	case l.queue <- server:
		return client, nil
	case <-l.done:
		left.Close()
		right.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		left.Close()
		right.Close()
		return nil, ctx.Err()
	}
}
func (n *pipeNode) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	n.mu.Lock()
	n.fallback = f
	n.mu.Unlock()
	return func() { n.mu.Lock(); n.fallback = nil; n.mu.Unlock() }, nil
}
func (n *pipeNode) Close() error {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.hub.mu.Lock()
	defer n.hub.mu.Unlock()
	for ap, l := range n.hub.listeners {
		if ap.Addr() == n.ip {
			_ = l.Close()
		}
	}
	return nil
}

type corePair struct {
	a, b   *Core
	na, nb *pipeNode
	hub    *pipeNetwork
}

func newCorePair(t *testing.T) corePair {
	t.Helper()
	hub := &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}
	aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	makeNode := func(ip, peerIP netip.Addr, id string) *pipeNode {
		return &pipeNode{hub: hub, ip: ip, who: map[netip.Addr]string{peerIP: id}, state: identity.State{IPs: []netip.Addr{ip}, Backend: "Running", AuthURL: "https://login.invalid/private-auth-token", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: id, DNSName: id + ".invalid", IPs: []netip.Addr{peerIP}}}}}}
	}
	na, nb := makeNode(aIP, bIP, "peer-b"), makeNode(bIP, aIP, "peer-a")
	open := func(node *pipeNode) *Core {
		c, err := Open(context.Background(), Options{Directory: t.TempDir(), Version: "test", NodeFactory: func(string, string) (NetworkBackend, error) { return node, nil }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := c.Close(); err != nil {
				t.Error(err)
			}
		})
		mustCommand(t, c, "network.configure", map[string]any{"mode": "tailnet"})
		c.op.Lock()
		err = c.startPeerServer([]netip.Addr{node.ip})
		c.op.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return corePair{a: open(na), b: open(nb), na: na, nb: nb, hub: hub}
}

func command(c *Core, id, name string, payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Command(ctx, webui.Command{RequestID: id, Name: name, Payload: raw})
}
func mustCommand(t *testing.T, c *Core, name string, payload any) any {
	t.Helper()
	value, err := command(c, randomID(), name, payload)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}
func trustPair(t *testing.T, pair corePair) {
	t.Helper()
	mustCommand(t, pair.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": true})
	mustCommand(t, pair.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": true})
}
func peerCall(t *testing.T, from *pipeNode, to *pipeNode, method, path string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if text, ok := payload.(string); ok {
		body = strings.NewReader(text)
	} else if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return from.DialIP(ctx, network, netip.AddrPortFrom(to.ip, PeerPort))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	r, err := http.NewRequest(method, "http://peer.invalid"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}
func assertPeerStatus(t *testing.T, from, to *pipeNode, method, path string, payload any, want int) []byte {
	t.Helper()
	status, body := peerCall(t, from, to, method, path, payload)
	if status != want {
		t.Fatalf("%s %s = %d: %s; want %d", method, path, status, body, want)
	}
	return body
}
func fileManifest(id, text string) transfer.Manifest {
	sum := sha256.Sum256([]byte(text))
	return transfer.Manifest{ID: id, Entries: []transfer.Entry{{ID: "file", Path: "folder/note.txt", Kind: transfer.File, Size: int64(len(text)), SHA256: hex.EncodeToString(sum[:])}}}
}

func TestMessageDeliveryUsesCurrentIdentityAndIsIdempotent(t *testing.T) {
	p := newCorePair(t)
	assertPeerStatus(t, p.na, p.nb, "GET", "/v1/hello", nil, 200)
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": "untrusted", "text": "hello"}, 403)
	trustPair(t, p)
	payload := map[string]string{"peerId": "peer-b", "text": "Hello / こんにちは"}
	first, err := command(p.a, "send-once", "message.send", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := command(p.a, "send-once", "message.send", payload)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent command = %+v, %v", second, err)
	}
	msg := first.(Message)
	if msg.Status != "sent" {
		t.Fatalf("outgoing status: %+v", msg)
	}
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": msg.ID, "text": msg.Text}, 200)
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": msg.ID, "text": "changed"}, 409)
	if _, err := command(p.a, "send-once", "message.send", map[string]string{"peerId": "peer-b", "text": "changed"}); err == nil {
		t.Fatal("request ID reused for changed command")
	}
	p.b.mu.RLock()
	count := len(p.b.messages)
	p.b.mu.RUnlock()
	if count != 1 {
		t.Fatalf("receiver persisted %d copies", count)
	}
	p.nb.mu.Lock()
	p.nb.who[p.na.ip] = "different-peer"
	p.nb.mu.Unlock()
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/messages", map[string]string{"id": "changed-identity", "text": "hello"}, 403)
	p.nb.mu.Lock()
	p.nb.who[p.na.ip] = "peer-a"
	p.nb.state.Snapshot.Peers[0].Expired = true
	p.nb.mu.Unlock()
	assertPeerStatus(t, p.na, p.nb, "GET", "/v1/hello", nil, 403)
	p.nb.mu.Lock()
	p.nb.state.Snapshot.Peers[0].Expired = false
	p.nb.state.Snapshot.Peers[0].IPs = []netip.Addr{netip.MustParseAddr("100.64.0.9")}
	p.nb.mu.Unlock()
	assertPeerStatus(t, p.na, p.nb, "GET", "/v1/hello", nil, 403)
}

func TestIncomingBatchRequiresAcceptanceAndPinnedGeneration(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	manifest := fileManifest("batch-one", "payload")
	before := assertPeerStatus(t, p.na, p.nb, "POST", "/v1/offers", manifest, 200)
	var offered wireBatch
	if err := json.Unmarshal(before, &offered); err != nil || offered.State != transfer.Pending {
		t.Fatalf("offer = %s, %v", before, err)
	}
	assertPeerStatus(t, p.na, p.nb, "PUT", "/v1/batches/batch-one/files/file", "payload", 409)
	dest := t.TempDir()
	accepted := mustCommand(t, p.b, "transfer.accept", map[string]string{"transferId": manifest.ID, "destination": dest}).(transfer.Batch)
	ackData := assertPeerStatus(t, p.na, p.nb, "PUT", "/v1/batches/batch-one/files/file", "payload", 200)
	var ack transfer.FileAck
	if err := json.Unmarshal(ackData, &ack); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accepted.Destination, ack.StoredName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ackAgain := assertPeerStatus(t, p.na, p.nb, "PUT", "/v1/batches/batch-one/files/file", "payload", 200)
	if !bytes.Equal(ackData, ackAgain) {
		t.Fatalf("retry changed acknowledgment: %s / %s", ackData, ackAgain)
	}
	infoAgain, err := os.Stat(path)
	if err != nil || !os.SameFile(info, infoAgain) {
		t.Fatalf("retry rewrote file: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != "payload" {
		t.Fatalf("saved file = %q, %v", saved, err)
	}
	state := assertPeerStatus(t, p.na, p.nb, "GET", "/v1/batches/batch-one", nil, 200)
	for _, forbidden := range []string{dest, p.a.dir, p.b.dir, `"destination"`, `"generation"`, `"peer"`, `"authUrl"`, `"csrfToken"`, "private-auth-token"} {
		if bytes.Contains(state, []byte(forbidden)) || bytes.Contains(ackData, []byte(forbidden)) {
			t.Errorf("peer wire leaked %q", forbidden)
		}
	}
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/offers", fileManifest("pending-old-generation", "next"), 200)
	old, _ := p.b.trust("peer-a")
	mustCommand(t, p.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
	assertPeerStatus(t, p.na, p.nb, "GET", "/v1/batches/batch-one", nil, 403)
	mustCommand(t, p.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": true})
	current, _ := p.b.trust("peer-a")
	if current.Generation <= old.Generation {
		t.Fatalf("trust generation did not increase: %d -> %d", old.Generation, current.Generation)
	}
	assertPeerStatus(t, p.na, p.nb, "GET", "/v1/batches/batch-one", nil, 404)
	assertPeerStatus(t, p.na, p.nb, "POST", "/v1/offers", fileManifest("pending-old-generation", "next"), 400)
	if _, err := command(p.b, randomID(), "transfer.accept", map[string]string{"transferId": "pending-old-generation", "destination": dest}); err == nil {
		t.Fatal("re-trust revived previous pending transfer")
	}
}

func TestSendPathsCompletesAcrossMockPeerTransport(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	dest := t.TempDir()
	mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": dest})
	source := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(source, []byte("selected file"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := p.a.SendPaths(context.Background(), "peer-b", []string{source})
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]string)["id"]
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.a.mu.RLock()
		batch := p.a.outgoing[id]
		p.a.mu.RUnlock()
		batch.mu.Lock()
		state, detail, running := batch.State, batch.Error, batch.running
		batch.mu.Unlock()
		if state == "completed" && !running {
			break
		}
		if state == "failed" || time.Now().After(deadline) {
			t.Fatalf("outgoing state %q: %s", state, detail)
		}
		time.Sleep(5 * time.Millisecond)
	}
	batch, err := p.b.transfers.Get(id)
	if err != nil || batch.State != transfer.Completed || batch.CompletedBytes != int64(len("selected file")) {
		t.Fatalf("received batch: %+v, %v", batch, err)
	}
	data, err := os.ReadFile(filepath.Join(batch.Destination, batch.Files[0].StoredName))
	if err != nil || string(data) != "selected file" {
		t.Fatalf("saved payload: %q, %v", data, err)
	}
	p.hub.mu.Lock()
	wire := p.hub.wire.String()
	p.hub.mu.Unlock()
	for _, secret := range []string{source, dest, p.a.dir, p.b.dir, "private-auth-token", `"generation"`, `"destination"`} {
		if strings.Contains(wire, secret) {
			t.Errorf("peer transport leaked %q", secret)
		}
	}
	p.a.mu.RLock()
	outgoing := p.a.outgoing[id]
	p.a.mu.RUnlock()
	outgoing.mu.Lock()
	spool := outgoing.Spool
	outgoing.mu.Unlock()
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful transfer retained spool: %v", err)
	}
}

func TestRangesStopRevokeAndFailureRemainSavedInactive(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	args := map[string]any{"name": "test-share", "network": "tcp", "ports": "1024-65535", "excludePorts": "8080", "peerIds": []string{"peer-a"}, "ttlSeconds": 60, "discoverable": true}
	value := mustCommand(t, p.b, "service.share", args).(map[string]any)
	id := value["id"].(string)
	p.nb.mu.Lock()
	dispatch := p.nb.fallback
	p.nb.mu.Unlock()
	if dispatch == nil {
		t.Fatal("compact share did not register dispatcher")
	}
	source := netip.AddrPortFrom(p.na.ip, 32123)
	for _, port := range []uint16{8080, DiscoveryPort, PeerPort} {
		if handler, _ := dispatch(source, netip.AddrPortFrom(p.nb.ip, port)); handler != nil {
			t.Errorf("excluded or reserved port %d is permitted", port)
		}
	}
	if handler, _ := dispatch(source, netip.AddrPortFrom(p.nb.ip, 8081)); handler == nil {
		t.Fatal("selected range not active")
	}
	if len(p.b.permittedServices("peer-a")) != 1 || len(p.b.permittedServices("other-peer")) != 0 {
		t.Fatal("discovery did not enforce exact share scope")
	}
	mustCommand(t, p.b, "service.stop", map[string]string{"id": id})
	if handler, _ := dispatch(source, netip.AddrPortFrom(p.nb.ip, 8081)); handler != nil {
		t.Fatal("stopped range remained active")
	}
	if views := p.b.serviceViews(); len(views) != 1 || views[0]["status"] != "stopped" {
		t.Fatalf("stopped view: %+v", views)
	}
	saved := mustCommand(t, p.b, "service.config", map[string]string{"id": id}).(SavedServiceConfiguration)
	args["replaceId"], args["expectedRevision"], args["backend"] = id, saved.Revision, saved.Configuration.Backend
	restarted := mustCommand(t, p.b, "service.share", args).(map[string]any)
	if restarted["id"] != id {
		t.Fatal("restarting saved service changed its identity")
	}
	mustCommand(t, p.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
	if handler, _ := dispatch(source, netip.AddrPortFrom(p.nb.ip, 8081)); handler != nil {
		t.Fatal("revocation retained share")
	}
	if len(p.b.permittedServices("peer-a")) != 0 {
		t.Fatal("revoked share advertised")
	}
	_, err := command(p.b, randomID(), "service.share", map[string]any{"name": "failed-udp", "network": "udp", "ports": "8090", "peerIds": []string{"peer-a"}, "ttlSeconds": 60})
	if err == nil || !strings.Contains(err.Error(), "saved but not started") {
		t.Fatalf("partial failure not reported: %v", err)
	}
	for _, view := range p.b.serviceViews() {
		if view["status"] != "stopped" && view["status"] != "failed" {
			t.Fatalf("failed or revoked service still active: %+v", view)
		}
	}
}

func TestPrivateStatusAndRestartDoNotExposeLoginOrResumeGrants(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	mustCommand(t, p.a, "message.send", map[string]string{"peerId": "peer-b", "text": "saved history"})
	mustCommand(t, p.b, "service.share", map[string]any{"name": "saved-share", "network": "tcp", "ports": "8081", "peerIds": []string{"peer-a"}, "ttlSeconds": 60})
	state, err := p.b.IPC(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"private-auth-token", "authUrl", "authURL", "csrfToken", "soba_session"} {
		if bytes.Contains(data, []byte(token)) {
			t.Errorf("status leaked %s", token)
		}
	}
	if err := p.b.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := &pipeNode{hub: p.hub, ip: p.nb.ip, who: map[netip.Addr]string{p.na.ip: "peer-a"}, state: p.nb.state}
	reopened, err := Open(context.Background(), Options{Directory: p.b.dir, Version: "test", NodeFactory: func(string, string) (NetworkBackend, error) { return fresh, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.messages) != 1 || reopened.messages[0].Text != "saved history" {
		t.Fatalf("history not preserved: %+v", reopened.messages)
	}
	if len(reopened.active) != 0 || len(reopened.outgoing) != 0 || len(reopened.transfers.List()) != 0 {
		t.Fatal("restart resumed transient grants or transfer state")
	}
	if views := reopened.serviceViews(); len(views) != 1 || views[0]["status"] != "saved" {
		t.Fatalf("restarted share: %+v", views)
	}
}

func TestFailedProfileSaveDoesNotPublishTrust(t *testing.T) {
	p := newCorePair(t)
	path := filepath.Join(p.a.dir, "sobalink.json")
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path); _ = os.Rename(backup, path) })
	_, err := command(p.a, randomID(), "peer.trust", map[string]any{"peerId": "peer-b", "trusted": true})
	if err == nil {
		t.Fatal("failed profile write reported success")
	}
	if _, ok := p.a.trust("peer-b"); ok {
		t.Fatal("failed profile write published trust")
	}
	if _, err := p.a.transfers.Offer(transfer.Peer{ID: "peer-b", Generation: 1}, fileManifest("unbound", "text")); !errors.Is(err, transfer.ErrUnknownPeer) {
		t.Fatalf("failed trust write changed receiver binding: %v", err)
	}
}

func TestFailedAutosavePersistenceCannotAcceptIncomingBatch(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	profile := filepath.Join(p.b.dir, "sobalink.json")
	backup := profile + ".backup"
	if err := os.Rename(profile, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(profile); _ = os.Rename(backup, profile) })
	_, err := command(p.b, randomID(), "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
	if err == nil {
		t.Fatal("autosave succeeded without a durable approval")
	}
	trusted, ok := p.b.trust("peer-a")
	if !ok || trusted.Autosave {
		t.Fatalf("failed save changed trust or enabled autosave: %+v", trusted)
	}
	if len(p.b.transfers.ReceivePolicies()) != 0 {
		t.Fatal("failed save published a receiver policy")
	}
	data := assertPeerStatus(t, p.na, p.nb, "POST", "/v1/offers", fileManifest("unapproved-autosave", "payload"), 200)
	var offered wireBatch
	if err := json.Unmarshal(data, &offered); err != nil || offered.State != transfer.Pending {
		t.Fatalf("batch accepted after failed autosave persistence: %s, %v", data, err)
	}
}

type gatedBody struct {
	ready, release chan struct{}
	once           sync.Once
	body           *strings.Reader
}

func (g *gatedBody) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.ready); <-g.release })
	return g.body.Read(p)
}
func (*gatedBody) Close() error { return nil }

func TestMessageCannotCommitAfterTrustOrIdentityChanges(t *testing.T) {
	for _, change := range []string{"revoke", "identity"} {
		t.Run(change, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			gate := &gatedBody{ready: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader(`{"id":"late-message","text":"must not persist"}`)}
			r := httptest.NewRequest("POST", "http://peer.invalid/v1/messages", gate)
			r.RemoteAddr = netip.AddrPortFrom(p.na.ip, 32123).String()
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			completed := make(chan struct{})
			go func() { p.b.peerHTTP(p.b.peerServer, w, r); close(completed) }()
			select {
			case <-gate.ready:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not reach body read")
			}
			if change == "revoke" {
				mustCommand(t, p.b, "peer.trust", map[string]any{"peerId": "peer-a", "trusted": false})
			} else {
				p.nb.mu.Lock()
				p.nb.who[p.na.ip] = "other-peer"
				p.nb.mu.Unlock()
			}
			close(gate.release)
			select {
			case <-completed:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not finish")
			}
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("request committed after %s: %d %s", change, w.Code, w.Body.String())
			}
			p.b.mu.RLock()
			count := len(p.b.messages)
			p.b.mu.RUnlock()
			if count != 0 {
				t.Fatalf("message persisted after %s", change)
			}
		})
	}
}

type uploadBarrierReader struct {
	body                   *bytes.Reader
	before                 int
	passed                 bool
	ready, release, closed chan struct{}
	readyOnce, closeOnce   sync.Once
}

func (b *uploadBarrierReader) Read(p []byte) (int, error) {
	if !b.passed {
		if b.before > 0 {
			if len(p) > b.before {
				p = p[:b.before]
			}
			n, err := b.body.Read(p)
			b.before -= n
			return n, err
		}
		b.readyOnce.Do(func() { close(b.ready) })
		select {
		case <-b.release:
			b.passed = true
		case <-b.closed:
			return 0, context.Canceled
		}
	}
	return b.body.Read(p)
}
func (b *uploadBarrierReader) Close() error { b.closeOnce.Do(func() { close(b.closed) }); return nil }

func stagedUpload(t *testing.T) (*http.Request, *uploadBarrierReader) {
	t.Helper()
	data := strings.Repeat("transfer-content-", 8192)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range []struct{ name, value string }{
		{"peerId", "peer-b"},
		{"requestId", "staged-upload"},
		{"manifest", fmt.Sprintf(`[{"path":"note.txt","kind":"file","size":%d}]`, len(data))},
	} {
		if err := w.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := w.CreateFormFile("files", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(body.Bytes(), []byte("transfer-content-")) + 8192
	if cut < 8192 {
		t.Fatal("multipart payload not found")
	}
	barrier := &uploadBarrierReader{body: bytes.NewReader(body.Bytes()), before: cut, ready: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/upload", barrier)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r, barrier
}

func TestMultipartStagingIsSafeDuringSnapshotsAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%t", shutdown), func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			mustCommand(t, p.b, "peer.autosave", map[string]any{"peerId": "peer-a", "enabled": true, "directory": t.TempDir()})
			r, barrier := stagedUpload(t)
			defer barrier.Close()
			w := httptest.NewRecorder()
			requestDone := make(chan struct{})
			go func() { p.a.Upload(w, r); close(requestDone) }()
			select {
			case <-barrier.ready:
			case <-requestDone:
				t.Fatalf("upload stopped before staging: %d %s", w.Code, w.Body.String())
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not reach staging barrier")
			}
			p.a.mu.RLock()
			batch := p.a.outgoing["staged-upload"]
			p.a.mu.RUnlock()
			if batch == nil {
				t.Fatal("upload body was consumed before reserving staging")
			}
			snapshotStop, snapshotDone := make(chan struct{}), make(chan struct{})
			// One writer brackets each Snapshot: odd means a call has not returned.
			var snapshotSteps atomic.Uint64
			go func() {
				defer close(snapshotDone)
				for {
					select {
					case <-snapshotStop:
						return
					default:
					}
					snapshotSteps.Add(1)
					_, _ = p.a.Snapshot(context.Background())
					snapshotSteps.Add(1)
				}
			}()
			defer func() {
				close(snapshotStop)
				if t.Failed() {
					t.Logf("staged upload diagnostic: snapshot_stop_requested=true snapshot_done=%t", channelClosed(snapshotDone))
				}
				<-snapshotDone
				if t.Failed() {
					t.Log("staged upload diagnostic: snapshot_cleanup_complete=true")
				}
			}()
			closeDone := make(chan error, 1)
			if shutdown {
				go func() { closeDone <- p.a.Close() }()
				select {
				case <-p.a.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("shutdown did not cancel application")
				}
				select {
				case <-barrier.closed:
				case <-time.After(3 * time.Second):
					t.Fatal("shutdown did not close the blocked upload body")
				}
			}
			close(barrier.release)
			select {
			case <-requestDone:
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not finish")
			}
			if !shutdown {
				if w.Code != http.StatusOK {
					t.Fatalf("upload = %d %s", w.Code, w.Body.String())
				}
				deadline := time.Now().Add(5 * time.Second)
				snapshotsBeforeWait := snapshotSteps.Load() / 2
				for {
					batch.mu.Lock()
					state, running, detail := batch.State, batch.running, batch.Error
					staging, completed, reserved := batch.staging, batch.Completed, batch.reserved
					spoolPresent, runDone := batch.Spool != "", batch.runDone
					batch.mu.Unlock()
					if state == "completed" && !running {
						break
					}
					if state == "failed" || time.Now().After(deadline) {
						steps := snapshotSteps.Load()
						t.Logf("staged upload diagnostic: running=%t staging=%t completed_bytes=%d reserved_bytes=%d spool_present=%t error_present=%t run_done_created=%t run_done_closed=%t request_done=%t snapshot_done=%t sender_done=%t snapshots_before_wait=%d snapshots_completed=%d snapshot_call_pending=%t", running, staging, completed, reserved, spoolPresent, detail != "", runDone != nil, channelClosed(runDone), channelClosed(requestDone), channelClosed(snapshotDone), channelClosed(p.a.Done()), snapshotsBeforeWait, steps/2, steps%2 != 0)
						t.Fatalf("staged upload = %s: %s", state, detail)
					}
					time.Sleep(time.Millisecond)
				}
				return
			}
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			batch.mu.Lock()
			running, spool := batch.running, batch.Spool
			batch.mu.Unlock()
			if running {
				t.Fatal("outgoing transfer running after shutdown")
			}
			if spool != "" {
				if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("shutdown retained unfinished staging: %v", err)
				}
			}
			if len(p.b.transfers.List()) != 0 {
				t.Fatal("staged upload was offered after shutdown began")
			}
			postClose, postBarrier := stagedUpload(t)
			close(postBarrier.release)
			defer postBarrier.Close()
			closedResponse := httptest.NewRecorder()
			p.a.Upload(closedResponse, postClose)
			if closedResponse.Code < 400 {
				t.Fatalf("closed application accepted another upload: %d", closedResponse.Code)
			}
		})
	}
}

var _ NetworkBackend = (*pipeNode)(nil)
