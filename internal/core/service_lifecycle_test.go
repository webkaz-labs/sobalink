package core

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/policy"
)

func availableServicePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func withServiceOperation(c *Core, f func()) {
	c.op.Lock()
	defer c.op.Unlock()
	f()
}

func TestServiceLifetimeRequiresExplicitOutboundNoExpiry(t *testing.T) {
	p := newCorePair(t)
	for _, test := range []struct {
		mode    string
		ttl     int
		command string
	}{
		{"", 0, "service.connect"}, {"finite", 0, "service.connect"},
		{"forever", 0, "service.connect"}, {"until-stopped", 1, "service.connect"},
		{"until-stopped", 0, "service.share"}, {"finite", 86401, "service.share"},
	} {
		_, err := command(p.a, randomID(), test.command, map[string]any{"name": "invalid", "network": "tcp", "ports": "8080", "peerId": "peer-b", "peerIds": []string{"peer-b"}, "lifetime": test.mode, "ttlSeconds": test.ttl})
		if err == nil || len(p.a.profileCopy().Services) != 0 {
			t.Fatalf("invalid lifetime was accepted or persisted: %+v", test)
		}
	}
	value := mustCommand(t, p.a, "service.connect", map[string]any{"name": "persistent", "network": "tcp", "ports": "8080", "localPort": availableServicePort(t), "peerId": "peer-b", "lifetime": "until-stopped"}).(map[string]any)
	id := value["id"].(string)
	if value["lifetime"] != "until-stopped" || value["expiresAt"] != nil {
		t.Fatalf("unbounded lifetime is not explicit in status: %+v", value)
	}
	var active *activeService
	withServiceOperation(p.a, func() {
		active = p.a.active[id]
		if err := active.guardAt(time.Now().Add(48 * time.Hour)); err != nil {
			t.Fatal("explicit outbound lifetime unexpectedly expired", err)
		}
		p.a.expireServices()
		if p.a.active[id] != active {
			t.Fatal("expiry sweep removed an explicit until-stopped connection")
		}
	})
	mustCommand(t, p.a, "service.stop", map[string]string{"id": id})
	if active.guard() == nil || p.a.serviceViews()[0]["status"] != "stopped" {
		t.Fatal("explicit stop failed to revoke the unbounded connection")
	}
	mustCommand(t, p.a, "peer.reconnect", map[string]string{"peerId": "peer-b"})
	if savedService(t, p.a, id).Active {
		t.Fatal("reconnect revived the stopped permission")
	}
	if err := p.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: p.a.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved := savedService(t, reopened, id)
	if saved.Active || saved.Configuration.Lifetime != "until-stopped" || saved.Configuration.TTLSeconds != 0 {
		t.Fatal("reopen activated or changed a saved until-stopped definition")
	}
}

func TestShareLoopbackMappingIsExactAndStaysPrivate(t *testing.T) {
	p := newCorePair(t)
	args := map[string]any{"name": "mapped-ipv6", "network": "tcp", "ports": "8080", "localPort": 9000, "loopbackHost": "::1", "peerIds": []string{"peer-a"}, "ttlSeconds": 60, "discoverable": true}
	value := mustCommand(t, p.b, "service.share", args).(map[string]any)
	saved := savedService(t, p.b, value["id"].(string))
	if saved.Configuration.LocalPort != 9000 || saved.Configuration.LoopbackHost != "::1" || saved.Configuration.Lifetime != "finite" || p.b.materializedCount() != 0 {
		t.Fatal("mapped compact share lost its target or allocated per-port listeners")
	}
	public, _ := json.Marshal(p.b.permittedServices("peer-a"))
	for _, private := range []string{"9000", "::1", "localPort", "loopbackHost", "lifetime"} {
		if strings.Contains(string(public), private) {
			t.Fatalf("local mapping leaked in peer discovery: %s", public)
		}
	}
	for _, host := range []string{"localhost", "127.0.0.2", "::", "::ffff:127.0.0.1", "192.0.2.1"} {
		args["name"], args["loopbackHost"] = "invalid-host", host
		if _, err := command(p.b, randomID(), "service.share", args); err == nil {
			t.Fatalf("accepted loopback host %q", host)
		}
	}
	args["loopbackHost"], args["ports"], args["localPort"] = "127.0.0.1", "8081-8082", 9000
	if _, err := command(p.b, randomID(), "service.share", args); err == nil {
		t.Fatal("ambiguous range mapping was accepted")
	}
	args["ports"] = "8081"
	p.nb.mu.Lock()
	p.nb.state.ReservedPorts = []uint16{60000}
	p.nb.mu.Unlock()
	for _, port := range []int{54543, 54544, 54545, 60000, -1, 65536} {
		args["localPort"] = port
		if _, err := command(p.b, randomID(), "service.share", args); err == nil {
			t.Fatalf("reserved or invalid mapped target %d was accepted", port)
		}
	}
	if len(p.b.profileCopy().Services) != 1 {
		t.Fatal("invalid mappings changed saved configuration")
	}
}

