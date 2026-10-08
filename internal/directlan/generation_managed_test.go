package directlan

import (
	"context"
	"errors"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These are inert bookkeeping objects, not a transport simulator. No Node
// constructor, supervisor, Prepare method, native owner, TLS, socket, tunnel,
// WG engine, application byte or endpoint movement is run by these selectors.
func inertManagedPublication() (*Node, *PreparedTransport, *runtimeGeneration, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{started: true, recovery: true, cfg: Config{ReplacementOwner: NewManagedTransportOwner()}}
	old := &runtimeGeneration{n: n, done: make(chan struct{})}
	old.retirement = &TransportRetirement{g: old}
	close(old.done)
	n.generation.Store(old)
	g := &runtimeGeneration{n: n, cfg: Config{AuthorityCurrent: func() bool { return true }}, peers: make(map[string]*peerState), published: make(chan struct{}), stop: make(chan struct{}), changed: make(chan struct{}), underlay: &generationUnderlay{}}
	build := newGenerationBuild(context.Background())
	build.generation.Store(g)
	close(build.done)
	p := &PreparedTransport{node: n, retired: old.retirement, build: build, done: make(chan struct{}), transaction: ctx, transferred: make(chan struct{}), managed: true}
	p.self = p
	n.staged.Store(p)
	return n, p, g, cancel
}

func TestManagedGenerationPublicationOneUseTransfer(t *testing.T) {
	n, p, g, cancel := inertManagedPublication()
	defer cancel()
	build := p.build
	calls := 0
	if err := n.PublishManagedTransport(n.cfg.ReplacementOwner, p, func() bool { calls++; return true }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || n.generation.Load() != g || n.staged.Load() != nil || !g.traffic.Load() || n.recovery || !p.published {
		t.Fatal("incomplete transfer")
	}
	cancel()
	p.Abort()
	if build.ctx.Err() != nil || g.sealed {
		t.Fatal("stale cancellation stopped successor")
	}
	if err := n.PublishManagedTransport(n.cfg.ReplacementOwner, p, func() bool { calls++; return true }); err == nil || calls != 1 {
		t.Fatal("replayed publication")
	}
	p.finishTransfer()
	if err := p.WaitClosed(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("transfer misreported physical close", err)
	}
}

func TestManagedGenerationPublicationRejectsStale(t *testing.T) {
	for _, guard := range []string{"unjoined", "failed-close", "foreign-candidate", "stop", "cancel", "authority", "final-check", "build-failure", "foreign-owner"} {
		t.Run(guard, func(t *testing.T) {
			n, p, g, cancel := inertManagedPublication()
			defer cancel()
			old := n.generation.Load()
			switch guard {
			case "unjoined":
				old.done = make(chan struct{})
			case "failed-close":
				old.result = ErrRecovery
			case "foreign-candidate":
				n.staged.Store(nil)
			case "stop":
				n.closing.Store(true)
			case "cancel":
				cancel()
			case "authority":
				g.cfg.AuthorityCurrent = func() bool { return false }
			case "build-failure":
				p.build.err = ErrRecovery
			}
			committed := false
			owner := n.cfg.ReplacementOwner
			if guard == "foreign-owner" {
				owner = NewManagedTransportOwner()
			}
			err := n.PublishManagedTransport(owner, p, func() bool {
				if guard == "final-check" {
					return false
				}
				committed = true
				return true
			})
			if err == nil || committed || n.generation.Load() != old || g.traffic.Load() || p.published {
				t.Fatal("rejected admission changed owner", err)
			}
		})
	}
}

func TestManagedGenerationCandidateRetainedMembership(t *testing.T) {
	base, model, _, now := currentProjectionFixture(t)
	cfg, err := ProjectCurrentEndpointConfig(base, model, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.withDefaults()
	cfg.AuthorityCurrent = func() bool { return true }
	cfg.CompletionAdmission = func(context.Context, *ManagedCompletionRequest) (ContextResponse, error) {
		return ContextResponse{}, ErrUnavailable
	}
	// No retired runtime membership exists. Durable confirmed membership alone
	// permits an identity that was absent from runtime; authentication stays fresh.
	n := &Node{cfg: cfg}
	old := &runtimeGeneration{n: n, cfg: cfg, peers: make(map[string]*peerState)}
	retired := &TransportRetirement{g: old}
	old.retirement = retired
	if _, err = n.managedCandidateConfig(retired, cfg, model); err != nil {
		t.Fatal("retained member rejected", err)
	}
	missing := currentProjectionCopy(model)
	missing.Peers = nil
	if _, err = n.managedCandidateConfig(retired, cfg, missing); err == nil {
		t.Fatal("new member accepted")
	}
	tampered := cloneGenerationConfig(cfg)
	tampered.Peers[0].Name = "changed"
	if _, err = n.managedCandidateConfig(retired, tampered, model); err == nil {
		t.Fatal("altered identity data accepted")
	}
	terminal, _, err := endpointmeta.RevokeManagedPairV4(model, model.Peers[0], now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.managedCandidateConfig(retired, cfg, terminal); err == nil {
		t.Fatal("terminal member accepted")
	}
}
