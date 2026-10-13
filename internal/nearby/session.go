package nearby

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Config struct {
	Scope         Scope
	Advertisement Advertisement
	Clock         Clock
}

// Session owns a single explicit discovery scope and every transport/timer it
// starts. Ending it invalidates all candidates; no hints are persisted.
const closeJoinTimeout = 2 * time.Second

type Session struct {
	mu             sync.Mutex
	store          candidateStore
	clock          Clock
	policy         discoveryPolicy
	deadline       time.Time
	local          Advertisement
	transport      Transport
	ctx            context.Context
	cancel         context.CancelCauseFunc
	closeOnce      sync.Once
	closeDone      chan struct{}
	closeErr       error
	joinUnresolved bool
	done           chan struct{}
	err            error
}

// Start is opt-in: there is no default native factory. A caller must first
// obtain scope/advertisement consent and pass the selected transport explicitly.
// This call does not open a bootstrap listener or establish a peer connection.
// On uncertain startup rollback it returns a nonnil Session together with an
// error. Preserve that owner and block replacement until cleanup is reconciled.
func Start(ctx context.Context, cfg Config, open TransportFactory) (*Session, error) {
	return start(ctx, cfg, open, discoveryPolicy{})
}

func start(ctx context.Context, cfg Config, open TransportFactory, policy discoveryPolicy) (*Session, error) {
	if err := policy.validateScope(cfg.Scope); err != nil {
		return nil, err
	}
	if err := cfg.Advertisement.validate(policy); err != nil {
		return nil, err
	}
	if cfg.Advertisement.Bootstrap.Addr() != cfg.Scope.LocalAddress {
		return nil, ErrInvalidAdvertisement
	}
	if open == nil {
		return nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	generation, err := NewSessionID()
	if err != nil {
		return nil, err
	}
	deadline := cfg.Clock.Now().Add(SessionLifetime)
	transport, startErr := open(cfg.Scope)
	if transport == nil {
		if startErr != nil {
			return nil, startErr
		}
		return nil, ErrUnsupported
	}
	if startErr == nil {
		if ctx.Err() != nil {
			startErr = context.Cause(ctx)
		} else if !cfg.Clock.Now().Before(deadline) {
			startErr = ErrExpired
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	store := newStore(cfg.Scope)
	store.generation = generation
	s := &Session{store: store, clock: cfg.Clock, policy: policy, deadline: deadline, local: cfg.Advertisement, transport: transport, ctx: ctx, cancel: cancel, done: make(chan struct{}), closeDone: make(chan struct{})}
	if startErr != nil {
		s.err = startErr
		if errors.Is(startErr, ErrCloseUncertain) || errors.Is(startErr, ErrCloseUnresolved) {
			s.closeErr = startErr
		}
		s.cancel(startErr)
		s.requestTransportClose()
		go func() { <-s.closeDone; close(s.done) }()
		if closeErr := s.Close(); closeErr != nil {
			return s, errors.Join(startErr, closeErr)
		}
		return nil, startErr
	}
	go s.run(ctx)
	return s, nil
}

func (s *Session) Candidates() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	if s.err != nil || s.ctx.Err() != nil || !now.Before(s.deadline) {
		return []Candidate{}
	}
	return s.store.list(now)
}
func (s *Session) Lookup(id string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	if s.err != nil || s.ctx.Err() != nil || !now.Before(s.deadline) {
		return Candidate{}, ErrCandidateUnavailable
	}
	return s.store.lookup(id, now)
}

// Done closes only after every owned reader, watcher and close worker joins.
// A nonnil Close error still means that resource cleanup is uncertain.
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.joinUnresolved {
		select {
		case <-s.done:
		default:
			return errors.Join(s.err, ErrCloseUncertain, ErrCloseUnresolved)
		}
	}
	return s.err
}
func (s *Session) requestTransportClose() {
	s.closeOnce.Do(func() {
		go func() {
			err := normalizedCloseError(s.transport.Close())
			s.mu.Lock()
			if s.closeErr == nil {
				s.closeErr = err
			}
			if s.closeErr != nil {
				s.err = errors.Join(s.err, s.closeErr)
			}
			s.mu.Unlock()
			close(s.closeDone)
		}()
	})
}

// Close cancels admission immediately and starts one owned socket-close
// operation. It waits at most two seconds for all owners to join; retries reuse
// the same operation and retained first close result. On any error the caller
// must retain this Session and block replacement rather than report stopped.
func (s *Session) Close() error {
	s.cancel(context.Canceled)
	s.requestTransportClose()
	timer := time.NewTimer(closeJoinTimeout)
	defer timer.Stop()
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	case <-timer.C:
		s.mu.Lock()
		defer s.mu.Unlock()
		s.joinUnresolved = true
		return errors.Join(ErrCloseUncertain, ErrCloseUnresolved, s.closeErr)
	}
}

