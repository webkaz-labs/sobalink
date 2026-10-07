//go:build directlan_context_tcp

package directlan

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// This separate opt-in gate opens only fresh IPv4 loopback TCP endpoints. It
// exercises ordinary Start and Exchange, never a synthetic generation or the
// Core persistence owner. No application, WG, endpoint-change or restart path
// is used. Test names and this tag must be selected explicitly by the runner.
const fixedContextTCPWait = 3 * time.Second

type fixedContextTCPPair struct {
	nodes    [2]*Node
	configs  [2]ContextControlConfig
	release  [2]func() error
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopped  chan struct{}
	stopErr  error
}

// The shared TCP+UDP allocator would add UDP socket scope. Keep this gate TCP
// only. Both reservations remain held until both complete configurations and
// both ordinary Nodes exist. Start cannot adopt a listener, so release-to-bind
// is not atomic: checked Start and actual Addr must succeed, without retries
// or endpoint reselection, before any handshake is attempted.
func fixedContextTCPReserve(t *testing.T) (netip.AddrPort, func() error) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve fixed loopback TCP endpoint: %v", err)
	}
	var once sync.Once
	var closeErr error
	release := func() error {
		once.Do(func() { closeErr = listener.Close() })
		return closeErr
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release loopback TCP reservation: %v", err)
		}
	})
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("reservation did not return a TCP address")
	}
	endpoint := address.AddrPort()
	if endpoint.Addr() != netip.MustParseAddr("127.0.0.1") || endpoint.Port() < 1024 {
		t.Fatal("reservation escaped the exact unprivileged IPv4 loopback scope")
	}
	return endpoint, release
}

func newFixedContextTCPPair(t *testing.T, wrongServerPin bool, completion ContextCompletion) *fixedContextTCPPair {
	t.Helper()
	f := &fixedContextTCPPair{stopped: make(chan struct{})}
	f.ctx, f.cancel = context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(func() { f.close(t) })
	var endpoints [2]netip.AddrPort
	for i := range endpoints {
		endpoints[i], f.release[i] = fixedContextTCPReserve(t)
	}
	if endpoints[0] == endpoints[1] {
		t.Fatal("simultaneously held TCP reservations were not distinct")
	}
	identities := [2]Identity{{Seed: strings.Repeat("41", 32)}, {Seed: strings.Repeat("42", 32)}}
	pins := [2]Identity{identities[1], identities[0]}
	if wrongServerPin {
		// A third synthetic identity is only incorrect pin data, never a third
		// Node or endpoint. The selected server endpoint remains unchanged.
		pins[0] = Identity{Seed: strings.Repeat("43", 32)}
	}
	for i := range f.configs {
		f.configs[i] = ContextControlConfig{Identity: identities[i], Listen: endpoints[i],
			AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ControlLimit: 1,
			Peers:      []Peer{{Key: pins[i].PublicKey(), TunnelKey: pins[i].TunnelKey(), Endpoint: endpoints[1-i]}},
			Completion: completion}
	}
	for i, cfg := range f.configs {
		var err error
		f.nodes[i], err = NewContextControl(cfg)
		if err != nil {
			t.Fatalf("construct TCP-only owner %d: %v", i, err)
		}
	}
	return f
}

func (f *fixedContextTCPPair) start(t *testing.T) {
	t.Helper()
	for i, n := range f.nodes {
		if err := f.release[i](); err != nil {
			t.Fatalf("release reservation %d before Start: %v", i, err)
		}
		if err := n.Start(f.ctx); err != nil {
			t.Fatalf("Start fixed endpoint %d (no reselection): %v", i, err)
		}
	}
	for i, n := range f.nodes {
		g := n.generation.Load()
		if g == nil || g.underlay == nil || g.underlay.listener == nil {
			t.Fatalf("owner %d did not publish its TCP listener", i)
		}
		actual, ok := g.underlay.listener.Addr().(*net.TCPAddr)
		if !ok || actual.AddrPort() != f.configs[i].Listen || n.Endpoint() != f.configs[i].Listen {
			t.Fatalf("owner %d did not bind its fixed endpoint", i)
		}
		if err := n.ControlReady(); err != nil {
			t.Fatalf("owner %d control readiness: %v", i, err)
		}
		if !errors.Is(n.Ready(), ErrUnavailable) || n.bind != nil || n.tunnel != nil || n.engine != nil ||
			g.bind != nil || g.tunnel != nil || g.engine.Load() != nil || g.traffic.Load() ||
			len(n.listeners) != 0 || len(g.peers) != 0 || len(g.peerRegistrations) != 0 {
			t.Fatalf("owner %d exposed application or WG runtime", i)
		}
	}
	t.Logf("owned fixed TCP listener endpoints: %s, %s", f.configs[0].Listen, f.configs[1].Listen)
}