func TestServiceTransportRecoveryKeepsOriginalPermission(t *testing.T) {
	p := newCorePair(t)
	remote, err := p.nb.Listen("tcp", net.JoinHostPort(p.nb.ip.String(), "8080"))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	value := mustCommand(t, p.a, "service.connect", map[string]any{"name": "recoverable", "network": "tcp", "ports": "8080", "localPort": availableServicePort(t), "peerId": "peer-b", "ttlSeconds": 60}).(map[string]any)
	id := value["id"].(string)
	roundTrip := func(payload string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			conn, err := remote.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, len(payload))
			if _, err = io.ReadFull(conn, buf); err == nil {
				_, err = conn.Write(buf)
			}
			done <- err
		}()
		conn, err := net.DialTimeout("tcp4", value["endpoint"].(string), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != payload {
			t.Fatalf("round trip: %q, %v", buf, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	roundTrip("before")
	var active *activeService
	var until time.Time
	withServiceOperation(p.a, func() {
		active = p.a.active[id]
		until = active.expires
		original := active.servers[0]
		_ = original.Close()
		state, _ := p.a.current(context.Background())
		p.a.revalidateServices(state)
		if active.servers[0] == original || !active.expires.Equal(until) || active.guard() != nil {
			t.Fatal("automatic recovery failed or renewed the permission")
		}
	})
	roundTrip("after")
	p.a.mu.Lock()
	delete(p.a.confirmed, "peer-b")
	p.a.mu.Unlock()
	mustCommand(t, p.a, "peer.reconnect", map[string]string{"peerId": "peer-b"})
	p.a.mu.RLock()
	confirmed := !p.a.confirmed["peer-b"].IsZero()
	p.a.mu.RUnlock()
	if !confirmed {
		t.Fatal("service reconnect skipped refreshing available app capabilities")
	}
	// A plain service may have no sobalink peer API. Explicit reconnect must
	// still recreate its transport rather than depending on a hello response.
	p.hub.mu.Lock()
	peerAPI := p.hub.listeners[netip.AddrPortFrom(p.nb.ip, PeerPort)]
	p.hub.mu.Unlock()
	_ = peerAPI.Close()
	mustCommand(t, p.a, "peer.reconnect", map[string]string{"peerId": "peer-b"})
	roundTrip("again")
	withServiceOperation(p.a, func() {
		state, _ := p.a.current(context.Background())
		if !active.expires.Equal(until) {
			t.Fatal("explicit reconnect renewed the deadline")
		}
		p.a.networkReady.Store(false)
		p.a.suspendServices()
		if len(active.servers) != 0 || active.guard() == nil || !active.permissionActiveAt(time.Now()) || p.a.materializedCount() != 1 {
			t.Fatal("suspend lost the permission/capacity reservation or retained transports")
		}
		conflict, err := net.Listen("tcp4", value["endpoint"].(string))
		if err != nil {
			t.Fatal(err)
		}
		defer conflict.Close()
		p.a.networkReady.Store(true)
		p.a.revalidateServices(state)
		if len(active.servers) != 0 || active.error == "" || !active.expires.Equal(until) || !active.permissionActiveAt(time.Now()) {
			t.Fatal("failed recovery lost the original grant or reported a live listener")
		}
		if err := conflict.Close(); err != nil {
			t.Fatal(err)
		}
		p.a.revalidateServices(state)
		if active.guard() != nil || active.error != "" || !active.expires.Equal(until) {
			t.Fatal("network recovery did not retain the original grant")
		}
		active.expires = time.Now().Add(-time.Second)
		p.a.expireServices()
		p.a.revalidateServices(state)
		if p.a.active[id] != nil || p.a.serviceStates[id] != "expired" {
			t.Fatal("expired service was revived")
		}
	})
}

func TestCompactShareRecoveryCannotReviveRevokedOrChangedScope(t *testing.T) {
	p := newCorePair(t)
	value := mustCommand(t, p.b, "service.share", map[string]any{"name": "compact", "network": "tcp", "ports": "8080-8081", "peerIds": []string{"peer-a"}, "ttlSeconds": 60}).(map[string]any)
	id := value["id"].(string)
	p.b.op.Lock()
	defer p.b.op.Unlock()
	active := p.b.active[id]
	until := active.expires
	state, _ := p.b.current(context.Background())
	p.b.networkReady.Store(false)
	p.b.suspendServices()
	if active.guard() == nil {
		t.Fatal("offline share remained authorized")
	}
	// Suspended grants continue to own their compact scopes.
	plan, err := p.b.rangePlan(state.IPs, nil)
	if err != nil || plan.PolicyCount() != 1 {
		t.Fatal("suspend lost reserved compact scope", err)
	}
	p.b.networkReady.Store(true)
	p.b.revalidateServices(state)
	if active.guard() != nil || !active.expires.Equal(until) {
		t.Fatal("share recovery changed permission")
	}
	state.Snapshot.Peers = nil
	p.b.revalidateServices(state)
	state, _ = p.b.current(context.Background())
	p.b.revalidateServices(state)
	if p.b.active[id] != nil {
		t.Fatal("peer identity removal was treated as transient transport loss")
	}
}

func TestReconnectPreservesHealthyShareForOtherAllowedPeers(t *testing.T) {
	p := newCorePair(t)
	otherIP := netip.MustParseAddr("100.64.0.3")
	p.nb.mu.Lock()
	p.nb.state.Snapshot.Peers = append(p.nb.state.Snapshot.Peers, policy.Peer{ID: "peer-c", DNSName: "c.example.test", IPs: []netip.Addr{otherIP}, Online: true})
	p.nb.who[otherIP] = "peer-c"
	p.nb.mu.Unlock()
	app, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	value := mustCommand(t, p.b, "service.share", map[string]any{"name": "shared", "network": "tcp", "ports": "8080", "localPort": app.Addr().(*net.TCPAddr).Port, "peerIds": []string{"peer-a", "peer-c"}, "ttlSeconds": 60}).(map[string]any)
	p.nb.mu.Lock()
	dispatch := p.nb.fallback
	p.nb.mu.Unlock()
	handler, _ := dispatch(netip.AddrPortFrom(otherIP, 40000), netip.AddrPortFrom(p.nb.ip, 8080))
	if handler == nil {
		t.Fatal("other allowed peer could not select the share")
	}
	mustCommand(t, p.b, "peer.reconnect", map[string]string{"peerId": "peer-a"})
	client, server := net.Pipe()
	defer client.Close()
	finished := make(chan struct{})
	go func() { defer close(finished); handler(server) }()
	go func() {
		conn, err := app.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("ok"))
	}()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("reconnecting one peer reset another peer's healthy share: %q, %v", buf, err)
	}
	client.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("shared flow did not stop")
	}
	if p.b.serviceViews()[0]["expiresAt"] != value["expiresAt"] {
		t.Fatal("peer reconnect renewed the share deadline")
	}
}

func TestNewManagementPortRevokesMappedShare(t *testing.T) {
	p := newCorePair(t)
	value := mustCommand(t, p.b, "service.share", map[string]any{"name": "mapped-control-conflict", "network": "tcp", "ports": "8080", "localPort": 9000, "peerIds": []string{"peer-a"}, "ttlSeconds": 60}).(map[string]any)
	id := value["id"].(string)
	withServiceOperation(p.b, func() {
		active := p.b.active[id]
		// StartWeb reserves its OS-selected management port through this same
		// operation; the injected port keeps the conflict deterministic.
		p.b.reserveServicePort("tcp", 9000)
		if p.b.active[id] != nil || active.guard() == nil || p.b.serviceStates[id] != "stopped" {
			t.Fatal("new management target remained exposed through a mapped share")
		}
	})
	if savedService(t, p.b, id).Active {
		t.Fatal("management conflict silently restarted the share")
	}
}
