package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/connectionroute"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// peerHTTPRoute exists only before HTTP exposure. It captures the actual
// backend, exact destination and (for direct LAN) generation capability.
// It is never placed in the HTTP request/context or fixed DialContext callback.
type peerHTTPRoute struct {
	backend string
	mixed   bool
	origin  transportorigin.Origin
	dial    func(context.Context) (net.Conn, error)
}

func capturePeerHTTPBackend(ctx context.Context, backend string, node NetworkBackend, peer, host string, ap netip.AddrPort, proof bool) (*peerHTTPRoute, error) {
	route := &peerHTTPRoute{backend: backend}
	if direct, ok := node.(*directLANBackend); ok {
		capability, err := direct.Node.CaptureDial(peer, "tcp", ap.Port())
		if err != nil {
			return nil, err
		}
		route.origin = capability.Origin()
		if route.origin == nil || route.origin.Identity() == nil {
			return nil, errors.New("direct LAN request has no generation origin")
		}
		route.dial = capability.Dial
	} else {
		// Tailnet and LAN retain their existing backend and exact numeric route.
		// This closure is consumed by the owner before Transport sees a request.
		route.dial = func(ctx context.Context) (net.Conn, error) { return node.DialIP(ctx, "tcp", ap) }
	}
	dial := route.dial
	route.dial = func(ctx context.Context) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil || conn == nil {
			return conn, err
		}
		if route.origin != nil {
			actual, ok := conn.(transportorigin.Connection)
			if !ok || actual.TransportOrigin() == nil || actual.TransportOrigin().Identity() != route.origin.Identity() {
				return conn, errors.New("peer connection generation origin changed")
			}
			if _, ok := conn.(interface{ WaitClosed(context.Context) error }); !ok {
				return conn, errors.New("peer connection has no terminal close acknowledgement")
			}
		}
		if proof {
			check := func() error {
				id, e := observedBackendPeer(ctx, node, conn)
				if e != nil || id != peer {
					return connectionroute.ErrDenied
				}
				return nil
			}
			if err := check(); err != nil {
				return conn, err
			}
			return &proofIdentityConn{Conn: conn, check: check}, nil
		}
		// Preserve the existing policy's post-dial identity/address validation,
		// now using the captured backend rather than a current Core lookup.
		state, err := node.State(ctx)
		if err != nil || !state.Snapshot.Running {
			return conn, errors.New("peer information unavailable")
		}
		if host == "" {
			host = ap.Addr().String()
		}
		p := &policy.Policy{Rules: []policy.Rule{{PeerID: peer, Host: host, Port: int(ap.Port()), Network: "tcp"}}, Source: func(context.Context) (policy.Snapshot, error) { return state.Snapshot, nil }}
		resolved, err := p.Resolve(ctx, "tcp", ap.String())
		if err != nil || resolved != ap {
			return conn, errors.New("peer destination changed during dial")
		}
		return conn, nil
	}
	return route, nil
}

func (c *Core) capturePeerHTTPRoute(ctx context.Context, id string, attempted map[string]bool) (*peerHTTPRoute, error) {
	c.mu.RLock()
	node, network, closing := c.node, c.profile.Settings.Network, c.closing
	c.mu.RUnlock()
	if closing || node == nil || ctx.Err() != nil || c.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	if mixed, ok := node.(*mixedBackend); ok {
		return mixed.capturePeerHTTPRoute(ctx, id, attempted)
	}
	stateCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := node.State(stateCtx)
	if err != nil || !state.Snapshot.Running {
		return nil, errors.New("network is not connected")
	}
	for _, peer := range state.Snapshot.Peers {
		if peer.ID != id || peer.Expired || len(peer.IPs) == 0 {
			continue
		}
		host := peer.DNSName
		if host == "" {
			host = peer.IPs[0].String()
		}
		p := &policy.Policy{Rules: []policy.Rule{{PeerID: id, Host: host, Port: PeerPort, Network: "tcp"}}, Source: func(context.Context) (policy.Snapshot, error) { return state.Snapshot, nil }}
		ap, err := p.Resolve(ctx, "tcp", config.Address(host, PeerPort))
		if err != nil {
			return nil, err
		}
		return capturePeerHTTPBackend(ctx, network, node, id, host, ap, false)
	}
	return nil, errors.New("peer is no longer present with the approved identity")
}

func (n *mixedBackend) capturePeerHTTPRoute(ctx context.Context, id string, attempted map[string]bool) (*peerHTTPRoute, error) {
	routes, states, err := n.routeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := n.routeReadiness(id, routes, states); err != nil {
		return nil, err
	}
	for _, route := range routes[id] {
		if attempted[route.backend] || mixedReadiness(states[route.backend]) == mixedAbsent {
			continue
		}
		captured, err := capturePeerHTTPBackend(ctx, route.backend, n.nodes[route.backend], route.id, route.peer.DNSName, netip.AddrPortFrom(route.address, PeerPort), true)
		if err != nil {
			if ctx.Err() == nil && mixedUnavailable(err) {
				// Capture can lose the same availability race as an unopened
				// DialIP. Retry only another actual backend from a fresh view.
				attempted[route.backend] = true
				return n.capturePeerHTTPRoute(ctx, id, attempted)
			}
			return nil, err
		}
		captured.mixed = true
		dial := captured.dial
		captured.dial = func(ctx context.Context) (net.Conn, error) {
			conn, err := dial(ctx)
			if err != nil || conn == nil {
				return conn, err
			}
			// The former policy.Dial revalidated the logical identity after
			// mixedBackend.DialIP returned. Preserve that exact post-dial check.
			current, states, err := n.routeSnapshot(ctx)
			if err != nil {
				return conn, err
			}
			if err := n.routeReadiness(id, current, states); err != nil {
				return conn, err
			}
			for _, actual := range current[id] {
				if actual.backend == route.backend && actual.id == route.id && actual.address == route.address {
					return conn, nil
				}
			}
			return conn, connectionroute.ErrDenied
		}
		return captured, nil
	}
	return nil, connectionroute.ErrUnavailable
}
