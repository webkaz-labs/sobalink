//go:build lanlink_integration

// Copyright (c) 2026 sobalink contributors
// SPDX-License-Identifier: MIT

package routecat

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/net/stun"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// This is an opt-in native loopback fixture, not a WAN/NAT traversal or
// real-device acceptance test. It needs no router, administrator, aliases or
// packet-capture permissions. Only explicit local STUN and pinned TLS relay
// destinations are configured; no peer is added to trigger public direct probes.
func TestWANCandidateNativeLoopbackSTUN(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_WAN_INTEGRATION") != "1" {
		t.Skip("requires explicit native loopback WAN discovery fixture opt-in")
	}
	if err := validateRuntime(false); err != nil {
		t.Fatal("unsafe native fixture configuration")
	}
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal("loopback STUN fixture listener failed")
	}
	defer pc.Close()
	var requests atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, src, err := pc.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			id, err := stun.ParseBindingRequest(buf[:n])
			if err != nil {
				continue
			}
			requests.Add(1)
			_, _ = pc.WriteToUDPAddrPort(stun.Response(id, src), src)
		}
	}()
	defer func() { pc.Close(); <-done }()
	relay, closeRelay := newIntegrationRelay(t)
	defer closeRelay()
	endpoint := pc.LocalAddr().(*net.UDPAddr).AddrPort()
	s := &Server{Region: relay, WANCandidates: &WANConfig{STUNEndpoints: []netip.AddrPort{endpoint}}, Logf: logger.Discard}
	if err := s.Start(); err != nil {
		t.Fatal("WAN fixture engine startup failed", err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		report := s.lb.sys.MagicSock.Get().GetLastNetcheckReport(ctx)
		if report != nil && report.UDP && report.GlobalV4.IsValid() {
			if requests.Load() == 0 || !report.GlobalV4.Addr().IsLoopback() {
				t.Fatal("configured STUN response not observed")
			}
			if report.PreferredDERP != 0 || len(report.RegionLatency)+len(report.RegionV4Latency)+len(report.RegionV6Latency) != 0 {
				t.Fatal("STUN report affected relay selection")
			}
			if report.GlobalV4.Port() != s.lb.sys.MagicSock.Get().LocalPort() {
				t.Fatal("STUN did not probe active transport socket")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("configured loopback STUN discovery did not complete")
		case <-time.After(20 * time.Millisecond):
		}
	}
	capability, err := ParseAddr(s.TailcatAddr())
	if err != nil || len(capability.Region) != 1 {
		t.Fatal("relay capability changed")
	}
	node := capability.Region[0].Nodes[0]
	if node.STUNPort != -1 || node.STUNOnly || node.CertName != relay.Nodes[0].CertName || node.DERPPort != relay.Nodes[0].DERPPort {
		t.Fatal("discovery map escaped into relay capability")
	}
	if got := s.RelayCandidates(); len(got) != 1 || got[0].Nodes[0].STUNPort != -1 {
		t.Fatal("discovery service became a relay candidate")
	}
	if _, err := validateRegions([]*tailcfg.DERPRegion{capability.Region[0]}, false); err != nil {
		t.Fatal("pinned relay validation changed")
	}
}
