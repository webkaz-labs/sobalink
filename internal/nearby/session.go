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
	provider       recordProvider
	assembler      recordAssembler
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

// StartNative selects the platform provider after the caller's explicit scope
// and advertisement consent. It does not create a bootstrap listener.
func StartNative(ctx context.Context, cfg Config) (*Session, error) {
	return startProvider(ctx, cfg, newNativeRecordProvider, discoveryPolicy{})
}
func start(ctx context.Context, cfg Config, open TransportFactory, policy discoveryPolicy) (*Session, error) {
	if open == nil {
		return nil, ErrUnsupported
	}
	return startProvider(ctx, cfg, func(ctx context.Context, cfg Config, p discoveryPolicy) (recordProvider, error) {
		return newRawRecordProvider(ctx, cfg, p, open)
	}, policy)
}
func startProvider(ctx context.Context, cfg Config, open recordProviderFactory, policy discoveryPolicy) (*Session, error) {
	if err := policy.validateScope(cfg.Scope); err != nil {
		return nil, err
	}
	if !cfg.Scope.LocalAddress.Is4() {
		return nil, ErrUnsupported
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
	provider, startErr := open(ctx, cfg, policy)
	if provider == nil {
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
	s := &Session{store: store, clock: cfg.Clock, policy: policy, deadline: deadline, local: cfg.Advertisement, provider: provider, assembler: newRecordAssembler(cfg.Scope, policy, deadline, cfg.Advertisement.SessionID), ctx: ctx, cancel: cancel, done: make(chan struct{}), closeDone: make(chan struct{})}
	if startErr != nil {
		s.err = startErr
		if errors.Is(startErr, ErrCloseUncertain) || errors.Is(startErr, ErrCloseUnresolved) {
			s.closeErr = startErr
		}
		s.cancel(startErr)
		s.requestProviderClose()
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
func (s *Session) requestProviderClose() {
	s.closeOnce.Do(func() {
		go func() {
			err := normalizedCloseError(s.provider.Close())
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

// Close cancels admission immediately and starts one owned provider-close
// operation. It waits at most two seconds for all owners to join; retries reuse
// the same operation and retained first close result. On any error the caller
// must retain this Session and block replacement rather than report stopped.
func (s *Session) Close() error {
	s.cancel(context.Canceled)
	s.requestProviderClose()
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
	observation recordObservation
	err         error
}

func (s *Session) run(ctx context.Context) {
	defer close(s.done)
	expires := s.clock.NewTimer(max(time.Duration(0), s.deadline.Sub(s.clock.Now())))
	defer expires.Stop()
	raw, isRaw := s.provider.(*rawRecordProvider)
	queueSize := 0
	if isRaw {
		queueSize = maxObservationQueue
	}
	// OS callback queues own their bound; do not add another buffered layer.
	packets := make(chan readResult, queueSize)
	readDone := make(chan struct{})
	watchDone := make(chan struct{})
	// This watcher closes a blocked provider even when the main loop is in Write.
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
		case <-expires.C():
			s.cancel(ErrExpired)
		}
		expires.Stop()
		s.requestProviderClose()
	}()
	go func() {
		defer close(readDone)
		for read := 0; read < MaxReceivedDatagrams; read++ {
			p, err := s.provider.ReadObservation()
			if isRaw {
				select {
				case packets <- readResult{p, err}:
				case <-ctx.Done():
					return
				default:
					s.cancel(ErrReceiveBudget)
					return
				}
			} else {
				select {
				case packets <- readResult{p, err}:
				case <-ctx.Done():
					return
				}
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
		s.requestProviderClose()
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
		if !isRaw {
			return nil
		}
		return raw.WritePacket(b)
	}
	if isRaw {
		if err := send(query); err != nil {
			finish(err)
			return
		}
		if err := send(ad); err != nil {
			finish(err)
			return
		}
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
			if isRaw && now.Sub(lastAd) >= AdvertisementInterval {
				if err := send(ad); err != nil {
					finish(err)
					return
				}
				lastAd = now
			}
			if isRaw && now.Sub(lastQuery) >= QueryInterval {
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
				// Dropping an OS removal/conflict while queries remain active
				// could retain a stale hint. Fence the whole owner instead.
				if !isRaw {
					finish(ErrReceiveBudget)
					return
				}
				continue
			}
			// Invalid metadata still consumes the finite receive budget. Parsing work
			// and per-second responses have separate budgets from input admission.
			parsed++
			observed := result.observation
			if !isRaw && (observed.Kind != osDNSServiceObservation || observed.query) {
				// Only the raw adapter validates socket metadata. A system
				// provider may never claim raw provenance for OS callbacks.
				finish(ErrInvalidPacket)
				return
			}
			if observed.query {
				if isRaw && observed.Kind == rawPacketObservation && now.Sub(lastAd) >= time.Second {
					if err := send(ad); err != nil {
						finish(err)
						return
					}
					lastAd = now
				}
				continue
			}
			s.mu.Lock()
			err := s.assembler.observe(observed, now, &s.store)
			var followups []string
			if err == nil && isRaw {
				followups = s.assembler.requestQueries(observed, now, &s.store)
			}
			s.mu.Unlock()
			if (!isRaw && err != nil) || errors.Is(err, ErrReceiveBudget) || errors.Is(err, ErrExpired) {
				finish(err)
				return
			}
			for _, sid := range followups {
				if err := send(packRecordQuery(sid)); err != nil {
					finish(err)
					return
				}
			}
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
