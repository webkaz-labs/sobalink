package routecat

import (
	"errors"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"tailscale.com/tailcfg"
	"tailscale.com/wgengine"
	"tailscale.com/wgengine/magicsock"
)

func TestEngineStatusDoesNotRestoreStaleEndpoints(t *testing.T) {
	backend := &locoBackend{privateOnly: true}
	// A zero magicsock supplies the authoritative empty endpoint set without
	// opening sockets. The delayed callback still carries a prior endpoint.
	backend.sys.MagicSock.Set(new(magicsock.Conn))
	endpoint := netip.MustParseAddrPort("[::1]:42000")
	backend.onEngineStatus(&wgengine.Status{LocalAddrs: []tailcfg.Endpoint{{Addr: endpoint}}}, nil)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.eps) != 0 {
		t.Fatal("delayed callback restored an endpoint absent from current magicsock endpoint state")
	}
}

func TestEngineStatusIgnoresMissingOrFailedUpdates(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:42000")
	backend := &locoBackend{privateOnly: true, eps: []netip.AddrPort{endpoint}}
	// No magicsock is installed: rejected notifications must not sample it.
	backend.onEngineStatus(nil, nil)
	backend.onEngineStatus(&wgengine.Status{}, errors.New("synthetic status failure"))
	if !slices.Equal(backend.eps, []netip.AddrPort{endpoint}) {
		t.Fatal("rejected callback changed endpoints")
	}
}

func TestEndpointRefreshUsesCurrentSnapshotAndOwnsStorage(t *testing.T) {
	a := netip.MustParseAddrPort("127.0.0.1:42000")
	b := netip.MustParseAddrPort("[::1]:42000")
	backend := &locoBackend{privateOnly: true}
	current := []netip.AddrPort{b, a, b}
	backend.refreshEndpoints(func() []netip.AddrPort { return current })
	if !slices.Equal(current, []netip.AddrPort{b, a, b}) {
		t.Fatal("refresh modified the source snapshot")
	}
	current[0] = netip.AddrPort{}
	backend.mu.Lock()
	got := slices.Clone(backend.eps)
	backend.mu.Unlock()
	if !slices.Equal(got, []netip.AddrPort{a, b}) {
		t.Fatal("refresh did not own a sorted, duplicate-free snapshot")
	}
	backend.refreshEndpoints(func() []netip.AddrPort { return nil })
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.eps) != 0 {
		t.Fatal("genuine authoritative removal was ignored")
	}
}

func TestEndpointRefreshSerializesSamplingAndAssignment(t *testing.T) {
	backend := &locoBackend{privateOnly: true}
	// No other goroutine can hold this mutex yet. This detects a reader moved
	// outside the assignment lock deterministically, before the stress case.
	sampledUnderLock := false
	backend.refreshEndpoints(func() []netip.AddrPort {
		if backend.mu.TryLock() {
			backend.mu.Unlock()
		} else {
			sampledUnderLock = true
		}
		return nil
	})
	if !sampledUnderLock {
		t.Fatal("endpoint source was sampled outside the assignment lock")
	}
	var sequence atomic.Uint32
	var unlocked atomic.Bool
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			backend.refreshEndpoints(func() []netip.AddrPort {
				if backend.mu.TryLock() {
					unlocked.Store(true)
					backend.mu.Unlock()
				}
				port := uint16(42000 + sequence.Add(1))
				return []netip.AddrPort{netip.AddrPortFrom(netip.IPv6Loopback(), port)}
			})
		})
	}
	workers.Wait()
	if unlocked.Load() {
		t.Fatal("endpoint source was sampled outside the assignment lock")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	want := netip.AddrPortFrom(netip.IPv6Loopback(), 42032)
	if !slices.Equal(backend.eps, []netip.AddrPort{want}) {
		t.Fatal("an earlier sample overwrote the latest sample")
	}
}

func TestEndpointRefreshStopsAfterClose(t *testing.T) {
	backend := &locoBackend{closed: true}
	backend.refreshEndpoints(func() []netip.AddrPort {
		t.Fatal("closed backend sampled endpoints")
		return nil
	})
}
