// Package identity contains the pinned, unstable tsnet API boundary.
package identity

import (
	"context"
	"errors"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"tailscale.com/client/local"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"
)

type State struct {
	IPs           []netip.Addr
	Backend       string
	AuthURL       string
	Snapshot      policy.Snapshot
	ReservedPorts []uint16
}
type Backend interface {
	Start() error
	State(context.Context) (State, error)
	Login(context.Context) error
	Logout(context.Context) error
	DialIP(context.Context, string, netip.AddrPort) (net.Conn, error)
	Close() error
}
type Node struct {
	s      *tsnet.Server
	client *local.Client
}

func New(dir, hostname string) (*Node, error) {
	// Interactive enrollment is the only supported flow. Do not silently consume
	// inherited keys, workload credentials, alternate control planes or test knobs.
	for _, k := range []string{"TS_AUTHKEY", "TS_AUTH_KEY", "TS_CLIENT_SECRET", "TS_CLIENT_ID", "TS_ID_TOKEN", "TS_AUDIENCE", "TS_CONTROL_URL", "TSNET_FORCE_LOGIN"} {
		if os.Getenv(k) != "" {
			return nil, errors.New("unsupported Tailscale authentication or control environment; remove TS_AUTHKEY/TS_AUTH_KEY/TS_CLIENT_* /TS_ID_TOKEN/TS_AUDIENCE/TS_CONTROL_URL/TSNET_FORCE_LOGIN")
		}
	}
	dir = filepath.Join(dir, "identity")
	if e := config.SecureDir(dir); e != nil {
		return nil, e
	}
	if e := filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("identity directory must not contain symlinks")
		}
		return config.Protect(p, d.IsDir())
	}); e != nil {
		return nil, e
	}
	// Disable before constructing tsnet: the environment knob suppresses upload
	// but the pinned logger would otherwise buffer private auth URLs on disk.
	logtail.Disable()
	// Defense in depth: retain disabled transport/upload and quiet callbacks.
	if e := os.Setenv("TS_NO_LOGS_NO_SUPPORT", "true"); e != nil {
		return nil, e
	}
	quiet := func(string, ...any) {}
	return &Node{s: &tsnet.Server{Dir: dir, Hostname: hostname, ControlURL: "https://controlplane.tailscale.com", UserLogf: quiet, Logf: quiet}}, nil
}
func (n *Node) Start() error {
	if e := n.s.Start(); e != nil {
		return e
	}
	c, e := n.s.LocalClient()
	n.client = c
	return e
}
func (n *Node) State(ctx context.Context) (State, error) {
	if n.client == nil {
		return State{}, errors.New("node not started")
	}
	s, e := n.client.Status(ctx)
	if e != nil {
		return State{}, e
	}
	out := State{Backend: s.BackendState, AuthURL: s.AuthURL, Snapshot: policy.Snapshot{Running: s.BackendState == "Running"}}
	if s.Self != nil {
		out.IPs = append([]netip.Addr(nil), s.Self.TailscaleIPs...)
		for _, raw := range s.Self.PeerAPIURL {
			u, err := url.Parse(raw)
			if err != nil {
				continue
			}
			p, err := strconv.Atoi(u.Port())
			if err == nil && p > 0 && p <= 65535 {
				out.ReservedPorts = append(out.ReservedPorts, uint16(p))
			}
		}
	}
	for _, p := range s.Peer {
		out.Snapshot.Peers = append(out.Snapshot.Peers, policy.Peer{ID: string(p.ID), DNSName: p.DNSName, IPs: p.TailscaleIPs, Expired: p.Expired, Online: p.Online})
	}
	return out, nil
}
func (n *Node) Login(ctx context.Context) error  { return n.client.StartLoginInteractive(ctx) }
func (n *Node) Logout(ctx context.Context) error { return n.client.Logout(ctx) }
func (n *Node) Close() error                     { return n.s.Close() }

// RegisterTCPFallback exposes the exact tsnet-only range dispatch boundary.
// The selector runs under tsnet's mutex and must only inspect immutable state.
func (n *Node) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	if n.client == nil {
		return nil, errors.New("node not started")
	}
	return n.s.RegisterFallbackTCPHandler(f), nil
}
func (n *Node) DialIP(ctx context.Context, network string, a netip.AddrPort) (net.Conn, error) {
	if !config.TailnetIP(a.Addr()) {
		return nil, errors.New("netstack destination must be a tailnet address")
	}
	// Never call Server.Dial/UserDial here: a peer-removal race can select an OS
	// fallback. Calling only the initialized netstack functions fails closed.
	d := n.s.Sys().Dialer.Get()
	return dialNetstack(ctx, network, a, d.NetstackDialTCP, d.NetstackDialUDP)
}

type netstackDial func(context.Context, netip.AddrPort) (net.Conn, error)

func dialNetstack(ctx context.Context, network string, a netip.AddrPort, tcp, udp netstackDial) (net.Conn, error) {
	if !config.TailnetIP(a.Addr()) {
		return nil, errors.New("invalid tailnet IP")
	}
	switch network {
	case "tcp":
		if tcp != nil {
			return tcp(ctx, a)
		}
	case "udp":
		if udp != nil {
			return udp(ctx, a)
		}
	}
	return nil, errors.New("netstack transport unavailable")
}

// InboundBackend binds only the embedded node's own tailnet addresses.
type InboundBackend interface {
	Listen(string, string) (net.Listener, error)
	ListenPacket(string, string) (net.PacketConn, error)
}

func (n *Node) validInbound(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || ap.Port() == 0 || !config.TailnetIP(ap.Addr()) {
		return errors.New("explicit tailnet address and port required")
	}
	if n.client == nil {
		return errors.New("node not started")
	}
	ip4, ip6 := n.s.TailscaleIPs()
	if ap.Addr() != ip4 && ap.Addr() != ip6 {
		return errors.New("listener must use this node's current tailnet address")
	}
	return nil
}
func (n *Node) Listen(network, address string) (net.Listener, error) {
	if network != "tcp" {
		return nil, errors.New("only TCP is supported")
	}
	if e := n.validInbound(address); e != nil {
		return nil, e
	}
	return n.s.Listen(network, address)
}
func (n *Node) ListenPacket(network, address string) (net.PacketConn, error) {
	if network != "udp" {
		return nil, errors.New("only UDP is supported")
	}
	if e := n.validInbound(address); e != nil {
		return nil, e
	}
	return n.s.ListenPacket(network, address)
}

// DiscoveryIdentity resolves an accepted tailnet connection using the local
// API on every request. The caller still must validate its pinned share scope.
type DiscoveryIdentity interface {
	WhoIs(context.Context, netip.AddrPort) (string, error)
}

func (n *Node) WhoIs(ctx context.Context, remote netip.AddrPort) (string, error) {
	if n.client == nil || !config.TailnetIP(remote.Addr()) || remote.Port() == 0 {
		return "", errors.New("current tailnet caller unavailable")
	}
	who, err := n.client.WhoIs(ctx, remote.String())
	if err != nil || who == nil || who.Node == nil || !config.ValidPeerID(string(who.Node.StableID)) {
		return "", errors.New("current tailnet caller unavailable")
	}
	return string(who.Node.StableID), nil
}
