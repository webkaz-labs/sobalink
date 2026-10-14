package nearby

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

var ErrDNSServiceUnavailable = errors.New("nearby: selected-interface OS DNS-SD is unavailable; use manual setup")

// Only fixed categories cross this boundary. Native error text, hostnames,
// addresses and interface identifiers are never included in diagnostics.
type dnssdFailure struct{ stage, kind string }

func (e dnssdFailure) Error() string { return "nearby: OS DNS-SD " + e.stage + " " + e.kind }
func (e dnssdFailure) Unwrap() error { return ErrDNSServiceUnavailable }

type dnssdBackend interface {
	Start() error
	Next() (dnssdEvent, error)
	Query(string, uint16) error
	// Close fences callbacks, wakes Next, joins all native calls and queue work,
	// then releases storage. It is once-owned and cannot return before that join.
	Close() error
}
type dnssdBackendFactory func(dnssdRegistration) (dnssdBackend, error)
type dnssdBinding func(Scope, *net.Interface, discoveryPolicy) (net.Interface, error)

type dnssdProvider struct {
	ctx          context.Context
	cancel       context.CancelCauseFunc
	observations chan recordObservation
	done         chan struct{}
	mu           sync.Mutex
	closeErr     error
}

// The owner and its cancellation fence exist before even the backend allocator
// is invoked. Potentially blocking daemon IPC is confined to owned workers.
func newDNSServiceProvider(ctx context.Context, cfg Config, policy discoveryPolicy, open dnssdBackendFactory, binding dnssdBinding) (recordProvider, error) {
	reg, err := dnssdRegistrationFor(cfg, policy)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if open == nil || binding == nil {
		return nil, ErrDNSServiceUnavailable
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	p := &dnssdProvider{ctx: ctx, cancel: cancel, observations: make(chan recordObservation), done: make(chan struct{})}
	go p.run(cfg, policy, reg, open, binding)
	return p, nil
}
func (p *dnssdProvider) ReadObservation() (recordObservation, error) {
	if p.ctx.Err() != nil {
		return recordObservation{}, context.Cause(p.ctx)
	}
	select {
	case <-p.ctx.Done():
		return recordObservation{}, context.Cause(p.ctx)
	case out := <-p.observations:
		if p.ctx.Err() != nil {
			return recordObservation{}, context.Cause(p.ctx)
		}
		return out, nil
	}
}
func (p *dnssdProvider) Close() error {
	p.cancel(context.Canceled)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeErr
}
func (p *dnssdProvider) run(cfg Config, policy discoveryPolicy, reg dnssdRegistration, open dnssdBackendFactory, binding dnssdBinding) {
	defer close(p.done)
	expiry := cfg.Clock.NewTimer(SessionLifetime)
	defer expiry.Stop()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-p.ctx.Done():
		case <-expiry.C():
			p.cancel(ErrExpired)
		}
	}()
	defer func() { p.cancel(context.Canceled); <-watchDone }()
	original, err := binding(cfg.Scope, nil, policy)
	if err != nil {
		p.cancel(err)
		return
	}
	// Daemon callbacks can remain silent while their queries stay active. A
	// separate bounded watcher detects local binding changes in that interval.
	bindingDone := make(chan struct{})
	go func() {
		defer close(bindingDone)
		for {
			tick := cfg.Clock.NewTimer(time.Second)
			select {
			case <-p.ctx.Done():
				tick.Stop()
				return
			case <-tick.C():
				tick.Stop()
			}
			if _, err := binding(cfg.Scope, &original, policy); err != nil {
				p.cancel(err)
				return
			}
		}
	}()
	defer func() { p.cancel(context.Canceled); <-bindingDone }()
	backend, openErr := open(reg)
	if backend == nil {
		if openErr == nil {
			openErr = ErrDNSServiceUnavailable
		}
		p.cancel(openErr)
		return
	}
	// Retain an owner even when construction returns owner+error.
	closeDone := make(chan struct{})
	go func() {
		<-p.ctx.Done()
		err := normalizedCloseError(backend.Close())
		p.mu.Lock()
		p.closeErr = err
		p.mu.Unlock()
		close(closeDone)
	}()
	defer func() { p.cancel(context.Canceled); <-closeDone }()
	if openErr != nil {
		p.cancel(openErr)
		return
	}
	if p.ctx.Err() != nil {
		return
	}
	if _, err = binding(cfg.Scope, &original, policy); err != nil {
		p.cancel(err)
		return
	}
	if err = backend.Start(); err != nil {
		p.cancel(err)
		return
	}
	queried := make(map[string]bool, MaxCandidates)
	received, totalBytes, parsed := 0, 0, 0
	window := cfg.Clock.Now()
	for {
		if p.ctx.Err() != nil {
			return
		}
		event, err := backend.Next()
		if err != nil {
			p.cancel(err)
			return
		}
		received++
		if received >= MaxReceivedDatagrams {
			p.cancel(ErrReceiveBudget)
			return
		}
		now := cfg.Clock.Now()
		if now.Sub(window) >= time.Second {
			window = now
			parsed = 0
		}
		if parsed >= MaxParsedPerSecond {
			p.cancel(ErrReceiveBudget)
			return
		}
		parsed++
		if _, err = binding(cfg.Scope, &original, policy); err != nil {
			p.cancel(err)
			return
		}
		if event.InterfaceIndex != reg.InterfaceIndex || event.InterfaceIndex == 0 {
			p.cancel(ErrInvalidPacket)
			return
		}
		record, err := dnssdDecodeRecord(event)
		if err != nil {
			p.cancel(err)
			return
		}
		size := len(event.Name) + len(event.Data)
		if size > maxRecordBytes || totalBytes > maxRecordLifetimeBytes-size {
			p.cancel(ErrReceiveBudget)
			return
		}
		totalBytes += size
		if record.Kind == ptrRecord && !record.Removed {
			sid, host, _ := dnssdSessionNames(record.Target)
			if sid == cfg.Advertisement.SessionID {
				continue
			}
			if !queried[sid] {
				if len(queried) >= MaxCandidates {
					p.cancel(ErrReceiveBudget)
					return
				}
				queried[sid] = true
				for _, q := range []struct {
					name string
					typ  uint16
				}{{record.Target, dnssdTypeSRV}, {record.Target, dnssdTypeTXT}, {host, dnssdTypeA}} {
					if p.ctx.Err() != nil {
						return
					}
					if _, err = binding(cfg.Scope, &original, policy); err != nil {
						p.cancel(err)
						return
					}
					if err = backend.Query(q.name, q.typ); err != nil {
						p.cancel(err)
						return
					}
				}
			}
		}
		out := recordObservation{Kind: osDNSServiceObservation, InterfaceIndex: cfg.Scope.InterfaceIndex, Records: []discoveryRecord{record}}
		if p.ctx.Err() != nil {
			return
		}
		select {
		case p.observations <- out:
		case <-p.ctx.Done():
			return
		}
	}
}
