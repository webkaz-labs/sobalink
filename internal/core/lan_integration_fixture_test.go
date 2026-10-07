//go:build lanlink_integration

package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"tailscale.com/feature/buildfeatures"
)

type nativeLANCoreFixture struct {
	t           *testing.T
	ctx         context.Context
	cancel      context.CancelFunc
	cores       []*Core
	resources   []io.Closer
	cleanupOnce sync.Once
}

func newNativeLANCoreFixture(t *testing.T) *nativeLANCoreFixture {
	t.Helper()
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires an explicitly enabled isolated native CI environment")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("isolated Core integration requires ts_omit_udptransport")
	}
	if lanlink.ValidateBuild() != nil {
		t.Fatal("isolated Core integration requires all LAN restriction build tags")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	f := &nativeLANCoreFixture{t: t, ctx: ctx, cancel: cancel}
	t.Cleanup(f.cleanup)
	return f
}

func (f *nativeLANCoreFixture) cleanup() {
	f.cleanupOnce.Do(func() {
		f.cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := len(f.resources) - 1; i >= 0; i-- {
				_ = f.resources[i].Close()
			}
			for i := len(f.cores) - 1; i >= 0; i-- {
				_ = f.cores[i].Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			f.t.Error("native Core teardown did not complete")
		}
	})
}

func (f *nativeLANCoreFixture) open() *Core {
	f.t.Helper()
	dir := f.t.TempDir()
	f.t.Cleanup(f.cleanup) // Close engines before this directory's cleanup on failure.
	c, err := Open(f.ctx, Options{Directory: dir, Version: "native-test", SkipNetworkStart: true})
	if err != nil {
		f.t.Fatal("could not open isolated Core state")
	}
	f.cores = append(f.cores, c)
	return c
}

func (f *nativeLANCoreFixture) invoke(c *Core, name string, payload any) (any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	call, stop := context.WithTimeout(f.ctx, 30*time.Second)
	defer stop()
	return c.Command(call, webui.Command{RequestID: randomID(), Name: name, Payload: encoded})
}

func (f *nativeLANCoreFixture) must(c *Core, name string, payload any) any {
	f.t.Helper()
	value, err := f.invoke(c, name, payload)
	if err != nil {
		code := "unclassified"
		switch candidate := networkErrorCode(err); candidate {
		case "lan_saved_start_changed", "lan_start_failed", "lan_environment_proxy", "lan_environment_override", "lan_relay_mismatch", "network_restart_required", "lan_state_capacity", "lan_recovery_required":
			code = candidate
		}
		// Report only fixed classifications, never payloads, raw errors,
		// identities, addresses, protected file paths or authorization material.
		f.t.Fatalf("native Core command %s failed (code=%s permission=%t canceled=%t timeout=%t)", name, code,
			errors.Is(err, os.ErrPermission), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded))
	}
	return value
}

func (f *nativeLANCoreFixture) relayAddress() netip.AddrPort {
	f.t.Helper()
	for attempt := 0; attempt < 8; attempt++ {
		probe, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			f.t.Fatal("could not select an isolated relay port")
		}
		address := probe.Addr().(*net.TCPAddr).AddrPort()
		_ = probe.Close()
		if address.Port() >= 1024 && (address.Port() < DiscoveryPort || address.Port() > lanlink.PairingPort) {
			return address
		}
	}
	f.t.Fatal("could not select an unreserved relay port")
	return netip.AddrPort{}
}

func (f *nativeLANCoreFixture) waitPeerAPI(from *Core, peer string) {
	f.t.Helper()
	window, stopWindow := context.WithTimeout(f.ctx, 30*time.Second)
	defer stopWindow()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		call, stop := context.WithTimeout(window, 5*time.Second)
		err := from.peerJSON(call, peer, "GET", "/v1/hello", nil, nil)
		stop()
		if err == nil {
			return
		}
		select {
		case <-window.Done():
			f.t.Fatal("normal Core startup did not make the real peer API reachable")
		case <-ticker.C:
		}
	}
}
