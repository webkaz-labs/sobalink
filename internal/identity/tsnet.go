// Package identity contains the pinned, unstable tsnet API boundary.
package identity

import (
	"context"
	"errors"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

type State struct {
	Backend  string
	AuthURL  string
	Snapshot policy.Snapshot
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
	// Diagnostic upload is disabled before tsnet starts. No application analytics.
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
	for _, p := range s.Peer {
		out.Snapshot.Peers = append(out.Snapshot.Peers, policy.Peer{ID: string(p.ID), DNSName: p.DNSName, IPs: p.TailscaleIPs, Expired: p.Expired})
	}
	return out, nil
}
func (n *Node) Login(ctx context.Context) error  { return n.client.StartLoginInteractive(ctx) }
func (n *Node) Logout(ctx context.Context) error { return n.client.Logout(ctx) }
func (n *Node) Close() error                     { return n.s.Close() }
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
