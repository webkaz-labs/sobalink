package backendworker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestReviewRevokeClosesQueuedAndLateChildStreamsBeforeAck(t *testing.T) {
	h := &engineHost{engine: &scopedTestEngine{}, handles: map[uint64]io.Closer{}, limits: DefaultLimits()}
	defer h.close()
	call := func(method string, q engineRequest) {
		t.Helper()
		raw, _ := json.Marshal(q)
		if _, e := h.handle(context.Background(), method, raw); e != nil {
			t.Fatal(e)
		}
	}
	call("fallback-enable", engineRequest{})
	src := netip.MustParseAddrPort("100.64.0.2:32000")
	dst := netip.MustParseAddrPort("100.64.0.1:4567")
	call("fallback-scopes", engineRequest{Scopes: []TCPPolicy{{Address: dst.Addr(), Ports: [][2]uint16{{4567, 4567}}, Peers: []netip.Addr{src.Addr()}, UntilRevoked: true}}})
	handler, _ := h.fallback.selector(src, dst)
	queued, queuedPeer := net.Pipe()
	defer queuedPeer.Close()
	handler(queued)
	call("fallback-scopes", engineRequest{})
	late, latePeer := net.Pipe()
	defer latePeer.Close()
	handler(late)
	for label, peer := range map[string]net.Conn{"queued": queuedPeer, "late": latePeer} {
		peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		if _, e := peer.Read(make([]byte, 1)); e != io.EOF {
			t.Errorf("%s child stream still open after revoke acknowledged: %v", label, e)
		}
	}
}
func TestReviewReservedControlBudgetDoesNotKillHealthyWorker(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 16)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, serverIn, serverOut, func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	c := NewClient(clientIn, clientOut)
	defer c.Close()
	results := make(chan error, 9)
	for i := 0; i < 8; i++ {
		go func() { results <- c.Call(ctx, "whois", nil, nil) }()
		<-entered
	}
	// A legitimate ninth control call must be refused locally or queued. It must
	// not retire every established stream even though overall Requests is 128.
	go func() { results <- c.Call(ctx, "whois", nil, nil) }()
	select {
	case e := <-done:
		t.Fatalf("valid control burst killed worker: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
}
