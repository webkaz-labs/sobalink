package core

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"net"
	"net/netip"
	"reflect"
	"time"
)

func scopePermits(scopes []backendworker.TCPPolicy, source, destination netip.AddrPort) bool {
	for _, p := range scopes {
		if p.Address != destination.Addr() || (!p.UntilRevoked && !time.Now().Before(p.Expires)) {
			continue
		}
		peer := false
		for _, ip := range p.Peers {
			if ip == source.Addr() {
				peer = true
				break
			}
		}
		if !peer {
			continue
		}
		for _, ports := range p.Ports {
			if destination.Port() >= ports[0] && destination.Port() <= ports[1] {
				return true
			}
		}
	}
	return false
}
func (n *mixedBackend) SetTCPScopes(ctx context.Context, logical []backendworker.TCPPolicy) error {
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil && len(logical) > 0 {
		_ = n.Close()
		return e
	}
	scopes := map[string][]backendworker.TCPPolicy{}
	for _, p := range logical {
		if p.Address != n.self {
			return errors.New("mixed scope must use exact local identity")
		}
		for _, name := range n.order {
			if !states[name].Snapshot.Running {
				continue
			}
			var peers []netip.Addr
			for _, alias := range p.Peers {
				for id, rs := range routes {
					if mixedIP(id) != alias {
						continue
					}
					for _, r := range rs {
						if r.backend == name && !r.peer.Expired {
							peers = append(peers, r.peer.IPs...)
						}
					}
				}
			}
			if len(peers) == 0 {
				continue
			}
			for _, ip := range states[name].IPs {
				copy := p
				copy.Address = ip
				copy.Peers = peers
				scopes[name] = append(scopes[name], copy)
			}
		}
	}
	n.mu.Lock()
	same := reflect.DeepEqual(n.scopes, scopes)
	n.scopes = scopes
	n.mu.Unlock()
	if same {
		return nil
	}
	for _, name := range n.order {
		if worker, ok := n.nodes[name].(interface {
			SetTCPScopes(context.Context, []backendworker.TCPPolicy) error
		}); ok {
			if !states[name].Snapshot.Running && states[name].Backend == "unavailable" {
				continue
			}
			if e := worker.SetTCPScopes(ctx, scopes[name]); e != nil {
				if errors.Is(e, net.ErrClosed) || errors.Is(e, backendworker.ErrClosed) {
					continue
				}
				_ = n.Close()
				return e
			}
		}
	}
	return nil
}
func (n *mixedBackend) RegisterTCPFallback(handler func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	var unregisters []func()
	cleanup := func() {
		for _, f := range unregisters {
			f()
		}
	}
	for _, name := range n.order {
		register, ok := n.nodes[name].(interface {
			RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error)
		})
		if !ok {
			cleanup()
			return nil, errors.New("mixed backend lacks scoped TCP dispatch")
		}
		unregister, e := register.RegisterTCPFallback(func(source, destination netip.AddrPort) (func(net.Conn), bool) {
			n.mu.Lock()
			allowed := !n.closed && scopePermits(n.scopes[name], source, destination)
			n.mu.Unlock()
			if !allowed {
				return nil, false
			}
			return func(c net.Conn) {
				wrapped, e := n.wrap(name, c)
				if e != nil {
					_ = c.Close()
					return
				}
				remote, e := netip.ParseAddrPort(wrapped.RemoteAddr().String())
				if e != nil {
					_ = wrapped.Close()
					return
				}
				f, ok := handler(remote, netip.AddrPortFrom(n.self, destination.Port()))
				if !ok || f == nil {
					_ = wrapped.Close()
					return
				}
				f(wrapped)
			}, true
		})
		if e != nil {
			cleanup()
			return nil, e
		}
		unregisters = append(unregisters, unregister)
	}
	return cleanup, nil
}
