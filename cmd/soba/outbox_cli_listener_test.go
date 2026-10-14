package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// Discovery requires a healthy peer application, including both listener
// owners. These inert listeners never bind a socket or accept a connection;
// their only event is the production owner's physical Close.
type outboxIPCListener struct {
	mu       sync.Mutex
	address  netip.AddrPort
	closed   chan struct{}
	joined   chan struct{}
	closes   int
	accepts  int
	closeOne sync.Once
	joinOne  sync.Once
}

func (l *outboxIPCListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.accepts++
	l.mu.Unlock()
	<-l.closed
	l.joinOne.Do(func() { close(l.joined) })
	return nil, net.ErrClosed
}
func (l *outboxIPCListener) Close() error {
	l.mu.Lock()
	l.closes++
	l.mu.Unlock()
	l.closeOne.Do(func() { close(l.closed) })
	return nil
}
func (l *outboxIPCListener) Addr() net.Addr { return net.TCPAddrFromAddrPort(l.address) }

type outboxIPCListenerLog struct {
	mu        sync.Mutex
	listeners []*outboxIPCListener
}

func (b *outboxIPCBackend) Listen(network, address string) (net.Listener, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || network != "tcp" || ap.Addr() != netip.MustParseAddr("100.64.0.1") || ap.Port() != core.PeerPort && ap.Port() != core.DiscoveryPort {
		return nil, errors.New("unexpected synthetic peer listener scope")
	}
	b.listeners.mu.Lock()
	defer b.listeners.mu.Unlock()
	for _, existing := range b.listeners.listeners {
		if existing.address == ap {
			return nil, errors.New("synthetic peer listener was replaced")
		}
	}
	l := &outboxIPCListener{address: ap, closed: make(chan struct{}), joined: make(chan struct{})}
	b.listeners.listeners = append(b.listeners.listeners, l)
	return l, nil
}

func (log *outboxIPCListenerLog) requireClosed(t *testing.T) {
	t.Helper()
	log.mu.Lock()
	listeners := append([]*outboxIPCListener(nil), log.listeners...)
	log.mu.Unlock()
	if len(listeners) != 2 {
		t.Errorf("synthetic peer application did not own exactly two listeners: %d", len(listeners))
	}
	for _, l := range listeners {
		l.mu.Lock()
		closes, accepts := l.closes, l.accepts
		l.mu.Unlock()
		if closes != 1 || accepts > 1 {
			t.Errorf("synthetic listener ownership differs: closes=%d accepts=%d", closes, accepts)
		}
		select {
		case <-l.closed:
		default:
			t.Error("Core.Close did not close its synthetic listener")
		}
		if accepts != 0 {
			select {
			case <-l.joined:
			case <-time.After(time.Second):
				t.Error("synthetic listener Accept did not join after close")
			}
		}
	}
}

type outboxIPCFailedListenerBackend struct {
	*outboxIPCBackend
	attempted chan struct{}
	once      sync.Once
}

func (b *outboxIPCFailedListenerBackend) Listen(string, string) (net.Listener, error) {
	b.once.Do(func() { close(b.attempted) })
	return nil, errors.New("synthetic peer listener unavailable")
}

// A negative control preserves the original fixture's failed-listener state
// and proves real maintenance remains fail-closed before any discovery dial.
func TestOutboxCLIFailedListenerBlocksDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	backend := &outboxIPCFailedListenerBackend{outboxIPCBackend: &outboxIPCBackend{}, attempted: make(chan struct{})}
	_, app := dispatchContractCore(t, ctx, func(profile *core.Profile) core.NetworkBackend {
		backend.configure(profile)
		return backend
	})
	select {
	case <-backend.attempted:
	case <-ctx.Done():
		t.Fatal("maintenance did not attempt peer listener startup")
	}
	// This ordinary read takes Core.op after the failing maintenance pass. Do
	// not cancel first: cancellation could itself prevent a wrongly admitted dial.
	if _, err := app.Command(ctx, webui.Command{RequestID: "synthetic-maintenance-barrier", Name: "policy.config", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if counts := backend.dials.snapshot(); counts != (outboxIPCDialCounts{}) {
		t.Fatalf("failed peer readiness admitted discovery before shutdown: %+v", counts)
	}
	// Join maintenance before the final observation, including the failing pass.
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if counts := backend.dials.snapshot(); counts != (outboxIPCDialCounts{}) {
		t.Fatalf("failed peer readiness admitted discovery: %+v", counts)
	}
}
