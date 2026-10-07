//go:build soba_e2e

// The browser acceptance harness uses the production Core and embedded assets.
// Peers are synthetic and entirely in-memory. A test-only wrapper supplies
// deterministic failure/recovery scenarios and rejects live enrollment. This
// binary is excluded from ordinary builds and distribution.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/webui"
	assets "github.com/webkaz-labs/sobalink/web"
)

type network struct {
	mu        sync.Mutex
	listeners map[netip.AddrPort]*listener
}
type node struct {
	network                  *network
	ip                       netip.Addr
	remoteIP                 netip.Addr
	id, remoteID, remoteName string
	managementPort           *atomic.Uint32
}
type listener struct {
	address netip.AddrPort
	queue   chan net.Conn
	done    chan struct{}
	once    sync.Once
}
type conn struct {
	net.Conn
	local, remote netip.AddrPort
}

func (c *conn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(c.local) }
func (c *conn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }
func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.queue:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *listener) Close() error             { l.once.Do(func() { close(l.done) }); return nil }
func (l *listener) Addr() net.Addr           { return net.TCPAddrFromAddrPort(l.address) }
func (n *node) Start() error                 { return nil }
func (n *node) Login(context.Context) error  { return nil }
func (n *node) Logout(context.Context) error { return nil }
func (n *node) State(context.Context) (identity.State, error) {
	state := identity.State{IPs: []netip.Addr{n.ip}, Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: n.remoteID, DNSName: n.remoteName, IPs: []netip.Addr{n.remoteIP}, Online: true}}}}
	if n.managementPort != nil && n.managementPort.Load() != 0 {
		state.ReservedPorts = []uint16{uint16(n.managementPort.Load())}
	}
	return state, nil
}
func (n *node) WhoIs(_ context.Context, source netip.AddrPort) (string, error) {
	if source.Addr() == n.remoteIP {
		return n.remoteID, nil
	}
	return "", errors.New("unknown synthetic peer")
}
func (n *node) Listen(protocol, address string) (net.Listener, error) {
	ap, e := netip.ParseAddrPort(address)
	if e != nil || protocol != "tcp" || ap.Addr() != n.ip {
		return nil, errors.New("invalid synthetic listener")
	}
	n.network.mu.Lock()
	defer n.network.mu.Unlock()
	if n.network.listeners[ap] != nil {
		return nil, errors.New("synthetic port already bound")
	}
	l := &listener{address: ap, queue: make(chan net.Conn), done: make(chan struct{})}
	n.network.listeners[ap] = l
	return l, nil
}
func (n *node) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("browser fixture does not supply UDP")
}
func (n *node) RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	return func() {}, nil
}
func (n *node) DialIP(ctx context.Context, protocol string, target netip.AddrPort) (net.Conn, error) {
	if protocol != "tcp" {
		return nil, errors.New("synthetic network is TCP")
	}
	n.network.mu.Lock()
	l := n.network.listeners[target]
	n.network.mu.Unlock()
	if l == nil {
		return nil, errors.New("synthetic peer is starting")
	}
	left, right := net.Pipe()
	source := netip.AddrPortFrom(n.ip, 41000)
	a := &conn{Conn: left, local: source, remote: target}
	b := &conn{Conn: right, local: target, remote: source}
	select {
	case l.queue <- b:
		return a, nil
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
func (n *node) Close() error {
	n.network.mu.Lock()
	defer n.network.mu.Unlock()
	for address, l := range n.network.listeners {
		if address.Addr() == n.ip {
			l.Close()
		}
	}
	return nil
}

func command(ctx context.Context, c *core.Core, name string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = c.Command(ctx, webui.Command{RequestID: fmt.Sprintf("fixture-%d", time.Now().UnixNano()), Name: name, Payload: b})
	return e
}

// The production HTTP handler still owns loopback/Origin/CSRF/auth/CSP checks.
// This wrapper is reached only after those checks, and never creates a network
// backend, account login or alternate endpoint itself.
type fixtureBackend struct {
	webui.Backend
	scenario           string
	managementPort     *atomic.Uint32
	uploadFailed       atomic.Bool
	serviceMu          sync.Mutex
	servicePort        uint16
	serviceReservation net.Listener
}

func (f *fixtureBackend) Snapshot(ctx context.Context) (map[string]any, error) {
	state, err := f.Backend.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if f.managementPort != nil {
		port := uint16(f.managementPort.Load())
		ports, _ := state["reservedPorts"].([]uint16)
		found := false
		for _, existing := range ports {
			found = found || existing == port
		}
		if port != 0 && !found {
			state["reservedPorts"] = append(append([]uint16(nil), ports...), port)
		}
	}
	return state, nil
}

func (f *fixtureBackend) Command(ctx context.Context, cmd webui.Command) (any, error) {
	if f.scenario == "saved-host" {
		// This scenario exercises only production reads and presentation setup.
		// No browser command may create identity, pairing, grants or listeners.
		if cmd.Name != "settings.update" {
			return nil, errors.New("saved-host fixture permits review only")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(cmd.Payload, &fields) != nil || len(fields) == 0 {
			return nil, errors.New("invalid presentation settings")
		}
		for name := range fields {
			if name != "locale" && name != "theme" {
				return nil, errors.New("saved-host fixture permits presentation settings only")
			}
		}
	}
	if cmd.Name == "lan.addresses" {
		// Never enumerate the CI machine's interfaces in browser evidence.
		return map[string]any{"addresses": []core.LANLocalAddress{{Interface: "fixture0", Address: "192.168.50.10", Prefix: "192.168.50.0/24"}}}, nil
	}
	if cmd.Name == "network.login" {
		return nil, errors.New("account login is unavailable in the browser fixture")
	}
	if cmd.Name == "network.configure" {
		var choice struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(cmd.Payload, &choice); err != nil {
			return nil, errors.New("invalid command payload")
		}
		if choice.Mode == "lan" || (offlineScenario(f.scenario) && choice.Mode != "none") {
			return nil, errors.New("network activation is unavailable in the offline browser fixture")
		}
	}
	if cmd.Name == "services.start" {
		// Batch start reaches Core's internal start path, so release only the
		// harness-reserved listener selected by these actual saved definitions.
		var selection struct {
			IDs   []string `json:"ids,omitempty"`
			Group string   `json:"group,omitempty"`
		}
		if json.Unmarshal(cmd.Payload, &selection) == nil {
			payload, _ := json.Marshal(selection)
			result, err := f.Backend.Command(ctx, webui.Command{RequestID: cmd.RequestID + "-fixture-selection", Name: "service.selection", Payload: payload})
			if err == nil {
				encoded, _ := json.Marshal(result)
				var reviewed struct {
					Services []core.ServiceSpec `json:"services"`
				}
				if json.Unmarshal(encoded, &reviewed) == nil {
					for _, service := range reviewed.Services {
						if service.Direction == "forward" && service.LocalPort == int(f.servicePort) {
							f.releaseServiceReservation()
						}
					}
				}
			}
		}
	}
	if cmd.Name == "service.connect" {
		var choice struct {
			LocalPort int `json:"localPort"`
		}
		if json.Unmarshal(cmd.Payload, &choice) == nil && choice.LocalPort == int(f.servicePort) {
			f.releaseServiceReservation()
		}
	}
	return f.Backend.Command(ctx, cmd)
}

func (f *fixtureBackend) releaseServiceReservation() {
	f.serviceMu.Lock()
	defer f.serviceMu.Unlock()
	if f.serviceReservation != nil {
		_ = f.serviceReservation.Close()
		f.serviceReservation = nil
	}
}

func (f *fixtureBackend) Upload(w http.ResponseWriter, r *http.Request) {
	if offlineScenario(f.scenario) || (f.scenario == "studio" && f.uploadFailed.CompareAndSwap(false, true)) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "upload_failed", "error": "Synthetic upload interruption; retry the reviewed batch"})
		return
	}
	f.Backend.Upload(w, r)
}

