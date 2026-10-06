package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/identity"
)

// RunNetworkWorker is the internal owner-pipe entrypoint. It constructs one
// engine and no Core, Web UI, transfer manager, profile writer or OS service.
// The parent owns private state locking and must never run a second worker for
// the same backend state. No credentials are supplied through command arguments.
func RunNetworkWorker(ctx context.Context, dir, mode, hostname string, in io.ReadCloser, out io.WriteCloser, selected ...backendworker.Limits) error {
	node, limits, err := prepareNetworkWorker(ctx, dir, mode, hostname, selected...)
	if err != nil {
		return err
	}
	return backendworker.ServeEngine(ctx, in, out, workerEngine{node}, limits)
}

// Prepare from one validated saved policy before starting any engine or owner RPC.
// Explicit IPC limits override only IPC budgets, never the LAN runtime or store.
func prepareNetworkWorker(ctx context.Context, dir, mode, hostname string, selected ...backendworker.Limits) (NetworkBackend, backendworker.Limits, error) {
	limits := backendworker.DefaultLimits()
	if !filepath.IsAbs(dir) || !config.ValidName(hostname) {
		return nil, limits, errors.New("invalid private worker configuration")
	}
	policy, err := readCapacityPolicy(dir)
	if err != nil {
		return nil, limits, err
	}
	if len(selected) > 0 {
		limits = selected[0]
	} else {
		limits, err = selectedWorkerLimits(policy)
		if err != nil {
			return nil, limits, err
		}
	}
	if e := limits.Validate(); e != nil {
		return nil, limits, e
	}
	var node NetworkBackend
	switch mode {
	case "tailnet":
		node, err = identity.New(dir, hostname)
	case "lan":
		var store *lanStore
		store, err = readLANStoreWithPolicy(filepath.Join(dir, "lan.json"), policy)
		if err == nil {
			if store == nil {
				return nil, limits, errors.New("LAN worker requires existing reviewed configuration")
			}
			owner := &Core{ctx: ctx, dir: dir, capacity: policy}
			node, err = owner.newLANBackend(store)
		}
	default:
		return nil, limits, errors.New("unsupported isolated network engine")
	}
	if err != nil {
		return nil, limits, err
	}
	return node, limits, nil
}

type workerEngine struct{ NetworkBackend }

func (w workerEngine) State(ctx context.Context) (json.RawMessage, error) {
	state, e := w.NetworkBackend.State(ctx)
	if e != nil {
		return nil, e
	}
	if state.SelfID == "" {
		if identified, ok := w.NetworkBackend.(interface{ PublicKey() string }); ok {
			state.SelfID = identified.PublicKey()
		}
	}
	return json.Marshal(state)
}

type processBackend struct {
	remote  *backendworker.RemoteEngine
	command *exec.Cmd
	done    chan error
	once    sync.Once
	ctx     context.Context
	cancel  context.CancelFunc
}

// startProcessBackend launches only this same installed executable, with owner
// pipes and a single engine. External scope must be authorized before calling.
func startProcessBackend(ctx context.Context, dir, mode, hostname string) (*processBackend, error) {
	if mode != "tailnet" && mode != "lan" {
		return nil, errors.New("unsupported isolated network engine")
	}
	executable, e := os.Executable()
	if e != nil {
		return nil, e
	}
	absolute, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	policy, e := readCapacityPolicy(dir)
	if e != nil {
		return nil, e
	}
	limits, e := selectedWorkerLimits(policy)
	if e != nil {
		return nil, e
	}
	encoded, e := json.Marshal(limits)
	if e != nil {
		return nil, e
	}
	childctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(childctx, executable, "__network-worker", mode, absolute, hostname, string(encoded))
	cmd.Stderr = io.Discard
	in, e := cmd.StdinPipe()
	if e != nil {
		cancel()
		return nil, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		_ = in.Close()
		cancel()
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		_ = in.Close()
		_ = out.Close()
		cancel()
		return nil, e
	}
	p := &processBackend{command: cmd, ctx: childctx, cancel: cancel, done: make(chan error, 1)}
	p.remote = backendworker.NewRemoteEngine(childctx, out, in, limits)
	go func() { e := cmd.Wait(); _ = p.remote.Close(); p.done <- e }()
	return p, nil
}
func (p *processBackend) Start() error {
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	_, e := p.remote.State(ctx)
	if e != nil {
		_ = p.Close()
	}
	return e
}
func (p *processBackend) State(ctx context.Context) (identity.State, error) {
	raw, e := p.remote.State(ctx)
	if e != nil {
		return identity.State{}, e
	}
	var state identity.State
	e = json.Unmarshal(raw, &state)
	return state, e
}
func (p *processBackend) Login(ctx context.Context) error  { return p.remote.Login(ctx) }
func (p *processBackend) Logout(ctx context.Context) error { return p.remote.Logout(ctx) }
func (p *processBackend) DialIP(ctx context.Context, n string, ap netip.AddrPort) (net.Conn, error) {
	return p.remote.DialIP(ctx, n, ap)
}
func (p *processBackend) Listen(n, a string) (net.Listener, error) { return p.remote.Listen(n, a) }
func (p *processBackend) ListenPacket(n, a string) (net.PacketConn, error) {
	return p.remote.ListenPacket(n, a)
}
func (p *processBackend) WhoIs(ctx context.Context, ap netip.AddrPort) (string, error) {
	return p.remote.WhoIs(ctx, ap)
}
func (p *processBackend) Close() error {
	p.once.Do(func() {
		_ = p.remote.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.cancel()
			<-p.done
		}
		p.cancel()
	})
	return nil
}

var _ NetworkBackend = (*processBackend)(nil)

func (w workerEngine) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	n, ok := w.NetworkBackend.(interface {
		RegisterTCPFallback(func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error)
	})
	if !ok {
		return nil, errors.New("network does not support scoped TCP dispatch")
	}
	return n.RegisterTCPFallback(f)
}
func (w workerEngine) ValidateTCPScopes(ctx context.Context, scopes []backendworker.TCPPolicy) error {
	if len(scopes) == 0 {
		return nil
	}
	state, e := w.NetworkBackend.State(ctx)
	if e != nil || !state.Snapshot.Running {
		return errors.New("worker identity is unavailable")
	}
	own := map[netip.Addr]bool{}
	peers := map[netip.Addr]bool{}
	for _, ip := range state.IPs {
		own[ip] = true
	}
	for _, p := range state.Snapshot.Peers {
		if p.Expired {
			continue
		}
		for _, ip := range p.IPs {
			peers[ip] = true
		}
	}
	for _, p := range scopes {
		if !own[p.Address] {
			return errors.New("worker scope must use its exact current address")
		}
		for _, ip := range p.Peers {
			if !peers[ip] {
				return errors.New("worker scope requires current authenticated peers")
			}
		}
	}
	return nil
}
func (p *processBackend) RegisterTCPFallback(f func(netip.AddrPort, netip.AddrPort) (func(net.Conn), bool)) (func(), error) {
	return p.remote.RegisterTCPFallback(f)
}
func (p *processBackend) SetTCPScopes(ctx context.Context, scopes []backendworker.TCPPolicy) error {
	return p.remote.SetTCPScopes(ctx, scopes)
}
