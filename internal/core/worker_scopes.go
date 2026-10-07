package core

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"net/netip"
	"sort"
	"time"
)

// syncWorkerTCPScopes reduces worker admission before acknowledging a parent
// permission change. A failed update retires the worker rather than retaining
// old permissions behind a successful parent reply.
func (c *Core) syncWorkerTCPScopes() error {
	return c.syncWorkerTCPScopesWithFailure(nil)
}

func (c *Core) syncWorkerTCPScopesWithFailure(failure *peerScopeFailure) error {
	node := c.nodeCopy()
	fail := func(cause error) error {
		if failure != nil {
			return failure.fail(cause)
		}
		return c.failWorkerScope(node, cause)
	}
	if failure != nil {
		if failure.node != node {
			return failure.fail(errors.New("network backend changed during scoped revocation"))
		}
		if err := failure.failure(); err != nil {
			return err
		}
	}
	scoped, ok := node.(interface {
		SetTCPScopes(context.Context, []backendworker.TCPPolicy) error
	})
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if failure != nil {
		ctx = context.WithValue(ctx, peerScopeFailureKey{}, failure)
	}
	c.mu.RLock()
	var active []*activeService
	for _, a := range c.active {
		if a.spec.Direction == "share" && a.spec.Network == "tcp" && a.ready.Load() && a.permissionActiveAt(time.Now()) {
			active = append(active, a)
		}
	}
	c.mu.RUnlock()
	sort.Slice(active, func(i, j int) bool { return active[i].spec.ID < active[j].spec.ID })
	scopes := make([]backendworker.TCPPolicy, 0, len(active))
	if len(active) > 0 {
		state, e := node.State(ctx)
		if e != nil {
			return fail(e)
		}
		peers := map[string][]netip.Addr{}
		for _, p := range state.Snapshot.Peers {
			if !p.Expired {
				peers[p.ID] = p.IPs
			}
		}
		for _, a := range active {
			s := backendworker.TCPPolicy{Address: a.address, Expires: a.expires, UntilRevoked: a.spec.Lifetime == "until-revoked"}
			for _, iv := range a.effective.Intervals() {
				s.Ports = append(s.Ports, [2]uint16{iv.First, iv.Last})
			}
			for _, id := range a.spec.PeerIDs {
				s.Peers = append(s.Peers, peers[id]...)
			}
			if len(s.Peers) == 0 {
				return fail(errors.New("worker grant identities are unavailable"))
			}
			sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Compare(s.Peers[j]) < 0 })
			scopes = append(scopes, s)
		}
	}
	if e := scoped.SetTCPScopes(ctx, scopes); e != nil {
		return fail(e)
	}
	return nil
}
func (c *Core) failWorkerScope(node NetworkBackend, cause error) error {
	c.networkReady.Store(false)
	c.mu.Lock()
	c.networkFatal = "backend worker permission state could not be confirmed; restart after reviewing saved permissions"
	c.mu.Unlock()
	if node != nil {
		_ = node.Close()
	}
	return cause
}
