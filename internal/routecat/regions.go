// Copyright (c) 2026 sobalink contributors
// SPDX-License-Identifier: MIT

package routecat

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// MaxRegions bounds background presence connections and candidate metadata.
const MaxRegions = 4

var ErrExplicitRegions = errors.New("one to four explicit numeric certificate-pinned relay candidates required")
var ErrPrivateOnlyBuild = errors.New("private-only transport requires a build without UDP underlay transport")

// validateRegions creates an immutable canonical map. IDs are local transport
// handles, not durable candidate identities. Endpoint plus pin is the identity.
// An empty map, DNS, optional address-family fallback and TLS weakening are
// rejected before the first socket is created.
func validateRegions(regions []*tailcfg.DERPRegion, privateOnly bool) ([]*tailcfg.DERPRegion, error) {
	if len(regions) == 0 || len(regions) > MaxRegions {
		return nil, ErrExplicitRegions
	}
	type candidate struct {
		address netip.AddrPort
		pin     string
	}
	candidates := make([]candidate, 0, len(regions))
	seen := make(map[netip.AddrPort]bool)
	for _, region := range regions {
		if region == nil || len(region.Nodes) != 1 || region.Nodes[0] == nil {
			return nil, ErrExplicitRegions
		}
		n := region.Nodes[0]
		ip, err := netip.ParseAddr(n.HostName)
		if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Is4In6() || ip.Zone() != "" || n.HostName != ip.String() || n.DERPPort <= 0 || n.DERPPort > 65535 {
			return nil, ErrExplicitRegions
		}
		if privateOnly && !ip.IsPrivate() && !ip.IsLoopback() {
			return nil, errors.New("private-only relay must be private or loopback")
		}
		if n.STUNPort != -1 || n.STUNOnly || n.InsecureForTests || n.STUNTestIP != "" || n.CanPort80 {
			return nil, ErrExplicitRegions
		}
		if ip.Is4() && (n.IPv4 != ip.String() || n.IPv6 != "none") || ip.Is6() && (n.IPv6 != ip.String() || n.IPv4 != "none") {
			return nil, ErrExplicitRegions
		}
		pin, ok := strings.CutPrefix(n.CertName, "sha256-raw:")
		raw, err := hex.DecodeString(pin)
		if !ok || err != nil || len(raw) != 32 || pin != strings.ToLower(pin) {
			return nil, ErrExplicitRegions
		}
		address := netip.AddrPortFrom(ip, uint16(n.DERPPort))
		if seen[address] {
			return nil, errors.New("duplicate relay endpoint")
		}
		seen[address] = true
		candidates = append(candidates, candidate{address, pin})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		localA := a.address.Addr().IsPrivate() || a.address.Addr().IsLoopback()
		localB := b.address.Addr().IsPrivate() || b.address.Addr().IsLoopback()
		if localA != localB {
			if localA {
				return -1
			}
			return 1
		}
		return a.address.Compare(b.address)
	})
	out := make([]*tailcfg.DERPRegion, 0, len(candidates))
	for i, c := range candidates {
		id := tailcfg.DERPRegionID(i + 1)
		n := &tailcfg.DERPNode{Name: c.address.String(), RegionID: id, HostName: c.address.Addr().String(), CertName: "sha256-raw:" + c.pin, IPv4: "none", IPv6: "none", DERPPort: int(c.address.Port()), STUNPort: -1}
		if c.address.Addr().Is4() {
			n.IPv4 = c.address.Addr().String()
		} else {
			n.IPv6 = c.address.Addr().String()
		}
		out = append(out, &tailcfg.DERPRegion{RegionID: id, RegionCode: fmt.Sprint(id), NoMeasureNoHome: privateOnly, Nodes: []*tailcfg.DERPNode{n}})
	}
	return out, nil
}

func regionMap(regions []*tailcfg.DERPRegion) *tailcfg.DERPMap {
	m := &tailcfg.DERPMap{OmitDefaultRegions: true, Regions: make(map[tailcfg.DERPRegionID]*tailcfg.DERPRegion, len(regions))}
	for _, r := range regions {
		m.Regions[r.RegionID] = r
	}
	return m
}

func validateRuntime(privateOnly bool) error {
	if buildfeatures.HasPortMapper || buildfeatures.HasCaptivePortal || buildfeatures.HasUseProxy {
		return errors.New("routecat requires ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy")
	}
	if privateOnly && buildfeatures.HasUDPTransport {
		return ErrPrivateOnlyBuild
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" {
			continue
		}
		name = strings.ToUpper(name)
		if name == "HTTP_PROXY" || name == "HTTPS_PROXY" || name == "ALL_PROXY" || name == "TS_PROXY" || strings.HasPrefix(name, "TS_") && name != "TS_NO_LOGS_NO_SUPPORT" {
			return errors.New("proxy and Tailscale environment overrides are unsupported")
		}
	}
	return nil
}

// startPresence keeps every configured region reachable, even when peers choose
// different candidates. A map alone only keeps the home DERP alive. Tailscale
// closes non-home connections after 60 seconds without a write request.
// The zero destination deliberately has no reverse route: using our own public
// key could reuse another region's learned reverse route instead of opening the
// requested connection. This bounded five-byte marker contains no capability.
// The relay drops it; enqueue success is not proof of relay reachability.
func (b *locoBackend) startPresence() {
	ctx, cancel := context.WithCancel(context.Background())
	b.presenceCancel = cancel
	b.presenceDone = make(chan struct{})
	ids := b.dm.RegionIDs()
	go func() {
		defer close(b.presenceDone)
		runPresence(ctx, 20*time.Second, ids, func(id tailcfg.DERPRegionID) {
			_, _ = b.sys.MagicSock.Get().SendDERPPacketTo(key.NodePublic{}, id, EncodeMeowed())
		})
	}()
}

func runPresence(ctx context.Context, interval time.Duration, ids []tailcfg.DERPRegionID, send func(tailcfg.DERPRegionID)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			send(id)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RelayCandidates returns copies of configured candidates, not reachability or
// current packet-path claims. Actual connected peer paths are in Status.
func (s *Server) RelayCandidates() []*tailcfg.DERPRegion {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.lb == nil {
		return nil
	}
	var regions []*tailcfg.DERPRegion
	for _, id := range s.lb.dm.RegionIDs() {
		regions = append(regions, s.lb.dm.Regions[id].Clone())
	}
	return regions
}
