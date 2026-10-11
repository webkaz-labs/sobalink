package core

import (
	"context"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"testing"
)

func TestCatalogViewPeerHTTPRouteExactPostOnly(t *testing.T) {
	cases := []struct {
		method, path   string
		proof, allowed bool
	}{
		{"POST", catalogViewPath, false, true},
		{"GET", catalogViewPath, false, false},
		{"PUT", catalogViewPath, false, false},
		{"DELETE", catalogViewPath, false, false},
		{"POST", catalogViewPath, true, false},
		{"POST", catalogViewPath + "?peer=other", false, false},
		{"POST", catalogViewPath + "/other", false, false},
		{"POST", "/v1/catalog/%73ervices", false, false},
		{"POST", catalogViewPath + "#fragment", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			target, upload, err := validatePeerHTTPRequest(tc.method, tc.path, tc.proof)
			if tc.allowed {
				if err != nil || target == nil || target.Host != "peer.invalid" || target.Path != catalogViewPath || upload {
					t.Fatal("exact read-only POST selector route rejected", err)
				}
			} else if err == nil {
				t.Fatal("catalog endpoint scope expanded", target)
			}
		})
	}
}

// Catalog requests use the existing bounded normal HTTP entrance; admission
// fails before encoding, route selection or any transport/network work.
func TestCatalogViewPeerHTTPRouteHonorsExistingEntranceBudget(t *testing.T) {
	c := &Core{ctx: context.Background(), capacity: capacity.Defaults()}
	c.capacity.Resources["tcpConnections"] = capacity.Limited(1)
	front, err := c.peerHTTPFront()
	if err != nil {
		t.Fatal(err)
	}
	defer front.close()
	release, err := front.reserve(peerHTTPPreparingLease, 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// This value cannot be JSON encoded. A capacity error establishes that the
	// ordinary budget is checked before even private selector preparation.
	err = c.peerJSON(context.Background(), "synthetic-peer", "POST", catalogViewPath, make(chan int), nil)
	if !errors.Is(err, errPeerHTTPCapacity) {
		t.Fatal("catalog route bypassed existing entrance budget", err)
	}
	front.mu.Lock()
	retained := front.leases[peerHTTPPreparingLease]
	front.mu.Unlock()
	if retained != 1 {
		t.Fatal("rejected catalog request leaked admission accounting")
	}
}