type privateSession struct {
	URL              string   `json:"url"`
	Code             string   `json:"code"`
	ReceiveDirectory string   `json:"receiveDirectory,omitempty"`
	Scenario         string   `json:"scenario"`
	Capabilities     []string `json:"capabilities"`
	LocalServicePort uint16   `json:"localServicePort,omitempty"`
	RouteUpdateFile  string   `json:"routeUpdateFile,omitempty"`
	RoutePeerID      string   `json:"routePeerId,omitempty"`
}

func receiveRecoveryScenario(s string) bool { return s == "receive-legacy" || s == "receive-damaged" }
func offlineScenario(s string) bool         { return s == "offline" || s == "routes" || s == "saved-host" }
func validScenario(s string) bool {
	return s == "studio" || offlineScenario(s) || receiveRecoveryScenario(s)
}

// Seed an existing, fictional saved profile before Core opens it. Recovery must
// come from the production accounting reader, never a substituted API snapshot.
// The supplied directories belong exclusively to this fixture's temporary root.
func prepareReceiveRecoveryFixture(directory, receiveDirectory, scenario string) error {
	if !receiveRecoveryScenario(scenario) {
		return nil
	}
	profile := core.Profile{
		Version:  1,
		Settings: core.Settings{Locale: "auto", Theme: "system", Network: "tailnet", Hostname: "Notebook", ReceiveDirectory: receiveDirectory},
		Peers:    []core.Trust{{ID: "fixture-studio", Name: "Studio", Network: "tailnet", Generation: 1, Autosave: true, Directory: receiveDirectory}},
		Services: []core.ServiceSpec{},
	}
	if err := config.SecureDir(directory); err != nil {
		return err
	}
	if err := config.WriteJSON(filepath.Join(directory, "sobalink.json"), profile); err != nil {
		return err
	}
	if scenario == "receive-damaged" {
		return os.WriteFile(filepath.Join(directory, "receive-accounting.json"), []byte("{fictional-receive-index damaged}\n"), 0600)
	}
	return nil
}

