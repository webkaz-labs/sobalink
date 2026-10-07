package directlan

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/tailscale/wireguard-go/device"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// generationBuild owns unpublished construction, including failures before a
// G can exist. Node retains it through either its initial-build slot or its one
// detached candidate. It grants no publication or authority to restart a G.
type generationBuild struct {
	ctx        context.Context
	cancel     context.CancelFunc
	generation atomic.Pointer[runtimeGeneration]
	done       chan struct{}
	err        error
	cleanupErr error
	underlay   atomic.Pointer[generationUnderlay]
}

func newGenerationBuild(ctx context.Context) *generationBuild {
	run, cancel := context.WithCancel(ctx)
	return &generationBuild{ctx: run, cancel: cancel, done: make(chan struct{})}
}
func (b *generationBuild) RequestStop() {
	b.cancel()
	b.signalResources()
}
func (b *generationBuild) signalResources() {
	if underlay := b.underlay.Load(); underlay != nil {
		underlay.RequestClose()
	}
	if g := b.generation.Load(); g != nil {
		g.requestStop(net.ErrClosed)
	}
}
func (b *generationBuild) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
	}
	if g := b.generation.Load(); g != nil {
		return g.wait(ctx)
	}
	return b.cleanupErr
}
func cloneGenerationConfig(cfg Config) Config {
	cfg.AllowedPrefixes = append([]netip.Prefix(nil), cfg.AllowedPrefixes...)
	cfg.Peers = append([]Peer(nil), cfg.Peers...)
	return cfg
}

// buildTransportGeneration constructs private state only. Traffic remains staged;
// only initial Start has a publication path. The caller registers b with Node
// before entry and owns cancellation/completion through b.done.
func (n *Node) buildTransportGeneration(b *generationBuild, cfg Config) (*runtimeGeneration, error) {
	network := "tcp6"
	if cfg.Listen.Addr().Is4() {
		network = "tcp4"
	}
	ln, err := (&net.ListenConfig{}).Listen(b.ctx, network, cfg.Listen.String())
	if err != nil {
		return nil, err
	}
	underlay := newGenerationUnderlay(ln)
	b.underlay.Store(underlay)
	if err := b.ctx.Err(); err != nil {
		underlay.RequestClose()
		b.cleanupErr = underlay.WaitClosed(context.Background())
		return nil, errors.Join(err, b.cleanupErr)
	}
	bind := &lanBind{cfg: cloneGenerationConfig(cfg)}
	local := n.OverlayAddr()
	tunnel, err := newUserspaceTunnel(local, func(src, dst netip.Addr) bool {
		policy := bind.policy.Load()
		return bind.owner != nil && bind.owner.trafficOpen() && policy != nil && dst == local && policy.sources[src]
	})
	if err != nil {
		underlay.RequestClose()
		b.cleanupErr = underlay.WaitClosed(context.Background())
		return nil, errors.Join(err, b.cleanupErr)
	}
	g := newRuntimeGeneration(n, bind, tunnel)
	g.cfg = cloneGenerationConfig(cfg)
	g.underlay = underlay
	bind.owner, tunnel.owner = g, g
	for _, peer := range cfg.Peers {
		g.peers[peer.Key] = newPeerStateForGeneration(g, cfg.Identity.PublicKey(), peer)
	}
	g.refreshBindPolicy(g.peers)
	b.generation.Store(g)
	go g.supervise()
	defer close(g.built)
	underlay.start(n, g)
	if err := b.ctx.Err(); err != nil {
		g.requestStop(err)
		return g, err
	}
	if err := g.buildTunnel(); err != nil {
		g.requestStop(err)
		return g, err
	}
	if err := b.ctx.Err(); err != nil {
		g.requestStop(err)
		return g, err
	}
	return g, nil
}

func (g *runtimeGeneration) buildTunnel() error {
	n := g.n
	requests := &tcpRequestLifecycle{g: g}
	tf := tcp.NewForwarderWithLifecycle(g.tunnel.stack, 0, g.cfg.FlowLimit, requests, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		entry := g.incoming.get(id)
		complete := func(reset bool) { requests.complete(r, id, entry, reset) }
		if entry == nil {
			complete(true)
			return
		}
		if !n.dispatch(g, func() { n.acceptTCP(g, r, entry.peer, complete) }) {
			complete(false)
		}
	})
	g.tunnel.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		peer := g.packetPeer(id)
		if peer == nil || !validService("tcp", id.LocalPort) {
			return false
		}
		return requests.handle(tf, id, pkt, peer)
	})
	g.tunnel.stack.SetTransportProtocolHandler(udp.ProtocolNumber, func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
		peer := g.packetPeer(id)
		if peer == nil || !validService("udp", id.LocalPort) {
			return false
		}
		creator, e := g.acquireCreator(context.Background(), false)
		if e != nil {
			return false
		}
		held := pkt.Clone()
		if !n.dispatch(g, func() {
			defer creator.finishOutgoing()
			defer held.DecRef()
			n.acceptUDP(g, creator, udp.NewForwarderRequest(g.tunnel.stack, id, held), peer)
		}) {
			held.DecRef()
			creator.finishOutgoing()
			return false
		}
		return true
	})
	engine, e := device.NewOwnedDevice(g.tunnel, g.bind, device.OwnedConfig{
		PeerRegistered: func(key device.NoisePublicKey, registration device.PeerRegistration) {
			// WG invokes this synchronously before peer publication and outside
			// its owner mutex. Only the exact preselected session is bound.
			target := g.registrationTarget.Load()
			if target != nil && target.key == key && registration != 0 {
				target.session.registration.CompareAndSwap(0, uint64(registration))
			}
		},
		SessionState: func(key device.NoisePublicKey, registration device.PeerRegistration, state device.PeerSessionState) {
			policy := g.bind.policy.Load()
			if policy == nil {
				return
			}
			if session := policy.sessions[[32]byte(key)]; session != nil && registration != 0 && session.registration.Load() == uint64(registration) {
				session.transition(state)
			}
		},
		AfterWake: func() { g.seal(ErrRecovery) },
	})
	if engine != nil {
		g.engine.Store(engine)
	}
	if e != nil {
		g.requestStop(e)
		return e
	}
	if !g.open() {
		engine.RequestStop(ErrRecovery)
		return ErrRecovery
	}
	releaseSetup, ok := engine.BeginSetup()
	if !ok {
		g.requestStop(ErrRecovery)
		return ErrRecovery
	}
	defer releaseSetup()
	priv, e := g.cfg.Identity.tunnelPrivate()
	if e == nil {
		e = engine.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(priv), g.cfg.Listen.Port()))
	}
	if e == nil {
		for _, p := range g.peers {
			if e = g.installPeer(p); e != nil {
				break
			}
		}
	}
	if e == nil {
		e = engine.Up()
	}
	if e != nil {
		g.requestStop(e)
		return e
	}
	return nil
}
