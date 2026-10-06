package connectionroute

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

var ErrDenied = errors.New("connection route is not authorized")
var ErrUnavailable = errors.New("connection route is unavailable")
var ErrCapacity = errors.New("connection route flow budget exhausted")

type Request struct {
	PeerID, ResourceID, Network string
	Port                        uint16
}

// Route implementations live in isolated backend workers. Authorize must check
// current backend authentication and the exact resource grant on every dial.
// It must return ErrDenied for permission failure. Only ErrUnavailable permits
// another explicitly approved route; denial is never bypassed by fallback.
type Route interface {
	ID() string
	Backend() string
	Authorize(context.Context, Request) error
	Dial(context.Context, Request) (net.Conn, error)
}

// Policy's order is user approved. A strict LAN boundary permits only direct-lan
// and guarded-lan route implementations; it must also prevent starting external
// backend workers at application startup, not merely prevent their selection.
type Policy struct {
	RouteIDs       []string
	StrictLAN      bool
	AttemptTimeout time.Duration
	HoldDown       time.Duration
	MaxFlows       int
}
type Router struct {
	mu         sync.Mutex
	policy     Policy
	routes     map[string]Route
	generation uint64
	closed     bool
	selected   map[string]string
	until      map[string]time.Time
	flows      map[*flow]bool
	pending    int
}

func New(policy Policy, routes []Route) (*Router, error) {
	if len(policy.RouteIDs) == 0 || len(policy.RouteIDs) > 8 || policy.AttemptTimeout <= 0 || policy.AttemptTimeout > 30*time.Second || policy.HoldDown < 0 || policy.MaxFlows < 1 {
		return nil, ErrDenied
	}
	m := make(map[string]Route)
	for _, r := range routes {
		if r == nil || r.ID() == "" || m[r.ID()] != nil {
			return nil, ErrDenied
		}
		m[r.ID()] = r
	}
	seen := map[string]bool{}
	for _, id := range policy.RouteIDs {
		r := m[id]
		if r == nil || seen[id] {
			return nil, ErrDenied
		}
		seen[id] = true
		if policy.StrictLAN && r.Backend() != "direct-lan" && r.Backend() != "guarded-lan" {
			return nil, ErrDenied
		}
	}
	policy.RouteIDs = append([]string(nil), policy.RouteIDs...)
	return &Router{policy: policy, routes: m, selected: map[string]string{}, until: map[string]time.Time{}, flows: map[*flow]bool{}}, nil
}
func requestKey(q Request) string { return q.PeerID + "\x00" + q.ResourceID + "\x00" + q.Network }
func (r *Router) Dial(ctx context.Context, q Request) (net.Conn, error) {
	if q.PeerID == "" || q.ResourceID == "" || len(q.PeerID) > 512 || len(q.ResourceID) > 512 || strings.ContainsRune(q.PeerID, 0) || strings.ContainsRune(q.ResourceID, 0) || q.Port == 0 || (q.Network != "tcp" && q.Network != "udp") {
		return nil, ErrDenied
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, net.ErrClosed
	}
	if len(r.flows)+r.pending >= r.policy.MaxFlows {
		r.mu.Unlock()
		return nil, ErrCapacity
	}
	generation := r.generation
	r.pending++
	ids := append([]string(nil), r.policy.RouteIDs...)
	key := requestKey(q)
	if time.Now().Before(r.until[key]) {
		selected := r.selected[key]
		for i, id := range ids {
			if id == selected {
				copy(ids[1:i+1], ids[0:i])
				ids[0] = selected
				break
			}
		}
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.pending--; r.mu.Unlock() }()
	for _, id := range ids {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		route := r.routes[id]
		attempt, cancel := context.WithTimeout(ctx, r.policy.AttemptTimeout)
		e := route.Authorize(attempt, q)
		if e != nil {
			cancel()
			if errors.Is(e, ErrUnavailable) {
				continue
			}
			return nil, e
		}
		conn, e := route.Dial(attempt, q)
		cancel()
		if e != nil {
			if conn != nil {
				_ = conn.Close()
			}
			if errors.Is(e, ErrUnavailable) {
				continue
			}
			return nil, e
		}
		if conn == nil {
			return nil, ErrUnavailable
		}
		// Recheck permission after a potentially slow connection attempt. The caller
		// must never receive a connection whose resource was revoked during dialing.
		if e = route.Authorize(ctx, q); e != nil {
			_ = conn.Close()
			return nil, e
		}
		f := &flow{Conn: conn, owner: r}
		r.mu.Lock()
		if r.closed || generation != r.generation {
			r.mu.Unlock()
			_ = conn.Close()
			return nil, ErrDenied
		}
		r.flows[f] = true
		if len(r.selected) >= r.policy.MaxFlows {
			clear(r.selected)
			clear(r.until)
		}
		r.selected[key] = id
		r.until[key] = time.Now().Add(r.policy.HoldDown)
		r.mu.Unlock()
		return f, nil
	}
	return nil, ErrUnavailable
}

// Invalidate closes all existing flows and cancels eligibility of in-flight
// dials after a binding or permission reduction. It never extends a lifetime.
func (r *Router) Invalidate() {
	r.mu.Lock()
	r.generation++
	flows := make([]*flow, 0, len(r.flows))
	for f := range r.flows {
		flows = append(flows, f)
	}
	clear(r.selected)
	clear(r.until)
	r.mu.Unlock()
	for _, f := range flows {
		_ = f.Close()
	}
}
func (r *Router) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.Invalidate()
	return nil
}

type flow struct {
	net.Conn
	owner *Router
	once  sync.Once
	err   error
}

func (f *flow) Close() error {
	f.once.Do(func() { f.err = f.Conn.Close(); f.owner.mu.Lock(); delete(f.owner.flows, f); f.owner.mu.Unlock() })
	return f.err
}