// Close both owners before waiting, retain real close errors, and read shared
// observations only after actual Node.Close joins. A timeout is test failure,
// never cleanup evidence; cleanup retries wait on the same close operations.
func (f *fixedContextTCPPair) close(t *testing.T) bool {
	t.Helper()
	f.stopOnce.Do(func() {
		go func() {
			defer f.cancel()
			results := make(chan error, len(f.nodes))
			for _, n := range f.nodes {
				go func(n *Node) {
					if n == nil {
						results <- nil
						return
					}
					err := n.Close()
					// The Start-context callback may have begun Close first.
					// Its repeat-close path joins the generation; also join
					// the last handler epilogue after acceptance has stopped.
					n.wg.Wait()
					results <- err
				}(n)
			}
			for range f.nodes {
				f.stopErr = errors.Join(f.stopErr, <-results)
			}
			close(f.stopped)
		}()
	})
	timer := time.NewTimer(fixedContextTCPWait)
	defer timer.Stop()
	select {
	case <-f.stopped:
		if f.stopErr != nil {
			t.Errorf("fixed TCP owner cleanup failed: %v", f.stopErr)
			return false
		}
	case <-timer.C:
		t.Error("fixed TCP owner cleanup remains unjoined")
		return false
	}
	for _, n := range f.nodes {
		if n == nil {
			continue
		}
		g := n.generation.Load()
		if len(n.wires) != 0 || len(n.dials) != 0 || len(n.contextAttempts) != 0 ||
			(g != nil && (len(g.work) != 0 || g.controlCount != 0 || len(g.failedControl) != 0 ||
				!channelClosed(g.done) || g.underlay == nil || !channelClosed(g.underlay.closed) || !channelClosed(g.underlay.accepted))) {
			t.Error("joined fixed TCP owners retained control work or sockets")
			return false
		}
	}
	return true
}

func fixedContextTCPPrepare(cfg ContextControlConfig, nonce byte) endpointmeta.PrepareRequest {
	peer := cfg.Peers[0]
	return endpointmeta.PrepareRequest{Version: 2, Operation: "pair-context-prepare",
		Sender: cfg.Identity.PublicKey(), Recipient: peer.Key,
		SenderTunnelKey: cfg.Identity.TunnelKey(), RecipientTunnelKey: peer.TunnelKey,
		SenderNonce:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{nonce}, 32)),
		SenderEndpoint: cfg.Listen.String(), RecipientEndpoint: peer.Endpoint.String(),
		SenderScope: endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.1/32"}}}
}

func fixedContextTCPReply(t *testing.T, a, b endpointmeta.PrepareRequest) endpointmeta.PrepareReply {
	t.Helper()
	if a.Sender > b.Sender {
		a, b = b, a
	}
	pair := endpointmeta.PairContext{Version: 1, HostKey: a.Sender, JoinerKey: b.Sender,
		HostTunnelKey: a.SenderTunnelKey, JoinerTunnelKey: b.SenderTunnelKey,
		HostNonce: a.SenderNonce, JoinerNonce: b.SenderNonce,
		HostEndpoint: a.SenderEndpoint, JoinerEndpoint: b.SenderEndpoint,
		HostScope: a.SenderScope, JoinerScope: b.SenderScope}
	binding, err := pair.Binding()
	if err != nil {
		t.Fatal(err)
	}
	return endpointmeta.PrepareReply{Version: 2, Operation: "pair-context-prepare", OK: true, PairContext: pair, PairBinding: binding}
}

func fixedContextTCPClaim(a *ContextAttempt, v VerifiedContextExchange, direction ContextDirection, request endpointmeta.PrepareRequest, reply *endpointmeta.PrepareReply) error {
	copyProof := v
	transcript, err := v.TryClaimFor(a)
	if err != nil {
		return fmt.Errorf("claim production TLS evidence: %w", err)
	}
	if _, err := copyProof.TryClaimFor(a); !errors.Is(err, ErrUntrusted) {
		return errors.New("copied TLS evidence was not one-use")
	}
	wantRequest, err := endpointmeta.Encode(request)
	if err != nil {
		return err
	}
	gotRequest, err := endpointmeta.Encode(transcript.Request())
	if err != nil || !bytes.Equal(gotRequest, wantRequest) || transcript.Operation() != ContextPrepare || transcript.Direction() != direction {
		return errors.New("TLS claim changed request, operation or direction")
	}
	if reply == nil {
		if transcript.Reply() != nil {
			return errors.New("inbound claim unexpectedly contained a reply")
		}
		return nil
	}
	wantReply, err := endpointmeta.Encode(*reply)
	if err != nil {
		return err
	}
	gotReply, err := endpointmeta.Encode(transcript.Reply())
	if err != nil || !bytes.Equal(gotReply, wantReply) {
		return errors.New("outbound claim changed the authenticated reply")
	}
	return nil
}

