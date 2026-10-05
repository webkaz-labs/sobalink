package backendworker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// TCPPolicy is a reviewed child-side admission scope. The parent still checks
// the current resource grant after acceptance. No catch-all port rule is implicit.
type TCPPolicy struct {
	Address      netip.Addr   `json:"address"`
	Ports        [][2]uint16  `json:"ports"`
	Peers        []netip.Addr `json:"peers"`
	Expires      time.Time    `json:"expires"`
	UntilRevoked bool         `json:"untilRevoked"`
}
type fallbackPolicies struct {
	policies   []TCPPolicy
	generation uint64
}
type fallbackIncoming struct {
	conn                net.Conn
	source, destination netip.AddrPort
	generation          uint64
}
type fallbackState struct {
	mu         sync.Mutex
	policies   atomic.Pointer[fallbackPolicies]
	incoming   chan fallbackIncoming
	done       chan struct{}
	unregister func()
	closed     bool
	generation uint64
	handles    map[uint64]bool
}

func validateTCPPolicies(policies []TCPPolicy) error {
	for _, p := range policies {
		if !p.Address.IsValid() || p.Address.IsUnspecified() || p.Address.Zone() != "" || len(p.Ports) == 0 || len(p.Peers) == 0 || p.Expires.IsZero() != p.UntilRevoked {
			return ErrProtocol
		}
		for _, ports := range p.Ports {
			if ports[0] == 0 || ports[1] < ports[0] {
				return ErrProtocol
			}
			if ports[0] <= 54545 && ports[1] >= 54543 {
				return ErrProtocol
			}
		}
		for _, ip := range p.Peers {
			if !ip.IsValid() || ip.IsUnspecified() || ip.Zone() != "" {
				return ErrProtocol
			}
		}
	}
	return nil
}
func (f *fallbackState) selector(source, destination netip.AddrPort) (func(net.Conn), bool) {
	snapshot := f.policies.Load()
	if snapshot == nil {
		return nil, false
	}
	allowed := false
	for _, p := range snapshot.policies {
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
				allowed = true
				break
			}
		}
		if allowed {
			break
		}
	}
	if !allowed {
		return nil, false
	}
	return func(c net.Conn) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.closed {
			_ = c.Close()
			return
		}
		select {
		case f.incoming <- fallbackIncoming{c, source, destination, snapshot.generation}:
		default:
			_ = c.Close()
		}
	}, true

}
func (h *engineHost) fallbackHandle(ctx context.Context, method string, q engineRequest) (json.RawMessage, error) {
	h.mu.Lock()
	f := h.fallback
	h.mu.Unlock()
	switch method {
	case "fallback-enable":
		if f != nil {
			return json.Marshal(engineResponse{})
		}
		register, ok := h.engine.(interface {
			RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error)
		})
		if !ok {
			return nil, errors.New("worker backend lacks scoped TCP dispatch")
		}
		f = &fallbackState{incoming: make(chan fallbackIncoming, 16), done: make(chan struct{}), handles: map[uint64]bool{}}
		f.policies.Store(&fallbackPolicies{})
		unregister, e := register.RegisterTCPFallback(f.selector)
		if e != nil {
			return nil, e
		}
		f.unregister = unregister
		h.mu.Lock()
		if h.fallback != nil || h.closed {
			h.mu.Unlock()
			unregister()
			return nil, ErrClosed
		}
		h.fallback = f
		h.mu.Unlock()
		return json.Marshal(engineResponse{})
	case "fallback-scopes":
		if f == nil && len(q.Scopes) == 0 {
			return json.Marshal(engineResponse{})
		}
		if f == nil {
			return nil, ErrClosed
		}
		if e := validateTCPPolicies(q.Scopes); e != nil {
			return nil, e
		}
		// Check local destination and authenticated peer addresses against this
		// engine's current state in its typed adapter before exposing any port.
		validator, ok := h.engine.(interface {
			ValidateTCPScopes(context.Context, []TCPPolicy) error
		})
		if !ok {
			return nil, ErrProtocol
		}
		if e := validator.ValidateTCPScopes(ctx, q.Scopes); e != nil {
			return nil, e
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return nil, ErrClosed
		}
		if reflect.DeepEqual(f.policies.Load().policies, q.Scopes) {
			f.mu.Unlock()
			return json.Marshal(engineResponse{})
		}
		f.generation++
		f.policies.Store(&fallbackPolicies{policies: q.Scopes, generation: f.generation})
		ids := make([]uint64, 0, len(f.handles))
		for id := range f.handles {
			ids = append(ids, id)
		}
		f.handles = map[uint64]bool{}
		f.mu.Unlock()
		for _, id := range ids {
			_ = h.remove(id)
		}
		return json.Marshal(engineResponse{})
	case "fallback-accept":
		if f == nil {
			return nil, ErrClosed
		}
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.done:
				return nil, ErrClosed
			case incoming := <-f.incoming:
				f.mu.Lock()
				valid := !f.closed && incoming.generation == f.generation
				f.mu.Unlock()
				if !valid {
					_ = incoming.conn.Close()
					continue
				}
				id, e := h.add(incoming.conn)
				if e != nil {
					return nil, e
				}
				f.mu.Lock()
				if f.closed || incoming.generation != f.generation {
					f.mu.Unlock()
					_ = h.remove(id)
					continue
				}
				f.handles[id] = true
				f.mu.Unlock()
				return json.Marshal(engineResponse{Handle: id, Network: "tcp", Local: incoming.destination.String(), Remote: incoming.source.String()})
			}
		}
	case "fallback-disable":
		h.closeFallback()
		return json.Marshal(engineResponse{})
	default:
		return nil, ErrProtocol
	}
}
func (h *engineHost) closeFallback() {
	h.mu.Lock()
	f := h.fallback
	h.fallback = nil
	h.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.policies.Store(&fallbackPolicies{})
	close(f.done)
	ids := make([]uint64, 0, len(f.handles))
	for id := range f.handles {
		ids = append(ids, id)
	}
	f.handles = map[uint64]bool{}
	f.mu.Unlock()
	f.unregister()
	for _, id := range ids {
		_ = h.remove(id)
	}
	for {
		select {
		case c := <-f.incoming:
			_ = c.conn.Close()
		default:
			return
		}
	}
}
func (e *RemoteEngine) SetTCPScopes(ctx context.Context, scopes []TCPPolicy) error {
	return e.client.Call(ctx, "fallback-scopes", engineRequest{Scopes: scopes}, nil)
}
func (e *RemoteEngine) RegisterTCPFallback(handler func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	if handler == nil {
		return nil, ErrProtocol
	}
	if err := e.client.Call(e.ctx, "fallback-enable", engineRequest{}, nil); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			var out engineResponse
			if err := e.client.Call(e.ctx, "fallback-accept", engineRequest{}, &out); err != nil {
				return
			}
			conn := e.conn(out)
			src, se := netip.ParseAddrPort(out.Remote)
			dst, de := netip.ParseAddrPort(out.Local)
			if se != nil || de != nil {
				_ = conn.Close()
				continue
			}
			select {
			case <-done:
				_ = conn.Close()
				return
			default:
			}
			f, ok := handler(src, dst)
			if !ok || f == nil {
				_ = conn.Close()
				continue
			}
			go f(conn)
		}
	}()
	return func() {
		once.Do(func() { close(done); _ = e.client.Call(e.ctx, "fallback-disable", engineRequest{}, nil) })
	}, nil
}
