package core

import (
	"context"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
	"time"
)

// startReviewedProxy is shared by explicit ephemeral and saved starts. A supplied
// deadline is a recovery of the existing grant, never a lifetime renewal.
func (c *Core) startReviewedProxy(ctx context.Context, review proxyReview, username, password string, expires time.Time) (map[string]any, error) {
	return c.startProxyWithReservation(ctx, review, username, password, expires, "")
}

// A recoveryID is supplied only after validating a retained suspended run.
func (c *Core) startProxyWithReservation(ctx context.Context, review proxyReview, username, password string, expires time.Time, recoveryID string) (map[string]any, error) {
	c.mu.RLock()
	conflict := false
	for _, a := range c.proxies {
		if a.scope.Name == review.Scope.Name {
			conflict = true
		}
	}
	for _, run := range c.savedProxyRuns {
		if run.Suspended && run.Scope.Name == review.Scope.Name && (recoveryID == "" || recoveryID != run.ID) {
			conflict = true
		}
	}
	c.mu.RUnlock()
	if conflict {
		return nil, &localCommandError{"proxy_name_conflict", "an active proxy already uses this name; stop it before reviewing a replacement"}
	}
	if recoveryID == "" && int64(c.materializedCount()) >= c.limit("resources", "materializedListeners") {
		return nil, &localCommandError{"proxy_listener_capacity", "local listener capacity reached; stop a listener or review capacity settings"}
	}
	lifetime, cancel := context.WithCancel(c.ctx)
	if !expires.IsZero() || review.Scope.Lifetime == "finite" {
		cancel()
		duration, _ := capacity.Duration(int64(review.Scope.TTLSeconds))
		if expires.IsZero() {
			expires = time.Now().Add(duration)
		}
		lifetime, cancel = context.WithDeadline(c.ctx, expires)
	}
	id := recoveryID
	if id == "" {
		id = randomID()
	}
	active := &activeProxy{id: id, scope: review.Scope, expires: expires, ctx: lifetime, cancel: cancel}
	node := c.nodeCopy()
	if node == nil {
		cancel()
		return nil, &localCommandError{"proxy_network_unavailable", "network unavailable; reconnect and review the proxy"}
	}
	pol := &policy.Policy{Guard: func() error { return lifetime.Err() }, OnRevoked: cancel, Source: func(ctx context.Context) (policy.Snapshot, error) {
		state, err := c.current(ctx)
		return state.Snapshot, err
	}, DialIP: node.DialIP}
	for _, t := range review.Targets {
		pol.Rules = append(pol.Rules, policy.Rule{PeerID: t.PeerID, Host: t.Host, Port: t.Port, Network: "tcp"})
	}
	active.policy = pol
	starter := c.proxyStart
	if starter == nil {
		starter = func(ctx context.Context, cfg transport.SOCKSConfig, dial transport.Dialer) (proxyServer, error) {
			return transport.StartSOCKS(ctx, cfg, dial)
		}
	}
	server, err := starter(lifetime, transport.SOCKSConfig{ListenAddress: review.Endpoint, Username: username, Password: password, Controller: c.serviceResources(), PolicyID: active.id}, c.proxyDial(active))
	username, password = "", ""
	if err != nil {
		cancel()
		if server != nil {
			_ = server.Close()
		}
		return nil, &localCommandError{"proxy_listener_unavailable", "proxy listener could not start; choose a free loopback port and review again"}
	}
	if server == nil || ctx.Err() != nil || lifetime.Err() != nil {
		cancel()
		if server != nil {
			_ = server.Close()
		}
		return nil, &localCommandError{"proxy_start_cancelled", "proxy start was cancelled or expired; review before starting again"}
	}
	select {
	case <-server.Done():
		cancel()
		_ = server.Close()
		return nil, &localCommandError{"proxy_listener_unavailable", "proxy listener stopped before it became ready; review and retry"}
	default:
	}
	active.server = server
	c.mu.Lock()
	if c.proxies == nil {
		c.proxies = map[string]*activeProxy{}
	}
	c.proxies[active.id] = active
	c.mu.Unlock()
	// A local proxy is a management entry point, never a shareable service target.
	c.reserveServicePort("tcp", uint16(review.Scope.LocalPort))
	return proxyView(active), nil
}