func scenarioCapabilities(scenario string) []string {
	if scenario == "saved-host" {
		return []string{"offline-network", "saved-host-review", "production-saved-state"}
	}
	if scenario == "routes" {
		return []string{"offline-network", "route-authorization", "prepared-route-edit"}
	}
	if receiveRecoveryScenario(scenario) {
		capabilities := []string{"service-lifecycle", "host-review", "application-stop", "receive-recovery", "saved-autosave"}
		if scenario == "receive-legacy" {
			return append(capabilities, "legacy-receive-review")
		}
		return append(capabilities, "damaged-receive-index")
	}
	return []string{"service-lifecycle", "offline-network", "failed-upload-retry", "host-review", "application-stop"}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "browser harness failed:", err)
		os.Exit(1)
	}
}
func run() (runErr error) {
	file := flag.String("session-file", "", "private output file read by the browser test")
	scenario := flag.String("scenario", "studio", "browser fixture: studio, offline, saved-host, routes, receive-legacy or receive-damaged")
	flag.Parse()
	if *file == "" {
		return errors.New("--session-file is required")
	}
	if !validScenario(*scenario) {
		return errors.New("--scenario must be studio, offline, saved-host, routes, receive-legacy or receive-damaged")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dir, e := os.MkdirTemp("", "sobalink-browser-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	receiveDir := filepath.Join(dir, "received")
	if e := os.Mkdir(receiveDir, 0700); e != nil {
		return e
	}
	if e := prepareReceiveRecoveryFixture(filepath.Join(dir, "notebook"), receiveDir, *scenario); e != nil {
		return e
	}
	if *scenario == "saved-host" {
		if err := core.PrepareSavedHostBrowserFixture(filepath.Join(dir, "notebook")); err != nil {
			return err
		}
	}
	routePeerID, routeUpdateFile := "", ""
	if *scenario == "routes" {
		routeUpdateFile = filepath.Join(filepath.Dir(*file), "route-update.json")
		defer os.Remove(routeUpdateFile)
		var err error
		routePeerID, err = core.PrepareRouteBrowserFixture(filepath.Join(dir, "notebook"), routeUpdateFile)
		if err != nil {
			return err
		}
	}
	netw := &network{listeners: map[netip.AddrPort]*listener{}}
	managementPort := new(atomic.Uint32)
	aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	aNode := &node{network: netw, ip: aIP, remoteIP: bIP, id: "fixture-notebook", remoteID: "fixture-studio", remoteName: "Studio", managementPort: managementPort}
	bNode := &node{network: netw, ip: bIP, remoteIP: aIP, id: "fixture-studio", remoteID: "fixture-notebook", remoteName: "Notebook"}
	a, e := core.Open(ctx, core.Options{Directory: filepath.Join(dir, "notebook"), Version: "browser-acceptance", SkipNetworkStart: true, NodeFactory: func(string, string) (core.NetworkBackend, error) {
		if offlineScenario(*scenario) {
			return nil, errors.New("offline browser fixture cannot activate a network")
		}
		return aNode, nil
	}})
	if e != nil {
		return e
	}
	defer func() { runErr = errors.Join(runErr, a.Close()) }()
	if e := command(ctx, a, "settings.update", map[string]string{"receiveDirectory": receiveDir}); e != nil {
		return e
	}
	if !offlineScenario(*scenario) {
		b, e := core.Open(ctx, core.Options{Directory: filepath.Join(dir, "studio"), Version: "browser-acceptance", NodeFactory: func(string, string) (core.NetworkBackend, error) { return bNode, nil }})
		if e != nil {
			return e
		}
		defer func() { runErr = errors.Join(runErr, b.Close()) }()
		for _, item := range []struct {
			app        *core.Core
			name, peer string
		}{{a, "Notebook", "fixture-studio"}, {b, "Studio", "fixture-notebook"}} {
			if e := command(ctx, item.app, "network.configure", map[string]string{"mode": "tailnet", "hostname": item.name}); e != nil {
				return e
			}
			if e := command(ctx, item.app, "peer.trust", map[string]any{"peerId": item.peer, "trusted": true}); e != nil {
				return e
			}
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			e = command(ctx, a, "peer.reconnect", map[string]string{"peerId": "fixture-studio"})
			if e == nil {
				break
			}
			if time.Now().After(deadline) {
				return e
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if e := command(ctx, b, "message.send", map[string]string{"peerId": "fixture-notebook", "text": "Here are the notes for review."}); e != nil {
			return e
		}
		if e := command(ctx, a, "message.send", map[string]string{"peerId": "fixture-studio", "text": "Thanks. I will review the batch before saving."}); e != nil {
			return e
		}
		if *scenario == "studio" {
			notes := filepath.Join(dir, "design-notes.txt")
			if e := os.WriteFile(notes, []byte("Fictional design notes for browser acceptance.\n"), 0600); e != nil {
				return e
			}
			if _, e := b.SendPaths(ctx, "fixture-notebook", []string{notes}); e != nil {
				return e
			}
		}
		if e := command(ctx, b, "service.share", map[string]any{"name": "sample-web", "network": "tcp", "ports": "8080", "peerIds": []string{"fixture-notebook"}, "lifetime": "finite", "ttlSeconds": 3600, "purpose": "web", "discoverable": true}); e != nil {
			return e
		}
		if e := command(ctx, a, "discovery.refresh", map[string]string{"peerId": "fixture-studio"}); e != nil {
			return e
		}
	} else if *scenario != "routes" && *scenario != "saved-host" {
		if e := command(ctx, a, "network.configure", map[string]string{"mode": "none", "hostname": "Notebook"}); e != nil {
			return e
		}
	}
	files, e := assets.Assets()
	if e != nil {
		return e
	}
	backend := &fixtureBackend{Backend: a, scenario: *scenario, managementPort: managementPort}
	if !offlineScenario(*scenario) {
		// Reserve a numeric loopback port until the exact UI connect action.
		// Core then owns the real listener and its ordinary stop lifecycle.
		reservation, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer reservation.Close()
		backend.serviceReservation = reservation
		backend.servicePort = reservation.Addr().(*net.TCPAddr).AddrPort().Port()
	}
	server, e := webui.Start(ctx, files, backend)
	if e != nil {
		return e
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		runErr = errors.Join(runErr, server.Close(closeCtx))
	}()
	managementPort.Store(uint32(server.Port()))
	code, e := server.IssueCode()
	if e != nil {
		return e
	}
	metadata := privateSession{URL: server.URL(), Code: code, ReceiveDirectory: receiveDir, Scenario: *scenario, Capabilities: scenarioCapabilities(*scenario), LocalServicePort: backend.servicePort, RoutePeerID: routePeerID, RouteUpdateFile: routeUpdateFile}
	if e := config.WriteJSON(*file, metadata); e != nil {
		return e
	}
	defer os.Remove(*file)
	fmt.Println("Production UI acceptance server ready with isolated fixtures")
	select {
	case <-ctx.Done():
	case <-a.Done():
	}
	return nil
}
