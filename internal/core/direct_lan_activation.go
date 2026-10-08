package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// An activation admission is private, one-use and bound to the complete file,
// projected membership, policy and process. It is not a caller-provided config.
// Core.op serializes every field; store.mu is never held across owner joins.
type managedActivationAdmission struct {
	core                                      *Core
	store                                     *directLANStore
	process, path, file, state, configuration string
	revision                                  uint64
	limits                                    lanStoreLimits
	limitsSource                              *lanStoreLimits
	projection                                managedFixedEndpointProjection
	currentEndpoints                          bool
	currentProjection                         string
	deadlines                                 map[directLANEndpointDeadlineKey]directLANEndpointDeadline
	receipt                                   *contextPublicationReceipt
	consumed                                  bool
	mode, hostname                            string
	removal                                   *managedRemovalOwner
}

func (c *Core) activationOwnerCurrent(ctx context.Context, a *managedActivationAdmission) bool {
	if a == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return a.core == c && a.store == c.directLAN && a.process != "" && a.process == c.lanStartNonce &&
		!c.closing && c.ctx != nil && c.ctx.Err() == nil && c.node == nil &&
		!c.managedCleanupPending && c.managedCleanupError == nil && c.managedRemoval == a.removal &&
		c.profile.Settings.Network == a.mode && c.profile.Settings.Hostname == a.hostname &&
		(c.attemptedNetwork == "" || c.attemptedNetwork == a.mode && c.attemptedHostname == a.hostname && a.removal != nil && a.removal.joined && a.removal.core == c && a.removal.store == a.store && a.removal.process == a.process && a.removal.attemptedNetwork == a.mode && a.removal.attemptedHostname == a.hostname)
}

func (s *directLANStore) captureActivationLocked(c *Core, now time.Time) (*managedActivationAdmission, error) {
	projection, err := s.managedFixedEndpointProjectionLocked(now)
	var current directlan.Config
	currentEndpoints := err != nil
	var deadlines map[directLANEndpointDeadlineKey]directLANEndpointDeadline
	if currentEndpoints {
		current, err = s.managedCurrentEndpointProjectionLocked(now)
		if err == nil {
			projection = managedFixedEndpointProjection{Peers: current.Peers, PairContexts: current.PairContexts, DeniedPeerKeys: current.DeniedPeerKeys}
			deadlines, err = s.activeEndpointDeadlinesLocked(current, now)
		}
	}
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	limits := *s.currentCapacity()
	if err != nil || int64(len(data))+1 > limits.bytes || limits.peers > 0 && int64(len(projection.Peers)) > limits.peers {
		return nil, directlan.ErrCapacity
	}
	p := c.profileCopy()
	a := &managedActivationAdmission{core: c, store: s, process: c.lanStartNonce, path: s.path, file: s.fileDigest, state: privateRevision(s.state), configuration: contextConfigurationDigest(s.state), revision: s.reviewRevision, limits: limits, limitsSource: s.limits.Load(), projection: projection, mode: p.Settings.Network, hostname: p.Settings.Hostname, removal: c.managedRemoval, currentEndpoints: currentEndpoints, deadlines: deadlines}
	if currentEndpoints {
		a.currentProjection = endpointProjectionIdentity(current)
	}
	return a, nil
}
func (s *directLANStore) matchActivationLocked(a *managedActivationAdmission, now time.Time) error {
	if a == nil || a.store != s || a.path != s.path || a.file != s.fileDigest || a.state != privateRevision(s.state) || a.configuration != contextConfigurationDigest(s.state) || a.revision != s.reviewRevision || a.limits != *s.currentCapacity() || a.limitsSource != s.limits.Load() {
		return endpointmeta.ErrReview
	}
	projection, err := s.activationProjectionLocked(a, now)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a.projection, projection) {
		return endpointmeta.ErrReview
	}
	return nil
}

// Current reopen uses the same exact successful no-delta publication boundary
// as fixed activation. A saved flag or re-observed file never becomes a receipt.
func (s *directLANStore) activationProjectionLocked(a *managedActivationAdmission, now time.Time) (managedFixedEndpointProjection, error) {
	if !a.currentEndpoints {
		return s.managedFixedEndpointProjectionLocked(now)
	}
	// Check original entries before observation could populate a missing key.
	for key, bound := range a.deadlines {
		got, ok := s.endpointDeadlines[key]
		if !ok || got != bound || bound.expired || !now.Before(bound.monotonic) {
			return managedFixedEndpointProjection{}, endpointmeta.ErrExpired
		}
	}
	cfg, err := s.managedCurrentEndpointProjectionLocked(now)
	if err != nil {
		return managedFixedEndpointProjection{}, err
	}
	if endpointProjectionIdentity(cfg) != a.currentProjection {
		return managedFixedEndpointProjection{}, endpointmeta.ErrReview
	}
	deadlines, err := s.activeEndpointDeadlinesLocked(cfg, now)
	if err != nil || !reflect.DeepEqual(deadlines, a.deadlines) {
		return managedFixedEndpointProjection{}, endpointmeta.ErrReview
	}
	return managedFixedEndpointProjection{Peers: cfg.Peers, PairContexts: cfg.PairContexts, DeniedPeerKeys: cfg.DeniedPeerKeys}, nil
}

