// Copyright (c) 2026 sobalink contributors
// SPDX-License-Identifier: MIT

package routecat

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
)

func TestWANCandidatesOptInOwnedAndCanonical(t *testing.T) {
	if got, err := ValidateWANConfig(nil, true, []netip.Prefix{}); err != nil || got != nil {
		t.Fatal("nil WAN config changed existing behavior")
	}
	config := &WANConfig{STUNEndpoints: []netip.AddrPort{netip.MustParseAddrPort("[2001:db8::1]:3479"), netip.MustParseAddrPort("192.0.2.1:3478")}, AdvertiseIPv6: true}
	got, err := ValidateWANConfig(config, false, nil)
	if !buildfeatures.HasUDPTransport || !buildfeatures.HasNATTraversal {
		if !errors.Is(err, ErrWANBuild) {
			t.Fatal("unsupported build admitted WAN candidates")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !got.AdvertiseIPv6 || got.STUNEndpoints[0] != config.STUNEndpoints[1] {
		t.Fatal("WAN config not canonical")
	}
	config.STUNEndpoints[1] = netip.AddrPort{}
	if !got.STUNEndpoints[0].IsValid() {
		t.Fatal("WAN config retained caller-owned storage")
	}
}

func TestWANCandidatesRejectRestrictedBeforeAnySetup(t *testing.T) {
	for _, prefixes := range [][]netip.Prefix{nil, {}, {netip.MustParsePrefix("192.168.50.0/24")}} {
		for _, privateOnly := range []bool{false, true} {
			if !privateOnly && prefixes == nil {
				continue
			}
			config := &WANConfig{AdvertiseIPv6: true}
			if _, err := ValidateWANConfig(config, privateOnly, prefixes); !errors.Is(err, ErrWANRestricted) {
				t.Fatal(err)
			}
			s := &Server{Regions: []*tailcfg.DERPRegion{testRegion("127.0.0.1", 54446)}, WANCandidates: config, PrivateOnly: privateOnly, DestinationPrefixes: prefixes}
			if err := s.Start(); !errors.Is(err, ErrWANRestricted) || s.lb != nil {
				t.Fatal("server did not reject before setup", err)
			}
			key := NewPrivateKey()
			key.Public.Region = s.Regions
			c := &Client{Server: key.Public.Addr(), WANCandidates: config, PrivateOnly: privateOnly, DestinationPrefixes: prefixes}
			if err := c.initLocked(); !errors.Is(err, ErrWANRestricted) || c.lb != nil {
				t.Fatal("client did not reject before setup", err)
			}
		}
	}
}

func TestWANCandidatesInvalidExactEndpoints(t *testing.T) {
	if !buildfeatures.HasUDPTransport || !buildfeatures.HasNATTraversal {
		t.Skip("build rejects all WAN configs")
	}
	invalid := []netip.AddrPort{{}, netip.MustParseAddrPort("192.0.2.1:0"), netip.MustParseAddrPort("0.0.0.0:3478"), netip.MustParseAddrPort("255.255.255.255:3478"), netip.MustParseAddrPort("224.0.0.1:3478"), netip.MustParseAddrPort("169.254.1.1:3478"), netip.MustParseAddrPort("[::]:3478"), netip.MustParseAddrPort("[ff02::1]:3478"), netip.MustParseAddrPort("[fe80::1]:3478"), netip.MustParseAddrPort("[2001:db8::1%fixture]:3478"), netip.MustParseAddrPort("[::ffff:192.0.2.1]:3478")}
	for _, ep := range invalid {
		if _, err := ValidateWANConfig(&WANConfig{STUNEndpoints: []netip.AddrPort{ep}}, false, nil); !errors.Is(err, ErrWANCandidates) {
			t.Fatal("invalid endpoint admitted", err)
		}
	}
	one := netip.MustParseAddrPort("192.0.2.1:3478")
	for _, eps := range [][]netip.AddrPort{nil, {one, one}, make([]netip.AddrPort, STUNRegionNamespaceSize+1)} {
		if _, err := ValidateWANConfig(&WANConfig{STUNEndpoints: eps}, false, nil); !errors.Is(err, ErrWANCandidates) {
			t.Fatal("invalid set admitted", err)
		}
	}
	if got, err := ValidateWANConfig(&WANConfig{AdvertiseIPv6: true}, false, nil); err != nil || len(got.STUNEndpoints) != 0 {
		t.Fatal("IPv6-only opt-in created implicit STUN")
	}
}

func TestWANCandidatesNeverExtendCapabilityOrRelayAuthority(t *testing.T) {
	key := NewPrivateKey()
	key.Public.Region = []*tailcfg.DERPRegion{testRegion("192.0.2.1", 4443)}
	before := key.Public.Addr()
	regions, err := validateRegions(key.Public.Region, false)
	if err != nil {
		t.Fatal(err)
	}
	lb := newLocoBackend(key.Private, key.Public.PresharedKey)
	lb.dm = regionMap(regions)
	lb.wanCandidates = &WANConfig{STUNEndpoints: []netip.AddrPort{netip.MustParseAddrPort("198.51.100.2:3478")}, AdvertiseIPv6: true}
	if lb.tailcatAddr() != before {
		t.Fatal("WAN settings changed legacy capability")
	}
	if !reflect.DeepEqual(lb.dm.Regions[1], regions[0]) || regions[0].Nodes[0].STUNPort != -1 {
		t.Fatal("WAN settings modified pinned relay authority")
	}
}

func TestWANCandidatesMetadataAndProbeBudgetAreIndependent(t *testing.T) {
	if !buildfeatures.HasUDPTransport || !buildfeatures.HasNATTraversal {
		t.Skip("WAN requires UDP build")
	}
	var endpoints []netip.AddrPort
	for port := uint16(30000); port < 30012; port++ {
		endpoints = append(endpoints, netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), port))
	}
	got, err := ValidateWANConfig(&WANConfig{STUNEndpoints: endpoints, ProbeBudget: 2}, false, nil)
	if err != nil || len(got.STUNEndpoints) != len(endpoints) || got.ProbeBudget != 2 {
		t.Fatal("metadata retained arbitrary cap or budget lost", err)
	}
	for _, budget := range []int{-1, STUNRegionNamespaceSize + 1} {
		if _, err := ValidateWANConfig(&WANConfig{AdvertiseIPv6: true, ProbeBudget: budget}, false, nil); !errors.Is(err, ErrWANCandidates) {
			t.Fatal("unbounded/invalid resource budget accepted", err)
		}
	}
}
