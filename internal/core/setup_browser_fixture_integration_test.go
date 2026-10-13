//go:build lanlink_integration

package core

import (
	"context"
	"time"
)

// Test executable only: product scope enumeration still rejects loopback.
// The nearby adapter also rejects it, exercising the explicit copied-contact
// fallback rather than implying native multicast or physical-LAN acceptance.
func installBrowserSetupScope(c *Core) {
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic-loopback", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "isolated-test"}
	scope.ScopeID = setupScopeID(scope)
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
}

type setupBrowserEvidence struct {
	Backend       bool `json:"backend"`
	Pairs         int  `json:"pairs"`
	Trusts        int  `json:"trusts"`
	StrictScope   bool `json:"strictScope"`
	Joined        bool `json:"joined"`
	LocalHello    bool `json:"localHello"`
	RemoteReceipt bool `json:"remoteReceipt"`
}

// Counts and proofs come from genuine protected Core/backend state. No browser
// claim is accepted as evidence of pairing, trust, hello, or physical closure.
func browserSetupEvidence(c *Core) setupBrowserEvidence {
	v := setupBrowserEvidence{Backend: c.nodeCopy() != nil, Trusts: len(c.profileCopy().Peers)}
	if store := c.lanStoreCopy(); store != nil {
		saved := store.copy()
		v.Pairs, v.StrictScope = len(saved.Trust.Peers), saved.DestinationPolicy.Strict()
	}
	if s := c.setupCopy(); s != nil {
		s.mu.Lock()
		v.LocalHello, v.RemoteReceipt = s.localConnected, s.remoteConnected
		s.mu.Unlock()
		select {
		case <-s.done:
			v.Joined = true
		default:
		}
	}
	return v
}

func browserSetupHello(ctx context.Context, c *Core) bool {
	s := c.setupCopy()
	if s == nil {
		return false
	}
	s.mu.Lock()
	peer := ""
	if s.peer != nil {
		peer = s.peer.Key
	}
	s.mu.Unlock()
	if peer == "" {
		return false
	}
	call, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var hello struct {
		Protocol int    `json:"protocol"`
		Product  string `json:"product"`
	}
	return c.peerJSON(call, peer, "GET", "/v1/hello", nil, &hello) == nil && hello.Protocol == 1 && hello.Product == "sobalink"
}