type readResult struct {
	packet Packet
	err    error
}

func (s *Session) run(ctx context.Context) {
	defer close(s.done)
	expires := s.clock.NewTimer(max(time.Duration(0), s.deadline.Sub(s.clock.Now())))
	defer expires.Stop()
	packets := make(chan readResult)
	readDone := make(chan struct{})
	watchDone := make(chan struct{})
	// This watcher closes a blocked socket even when the main loop is in Write.
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
		case <-expires.C():
			s.cancel(ErrExpired)
		}
		expires.Stop()
		s.requestTransportClose()
	}()
	go func() {
		defer close(readDone)
		for read := 0; read < MaxReceivedDatagrams; read++ {
			p, err := s.transport.ReadPacket()
			select {
			case packets <- readResult{p, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var tick Timer
	finish := func(err error) {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		// Publish invalidation before waiting for transport goroutines.
		s.mu.Lock()
		s.err = errors.Join(err, s.closeErr)
		clear(s.store.items)
		s.mu.Unlock()
		s.cancel(err)
		s.requestTransportClose()
		expires.Stop()
		if tick != nil {
			tick.Stop()
		}
		<-readDone
		<-watchDone
		<-s.closeDone
	}
	ad, err := packAdvertisementWithPolicy(s.local, s.policy)
	if err != nil {
		finish(err)
		return
	}
	query := packQuery()
	tick = s.clock.NewTimer(time.Second)
	defer func() { tick.Stop() }()
	sent := 0
	send := func(b []byte) error {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if !s.clock.Now().Before(s.deadline) {
			return ErrExpired
		}
		if sent >= MaxSentDatagrams {
			return ErrSendBudget
		}
		sent++
		return s.transport.WritePacket(b)
	}
	if err := send(query); err != nil {
		finish(err)
		return
	}
	if err := send(ad); err != nil {
		finish(err)
		return
	}
	lastAd, lastQuery := s.clock.Now(), s.clock.Now()
	windowStart := s.clock.Now()
	received, parsed := 0, 0
	for {
		select {
		case <-ctx.Done():
			finish(context.Cause(ctx))
			return
		case <-tick.C():
			now := s.clock.Now()
			if !now.Before(s.deadline) {
				finish(ErrExpired)
				return
			}
			s.mu.Lock()
			s.store.prune(now)
			s.mu.Unlock()
			if now.Sub(lastAd) >= AdvertisementInterval {
				if err := send(ad); err != nil {
					finish(err)
					return
				}
				lastAd = now
			}
			if now.Sub(lastQuery) >= QueryInterval {
				if err := send(query); err != nil {
					finish(err)
					return
				}
				lastQuery = now
			}
			tick = s.clock.NewTimer(time.Second)
		case result := <-packets:
			if result.err != nil {
				err := result.err
				if ctx.Err() != nil {
					err = context.Cause(ctx)
				}
				finish(err)
				return
			}
			received++
			if received >= MaxReceivedDatagrams {
				finish(ErrReceiveBudget)
				return
			}
			now := s.clock.Now()
			if !now.Before(s.deadline) {
				finish(ErrExpired)
				return
			}
			if now.Sub(windowStart) >= time.Second {
				windowStart, parsed = now, 0
			}
			if parsed >= MaxParsedPerSecond {
				continue
			}
			// Invalid metadata still consumes the finite receive budget. Parsing work
			// and per-second responses have separate budgets from input admission.
			parsed++
			if !validatePacketWithPolicy(s.store.scope, result.packet, s.policy) {
				continue
			}
			observed, ttl, isQuery, err := unpackPacketWithPolicy(result.packet, s.policy)
			if err != nil {
				continue
			}
			if isQuery {
				if now.Sub(lastAd) >= time.Second {
					if err := send(ad); err != nil {
						finish(err)
						return
					}
					lastAd = now
				}
				continue
			}
			if observed.SessionID == s.local.SessionID {
				continue
			}
			s.mu.Lock()
			s.store.observe(observed, result.packet.Source, ttl, now)
			s.mu.Unlock()
		}
	}
}

// IsUnavailable reports a closed or exhausted discovery session without
// implying that the network or selected peer is unreachable.
func IsUnavailable(err error) bool {
	if errors.Is(err, ErrCloseUncertain) || errors.Is(err, ErrCloseUnresolved) {
		return false
	}
	return errors.Is(err, ErrExpired) || errors.Is(err, ErrReceiveBudget) || errors.Is(err, ErrSendBudget) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrSocketUnavailable) || errors.Is(err, ErrScopeChanged) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