// Whole-state no-delta republish deliberately has no peer target. All-terminal
// and legacy-only v4 files use this exact same publisher and receipt boundary.
func (s *directLANStore) republishActivationLocked(a *managedActivationAdmission, live *contextSaveLiveness, now time.Time) error {
	if err := live.err(); err != nil {
		return err
	}
	if err := s.matchActivationLocked(a, now); err != nil {
		return err
	}
	if !s.contextPublicationCurrentLocked(a.process) {
		if err := s.writeContextPublicationLocked(a.process, cloneDirectLANState(s.state), live); err != nil {
			return err
		}
		a.file, a.state, a.revision = s.fileDigest, privateRevision(s.state), s.reviewRevision
	}
	a.receipt = s.contextPublication
	return live.err()
}

// This is the sole ordinary construction coordinator. runtimeConfig remains
// closed for managed/v4 evidence and cannot serve as an alternate factory.
func (c *Core) prepareDirectLANActivationLocked(ctx context.Context, s *directLANStore) (*managedActivationAdmission, error) {
	s.mu.Lock()
	a, err := s.captureActivationLocked(c, time.Now())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !c.activationOwnerCurrent(ctx, a) {
		return nil, endpointmeta.ErrReview
	}
	c.mu.RLock()
	control := c.contextControl
	c.mu.RUnlock()
	if control != nil && (control.store != s || control.process != a.process) {
		return nil, endpointmeta.ErrReview
	}
	s.mu.Lock()
	err = s.republishActivationLocked(a, &contextSaveLiveness{ctx: ctx, core: c.ctx}, time.Now())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = c.stopContextControlLocked(); err != nil {
		return nil, err
	}
	// Even absent context control, an abandoned previous epoch is invalidated;
	// no ordinary factory may inherit it or manufacture authority from it.
	s.mu.Lock()
	if s.contextEpoch != nil {
		s.contextEpoch.Invalidate()
		s.contextEpoch = nil
	}
	err = s.matchActivationLocked(a, time.Now())
	if err == nil && (!s.contextPublicationCurrentLocked(a.process) || s.contextPublication != a.receipt) {
		err = endpointmeta.ErrReview
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !c.activationOwnerCurrent(ctx, a) {
		return nil, endpointmeta.ErrReview
	}
	return a, nil
}

func (c *Core) newAdmittedDirectLANBackendLocked(ctx context.Context, s *directLANStore) (NetworkBackend, error) {
	state := s.copy()
	if state.Version != directLANPairRecordStateVersion && !directLANMetadataManaged(state.Metadata) {
		c.mu.RLock()
		control := c.contextControl
		c.mu.RUnlock()
		if control == nil {
			return c.newDirectLANBackend(s)
		}
	}
	a, err := c.prepareDirectLANActivationLocked(ctx, s)
	if err != nil {
		return nil, err
	}
	candidate, err := c.newManagedCompletionBackendLocked(s, a)
	return ordinaryDirectLANCandidate(candidate, err)
}

// Caller holds Core.op. The root is fixed once, before Start. Mixed ownership
// requires BOTH exact containing root and exact direct-LAN child identity.
func bindManagedActivationRoot(n NetworkBackend) {
	switch b := n.(type) {
	case *directLANBackend:
		if b.completion != nil {
			b.completion.root = n
		}
	case *mixedBackend:
		if child, ok := b.nodes["direct-lan"].(*directLANBackend); ok && child.completion != nil {
			child.completion.root = b
		}
	}
}
func managedActivationChild(n NetworkBackend) *managedCompletionOwner {
	switch b := n.(type) {
	case *directLANBackend:
		return b.currentCompletion()
	case *mixedBackend:
		if child, ok := b.nodes["direct-lan"].(*directLANBackend); ok {
			return child.currentCompletion()
		}
	}
	return nil
}

// Retain the exact failed candidate until its Close result is known successful.
// Repeated Close returns the original retained error on concrete backends.
func (c *Core) closeFailedNetworkCandidateLocked(n NetworkBackend, cause error) error {
	c.networkReady.Store(false)
	c.mu.Lock()
	c.networkState = "error"
	c.mu.Unlock()
	closeErr := n.Close()
	if closeErr == nil {
		c.mu.Lock()
		if c.node == n {
			c.node = nil
		}
		c.mu.Unlock()
	}
	return c.safeNetworkStartupError(c.profileCopy().Settings.Network, errors.Join(cause, closeErr))
}

// Never box a nil concrete factory result into NetworkBackend. A genuinely
// retained candidate remains observable alongside its error.
func ordinaryDirectLANCandidate(candidate *directLANBackend, err error) (NetworkBackend, error) {
	if candidate == nil {
		return nil, err
	}
	return candidate, err
}

// Public presentation is bounded to trusted product text; diagnostic causes
// remain available to errors.Is/As without being interpolated into Error().
type networkStartupError struct {
	outcome error
	cause   error
}

func (e *networkStartupError) Error() string     { return e.outcome.Error() }
func (e *networkStartupError) ErrorCode() string { return networkErrorCode(e.outcome) }
func (e *networkStartupError) Unwrap() []error   { return []error{e.outcome, e.cause} }
func (c *Core) safeNetworkStartupError(mode string, cause error) error {
	if cause == nil {
		return nil
	}
	mapped := cause
	if mode == "lan" {
		mapped = codedLANError(cause)
	}
	if mode == "mixed" {
		mapped = c.codedMixedError(cause)
	}
	var outcome error = errors.New("could not start network; private identity state was retained")
	var lan *lanCommandError
	var local *localCommandError
	var mixed *mixedError
	switch {
	case errors.As(mapped, &lan):
		outcome = lan
	case errors.As(mapped, &local):
		outcome = local
	case errors.As(mapped, &mixed):
		outcome = mixed
	}
	return &networkStartupError{outcome: outcome, cause: cause}
}