func TestContextControlFixedTCPPrepareExchange(t *testing.T) {
	var inboundCalls, outboundCalls, admissions atomic.Int32
	var armed atomic.Pointer[ContextAttempt]
	var request endpointmeta.PrepareRequest
	var reply endpointmeta.PrepareReply
	responseEpoch := NewContextEpoch()
	defer responseEpoch.Invalidate()
	f := newFixedContextTCPPair(t, false, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
		inboundCalls.Add(1)
		if a != armed.Load() {
			return ContextResponse{}, errors.New("callback received a different armed attempt")
		}
		if err := fixedContextTCPClaim(a, v, ContextInbound, request, nil); err != nil {
			return ContextResponse{}, err
		}
		return ContextResponse{Reply: reply, Epoch: responseEpoch, Admit: func() bool {
			return admissions.Add(1) == 1 && responseEpoch.Valid() && !a.Cancelled()
		}}, nil
	})
	request = fixedContextTCPPrepare(f.configs[0], 1)
	incoming := fixedContextTCPPrepare(f.configs[1], 2)
	reply = fixedContextTCPReply(t, request, incoming)
	f.start(t)
	inboundEpoch, outboundEpoch := NewContextEpoch(), NewContextEpoch()
	defer inboundEpoch.Invalidate()
	defer outboundEpoch.Invalidate()
	inbound, err := f.nodes[1].ArmContextAttempt(f.configs[1].Peers[0].Key, incoming, inboundEpoch)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(inbound)
	outbound, err := f.nodes[0].PrepareContextAttempt(f.configs[0].Peers[0].Key, request, outboundEpoch)
	if err != nil {
		t.Fatal(err)
	}
	completion := func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
		outboundCalls.Add(1)
		if a != outbound {
			return ContextResponse{}, errors.New("callback received a different outbound attempt")
		}
		return ContextResponse{}, fixedContextTCPClaim(a, v, ContextOutbound, request, &reply)
	}
	if err := outbound.Exchange(f.ctx, completion); err != nil {
		t.Fatalf("fixed endpoint production Exchange: %v", err)
	}
	if err := outbound.Exchange(f.ctx, completion); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("consumed attempt accepted a second Exchange: %v", err)
	}
	if !f.close(t) {
		return
	}
	if inboundCalls.Load() != 1 || outboundCalls.Load() != 1 || admissions.Load() != 1 || !inbound.Cancelled() || !outbound.Cancelled() {
		t.Fatal("exchange did not consume exactly one callback, claim and response admission per direction")
	}
}

func TestContextControlFixedTCPWrongServerPin(t *testing.T) {
	var callbacks atomic.Int32
	completion := func(context.Context, *ContextAttempt, VerifiedContextExchange) (ContextResponse, error) {
		callbacks.Add(1)
		return ContextResponse{}, errors.New("wrong pin reached completion")
	}
	f := newFixedContextTCPPair(t, true, completion)
	f.start(t)
	request := endpointmeta.BoundRequest{Version: 2, Operation: "pair-context-status", PairBinding: strings.Repeat("ab", 32)}
	inboundEpoch, outboundEpoch := NewContextEpoch(), NewContextEpoch()
	defer inboundEpoch.Invalidate()
	defer outboundEpoch.Invalidate()
	if _, err := f.nodes[1].ArmContextAttempt(f.configs[1].Peers[0].Key, request, inboundEpoch); err != nil {
		t.Fatal(err)
	}
	outbound, err := f.nodes[0].PrepareContextAttempt(f.configs[0].Peers[0].Key, request, outboundEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbound.Exchange(f.ctx, completion); !errors.Is(err, ErrIdentity) {
		t.Fatalf("wrong server pin did not fail identity verification: %v", err)
	}
	if !f.close(t) {
		return
	}
	if callbacks.Load() != 0 || !outbound.Cancelled() {
		t.Fatal("wrong pin produced callback evidence or retained its outbound attempt")
	}
}
