// Package policy resolves only known peer identities, never system DNS.
package policy

import (
	"context"
	"errors"
	"fmt"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

type Peer struct {
	ID      string
	DNSName string
	IPs     []netip.Addr
	Expired bool
}
type Snapshot struct {
	Running bool
	Peers   []Peer
}
type Source func(context.Context) (Snapshot, error)
type DialIP func(context.Context, string, netip.AddrPort) (net.Conn, error)
type Rule struct {
	Host    string
	Port    int
	Network string
}
type Policy struct {
	Rules  []Rule
	Source Source
	DialIP DialIP
	mu     sync.Mutex
	active map[*liveConn]struct{}
}

func Normalize(s string) string { return strings.ToLower(strings.TrimSuffix(s, ".")) }
func (p *Policy) Resolve(ctx context.Context, network, address string) (netip.AddrPort, error) {
	endpoint, _, err := p.resolve(ctx, network, address)
	return endpoint, err
}

func (p *Policy) resolve(ctx context.Context, network, address string) (netip.AddrPort, string, error) {
	if p.Source == nil {
		return netip.AddrPort{}, "", errors.New("peer information unavailable")
	}
	snapshot, err := p.Source(ctx)
	if err != nil || ctx.Err() != nil {
		return netip.AddrPort{}, "", errors.New("peer information unavailable")
	}
	return p.resolveSnapshot(snapshot, network, address)
}

func (p *Policy) resolveSnapshot(s Snapshot, network, address string) (netip.AddrPort, string, error) {
	if network != "tcp" && network != "udp" {
		return netip.AddrPort{}, "", errors.New("network not permitted")
	}
	host, portText, e := net.SplitHostPort(address)
	if e != nil {
		return netip.AddrPort{}, "", errors.New("expected host:port")
	}
	port, e := strconv.Atoi(portText)
	if e != nil || port < 1 || port > 65535 {
		return netip.AddrPort{}, "", errors.New("invalid port")
	}
	var matches []Rule
	for _, r := range p.Rules {
		if r.Network == network && r.Port == port {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return netip.AddrPort{}, "", errors.New("destination is not allowlisted")
	}
	if !s.Running {
		return netip.AddrPort{}, "", errors.New("tailnet is not connected")
	}
	requested := findPeer(s, host)
	if requested == nil {
		return netip.AddrPort{}, "", errors.New("destination is not a current tailnet peer")
	}
	allowed := false
	for _, r := range matches {
		rp := findPeer(s, r.Host)
		if rp != nil && rp.ID == requested.ID {
			allowed = true
			break
		}
	}
	if !allowed {
		return netip.AddrPort{}, "", errors.New("destination is not allowlisted")
	}
	if ip, e := netip.ParseAddr(Normalize(host)); e == nil {
		return netip.AddrPortFrom(ip, uint16(port)), requested.ID, nil
	}
	for _, ip := range requested.IPs {
		if ip.Is4() && config.TailnetIP(ip) {
			return netip.AddrPortFrom(ip, uint16(port)), requested.ID, nil
		}
	}
	for _, ip := range requested.IPs {
		if config.TailnetIP(ip) {
			return netip.AddrPortFrom(ip, uint16(port)), requested.ID, nil
		}
	}
	return netip.AddrPort{}, "", errors.New("peer has no permitted tailnet address")
}

func findPeer(s Snapshot, host string) *Peer {
	host = Normalize(host)
	ip, iperr := netip.ParseAddr(host)
	var found *Peer
	for i := range s.Peers {
		p := &s.Peers[i]
		if p.Expired || p.ID == "" {
			continue
		}
		match := false
		if iperr == nil {
			if !config.TailnetIP(ip) {
				continue
			}
			for _, a := range p.IPs {
				if a == ip {
					match = true
				}
			}
		} else {
			dns := Normalize(p.DNSName)
			match = dns != "" && (host == dns || (!strings.Contains(host, ".") && host == strings.Split(dns, ".")[0]))
		}
		if match {
			if found != nil && found.ID != p.ID {
				return nil
			}
			found = p
		}
	}
	return found
}
func (p *Policy) Validate(ctx context.Context, network, address string) error {
	_, e := p.Resolve(ctx, network, address)
	return e
}
func (p *Policy) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	endpoint, peerID, err := p.resolve(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if p.DialIP == nil {
		return nil, fmt.Errorf("netstack dialer unavailable")
	}
	conn, err := p.DialIP(ctx, network, endpoint)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("netstack dialer returned no connection")
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	live := &liveConn{Conn: conn, policy: p, network: network, endpoint: endpoint, peerID: peerID, ctx: lifetime, cancel: cancel}
	p.mu.Lock()
	if p.active == nil {
		p.active = make(map[*liveConn]struct{})
	}
	p.active[live] = struct{}{}
	p.mu.Unlock()
	// Catch revocation or address reassignment during the underlying dial, too.
	if err := live.validate(ctx); err != nil {
		_ = live.Close()
		return nil, err
	}
	return live, nil
}
