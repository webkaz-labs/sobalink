//go:build soba_e2e

// The browser acceptance harness uses the production Core and embedded assets.
// Only the peer network is synthetic and entirely in-memory. It is excluded
// from ordinary builds and distribution; no real node is enrolled.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/core"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"github.com/webkaz-labs/tsnet-bridge/internal/webui"
	assets "github.com/webkaz-labs/tsnet-bridge/web"
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
	return identity.State{IPs: []netip.Addr{n.ip}, Backend: "Running", Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: n.remoteID, DNSName: n.remoteName, IPs: []netip.Addr{n.remoteIP}, Online: true}}}}, nil
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
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "browser harness failed:", err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("session-file", "", "private output file read by the browser test")
	flag.Parse()
	if *file == "" {
		return errors.New("--session-file is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dir, e := os.MkdirTemp("", "sobalink-browser-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	netw := &network{listeners: map[netip.AddrPort]*listener{}}
	aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	aNode := &node{network: netw, ip: aIP, remoteIP: bIP, id: "fixture-notebook", remoteID: "fixture-studio", remoteName: "Studio"}
	bNode := &node{network: netw, ip: bIP, remoteIP: aIP, id: "fixture-studio", remoteID: "fixture-notebook", remoteName: "Notebook"}
	a, e := core.Open(ctx, core.Options{Directory: filepath.Join(dir, "notebook"), Version: "browser-acceptance", NodeFactory: func(string, string) (core.NetworkBackend, error) { return aNode, nil }})
	if e != nil {
		return e
	}
	defer a.Close()
	b, e := core.Open(ctx, core.Options{Directory: filepath.Join(dir, "studio"), Version: "browser-acceptance", NodeFactory: func(string, string) (core.NetworkBackend, error) { return bNode, nil }})
	if e != nil {
		return e
	}
	defer b.Close()
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
	receiveDir := filepath.Join(dir, "received")
	if e := os.Mkdir(receiveDir, 0700); e != nil {
		return e
	}
	if e := command(ctx, a, "settings.update", map[string]string{"receiveDirectory": receiveDir}); e != nil {
		return e
	}
	if e := command(ctx, b, "message.send", map[string]string{"peerId": "fixture-notebook", "text": "Here are the notes for review."}); e != nil {
		return e
	}
	if e := command(ctx, a, "message.send", map[string]string{"peerId": "fixture-studio", "text": "Thanks. I will review the batch before saving."}); e != nil {
		return e
	}
	notes := filepath.Join(dir, "design-notes.txt")
	if e := os.WriteFile(notes, []byte("Fictional design notes for browser acceptance.\n"), 0600); e != nil {
		return e
	}
	if _, e := b.SendPaths(ctx, "fixture-notebook", []string{notes}); e != nil {
		return e
	}
	if e := command(ctx, b, "service.share", map[string]any{"name": "sample-web", "network": "tcp", "ports": "8080", "peerIds": []string{"fixture-notebook"}, "ttlSeconds": 3600, "purpose": "web", "discoverable": true}); e != nil {
		return e
	}
	_ = command(ctx, a, "peer.reconnect", map[string]string{"peerId": "fixture-studio"})
	files, e := assets.Assets()
	if e != nil {
		return e
	}
	url, code, e := a.StartWeb(files)
	if e != nil {
		return e
	}
	if e := config.WriteJSON(*file, map[string]string{"url": url, "code": code}); e != nil {
		return e
	}
	defer os.Remove(*file)
	fmt.Println("Production UI acceptance server ready with synthetic peers")
	<-ctx.Done()
	return nil
}
