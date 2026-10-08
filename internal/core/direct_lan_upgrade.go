package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// UpgradeIntent carries a single locally approved, expiring selection. It is
// not a receipt, transcript, saved confirmation or permission to renew consent.
// The lifecycle launcher may carry these exact fields across a process exit.
type UpgradeIntent struct {
	PeerID           string `json:"peerId"`
	Deadline         string `json:"deadline"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
}
type UpgradeReview struct {
	Revision          string             `json:"revision"`
	PeerID            string             `json:"peerId"`
	Deadline          string             `json:"deadline"`
	LocalEndpoint     string             `json:"localEndpoint"`
	PeerEndpoint      string             `json:"peerEndpoint"`
	Scope             endpointmeta.Scope `json:"scope"`
	RestartRequired   bool               `json:"restartRequired"`
	ResumePreparation bool               `json:"resumePreparation"`
	PreviousDeadline  string             `json:"previousDeadline,omitempty"`
}
type UpgradeProgress struct {
	State           string `json:"state"`
	PeerID          string `json:"peerId,omitempty"`
	Deadline        string `json:"deadline,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	Error           string `json:"error,omitempty"`
	RestartRequired bool   `json:"restartRequired,omitempty"`
}
type contextUpgradeJob struct {
	intent UpgradeIntent
	policy string
	view   UpgradeProgress // Core.mu
	cancel context.CancelFunc
	done   chan struct{}
}

func decodeUpgradeIntent(raw json.RawMessage) (UpgradeIntent, error) {
	var in UpgradeIntent
	if len(raw) > 1024 {
		return in, endpointmeta.ErrCapacity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || len(in.PeerID) > 64 || len(in.ExpectedRevision) > 64 {
		return in, endpointmeta.ErrInvalid
	}
	return in, nil
}
func upgradeDeadline(raw string, now time.Time) (time.Time, error) {
	until, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || until.UTC().Format(time.RFC3339Nano) != raw {
		return time.Time{}, endpointmeta.ErrInvalid
	}
	if !until.After(now) || until.Sub(now) > 5*time.Minute {
		return time.Time{}, endpointmeta.ErrExpired
	}
	return until, nil
}

// Core.op is held. A restart review deliberately omits process identity but
// binds the complete observed protected state, profile, capacity and expiry.
// The new process must reread those same bytes; no admission survives reopen.
func (c *Core) reviewUpgradeLocked(in UpgradeIntent) (UpgradeReview, error) {
	now := time.Now()
	if _, err := upgradeDeadline(in.Deadline, now); err != nil {
		return UpgradeReview{}, err
	}
	c.mu.RLock()
	s, closing, attempted, pending := c.directLAN, c.closing, c.attemptedNetwork, c.managedCleanupPending
	c.mu.RUnlock()
	if s == nil || closing || pending || c.ctx.Err() != nil {
		return UpgradeReview{}, directlan.ErrUnavailable
	}
	p := c.profileCopy()
	if p.Settings.Network != "direct-lan" && p.Settings.Network != "mixed" {
		return UpgradeReview{}, endpointmeta.ErrReview
	}
	capacity := c.capacityPolicy()
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.endpointModelLocked(now, false)
	if err != nil {
		return UpgradeReview{}, err
	}
	i, err := contextPeer(*m, in.PeerID)
	if err != nil {
		return UpgradeReview{}, err
	}
	r := m.Peers[i]
	if r.ContextConfirmed {
		return UpgradeReview{}, endpointmeta.ErrReview
	}
	if m.PreviousLocalEndpoint != m.LocalPeer.Endpoint {
		return UpgradeReview{}, endpointmeta.ErrReview
	}
	for _, other := range m.Peers {
		if other.PairRevocation != nil {
			continue
		}
		if other.Peer.Key != in.PeerID && (other.UpgradePending != nil || other.PairContext != nil && !other.ContextConfirmed) {
			return UpgradeReview{}, endpointmeta.ErrReview
		}
		if other.EndpointState != nil {
			if other.PairContext == nil {
				return UpgradeReview{}, endpointmeta.ErrReview
			}
			initial, e := endpointmeta.InitialState(*other.PairContext, m.LocalPeer.Key)
			if e != nil || !reflect.DeepEqual(initial, *other.EndpointState) {
				return UpgradeReview{}, endpointmeta.ErrReview
			}
		}
	}
	review := UpgradeReview{PeerID: in.PeerID, Deadline: in.Deadline, LocalEndpoint: m.LocalPeer.Endpoint, PeerEndpoint: r.Peer.Endpoint, Scope: m.LocalScope, RestartRequired: attempted != ""}
	review.Scope.Prefixes = append([]string(nil), review.Scope.Prefixes...)
	if r.UpgradePending != nil && r.PairContext == nil {
		if _, err := endpointmeta.ReviewContextResume(*m, in.PeerID, in.Deadline, now); err != nil {
			return UpgradeReview{}, err
		}
		review.ResumePreparation = true
		review.PreviousDeadline = r.UpgradePending.PrepareDeadline
	}
	// No caller can choose nonce, peer revision, remote scope or a transcript.
	in.ExpectedRevision = ""
	review.Revision = privateRevision(struct {
		Domain  string
		File    string
		State   directLANState
		Profile Profile
		Policy  any
		Intent  UpgradeIntent
	}{"context-upgrade-intent-v1", s.fileDigest, cloneDirectLANState(s.state), p, capacity, in})
	return review, nil
}

// This is a narrow two-stage command entry: short admissions own Core.op;
// transport work runs in the bounded job with that lock released. It does not
// change locking for any other command or cache expiring review results.
func (c *Core) upgradeCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	in, err := decodeUpgradeIntent(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	c.op.Lock()
	defer c.op.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if c.ctx.Err() != nil {
		return nil, c.ctx.Err()
	}
	c.mu.RLock()
	old := c.contextUpgrade
	c.mu.RUnlock()
	if name == "direct-lan.upgrade.status" || name == "direct-lan.upgrade.cancel" {
		if in != (UpgradeIntent{}) {
			return nil, directLANEndpointError(endpointmeta.ErrInvalid)
		}
		if old == nil {
			return UpgradeProgress{State: "idle"}, nil
		}
		if name == "direct-lan.upgrade.cancel" {
			old.cancel()
		}
		c.mu.RLock()
		view := old.view
		c.mu.RUnlock()
		return view, nil
	}
	if name != "direct-lan.upgrade.review" && name != "direct-lan.upgrade.run" {
		return nil, endpointmeta.ErrInvalid
	}
	if old != nil {
		select {
		case <-old.done:
		default:
			if name == "direct-lan.upgrade.run" && old.intent == in {
				c.mu.RLock()
				view := old.view
				c.mu.RUnlock()
				return view, nil
			}
			return nil, &localCommandError{"direct_lan_upgrade_busy", "an upgrade is already in progress; inspect or cancel it first"}
		}
	}
	review, err := c.reviewUpgradeLocked(in)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	if name == "direct-lan.upgrade.review" {
		return review, nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != review.Revision {
		return nil, directLANEndpointReviewChanged()
	}
	if review.RestartRequired {
		return UpgradeProgress{State: "restart-required", PeerID: in.PeerID, Deadline: in.Deadline, RestartRequired: true}, nil
	}
	until, _ := time.Parse(time.RFC3339Nano, in.Deadline)
	lifetime, cancel := context.WithDeadline(c.ctx, until)
	stopCaller := context.AfterFunc(ctx, cancel)
	defer stopCaller()
	job := &contextUpgradeJob{intent: in, policy: c.upgradePolicyDigest(), cancel: cancel, done: make(chan struct{}), view: UpgradeProgress{State: "preparing", PeerID: in.PeerID, Deadline: in.Deadline}}
	// Preparation is a reviewed local reducer through the existing sole writer.
	s, _, err := c.contextStoreLocked()
	if err == nil {
		s.mu.Lock()
		m, e := s.endpointModelLocked(time.Now(), false)
		var input contextInputs
		if e == nil {
			i, _ := contextPeer(*m, in.PeerID)
			if m.Peers[i].PairContext == nil {
				if review.ResumePreparation {
					var resume endpointmeta.ContextResumeReview
					resume, e = endpointmeta.ReviewContextResume(*m, in.PeerID, in.Deadline, time.Now())
					input = contextInputs{Operation: contextResume, PeerKey: in.PeerID, Resume: resume}
				} else {
					input, e = s.contextPreparationInputsLocked(*m, in.PeerID, in.Deadline)
				}
			}
		}
		s.mu.Unlock()
		if e == nil && input.Operation != 0 {
			var admission contextAdmission
			admission, e = c.reviewContextLocked(input)
			if e == nil {
				_, e = c.applyContextLocked(lifetime, admission, contextTranscript{})
			}
		}
		err = e
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		return nil, directLANEndpointError(err)
	}
	c.mu.Lock()
	if c.closing || c.ctx.Err() != nil || lifetime.Err() != nil {
		c.mu.Unlock()
		cancel()
		return nil, context.Canceled
	}
	c.contextUpgrade = job
	c.mu.Unlock()
	go c.runContextUpgrade(lifetime, job)
	return UpgradeProgress{State: "preparing", PeerID: in.PeerID, Deadline: in.Deadline}, nil
}

func (c *Core) upgradePolicyDigest() string {
	return privateRevision(struct {
		Profile  Profile
		Capacity any
	}{c.profileCopy(), c.capacityPolicy()})
}

func (c *Core) upgradePhase(job *contextUpgradeJob, phase string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.contextUpgrade != job {
		return
	}
	job.view.State = phase
	if err != nil {
		coded := directLANEndpointError(err)
		job.view.Error = coded.Error()
		job.view.ErrorCode = networkErrorCode(coded)
	}
}

func (c *Core) runContextUpgrade(ctx context.Context, job *contextUpgradeJob) {
	defer close(job.done)
	defer job.cancel()
	err := c.startContextControl(ctx)
	if err == nil {
		err = c.driveContextUpgrade(ctx, job)
	}
	if err != nil {
		c.op.Lock()
		closeErr := c.stopContextControlLocked()
		c.op.Unlock()
		err = errors.Join(err, closeErr)
		state := "failed"
		if errors.Is(err, context.Canceled) {
			state = "cancelled"
		}
		c.upgradePhase(job, state, err)
	}
}

// Each pass captures fresh, exact admissions. A failed wire may retry within
// the original deadline; neither the job nor retries refresh any consent.
func (c *Core) driveContextUpgrade(ctx context.Context, job *contextUpgradeJob) error {
	c.upgradePhase(job, "exchanging", nil)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Never queue behind a completing inbound response. A queued controller
		// can acquire Core.op between completion and its nonwaiting send gate.
		if !c.op.TryLock() {
			if err := waitUpgradePoll(ctx); err != nil {
				return err
			}
			continue
		}
		if job.policy != c.upgradePolicyDigest() {
			c.op.Unlock()
			return endpointmeta.ErrReview
		}
		c.mu.RLock()
		owner := c.contextControl
		c.mu.RUnlock()
		if err := c.currentContextOwnerLocked(owner); err != nil {
			c.op.Unlock()
			return err
		}
		owner.store.mu.Lock()
		m, err := owner.store.endpointModelLocked(time.Now(), false)
		var record endpointmeta.PeerRecord
		localKey := ""
		if err == nil {
			var i int
			i, err = contextPeer(*m, job.intent.PeerID)
			if err == nil {
				record = m.Peers[i]
				localKey = m.LocalPeer.Key
			}
		}
		owner.store.mu.Unlock()
		if err != nil {
			c.op.Unlock()
			return err
		}
		if record.ContextConfirmed {
			c.upgradePhase(job, "connecting", nil)
			// The coordinator joins context control, requires its current receipt and
			// constructs the ordinary duplicate-completion responder. It never derives
			// confirmation from this progress view.
			err = c.startNetwork(ctx)
			c.op.Unlock()
			if err != nil {
				return err
			}
			c.upgradePhase(job, "network-started", nil)
			return nil
		}
		if record.PairContext == nil && contextSavedPair(record) != nil && localKey > record.Peer.Key {
			// The responder can already hold an authenticated preparation while
			// its peer has switched to ordinary service. Commit that exact saved
			// binding through the existing owner/publisher before requesting
			// status. The unsent commit attempt is cancelled, never presented as
			// wire evidence; only the subsequent real status reply can confirm.
			commit, commitErr := c.prepareContextOutboundLocked(ctx, owner, job.intent.PeerID, directlan.ContextCommit)
			if commitErr != nil {
				c.op.Unlock()
				return commitErr
			}
			commit.attempt.Cancel()
			delete(owner.operations, commit.attempt)
		}
		operation, outboundSide, opposite := upgradeExchangePlan(localKey, record)
		if operation != directlan.ContextPrepare {
			c.upgradePhase(job, "confirming", nil)
		}
		var outbound *contextExchangeOperation
		if outboundSide {
			outbound, err = c.prepareContextOutboundLocked(ctx, owner, job.intent.PeerID, operation)
			if err == nil && opposite != 0 {
				_, err = c.armUpgradeInboundLocked(ctx, owner, job.intent.PeerID, opposite)
			}
		} else {
			_, err = c.armUpgradeInboundLocked(ctx, owner, job.intent.PeerID, operation)
		}
		if err == nil && operation != directlan.ContextPrepare {
			// A prepared response may have been lost before the initiator
			// saved it. Until local confirmation, retain the exact duplicate
			// prepare responder as well; no new nonce or binding is accepted.
			_, err = c.armUpgradeInboundLocked(ctx, owner, job.intent.PeerID, directlan.ContextPrepare)
		}
		c.op.Unlock()
		if err != nil {
			return err
		}
		if outbound != nil {
			if err := c.exchangeUpgradeContext(ctx, owner, outbound, job.policy); err != nil {
				return err
			}
		}
		// Exchange completion alone is never success. The next pass reobserves the
		// actual sole-publisher state and validates the current owner/record again.
		if err := waitUpgradePoll(ctx); err != nil {
			return err
		}
	}
}

// Deterministic roles avoid cancelling a same-operation inbound/outbound slot.
// After commitment, opposite operations allow both sides to recover a lost
// response without a bilateral manual Connect step or a new wire operation.
func upgradeExchangePlan(local string, r endpointmeta.PeerRecord) (directlan.ContextOperation, bool, directlan.ContextOperation) {
	initiator := local < r.Peer.Key
	if contextSavedPair(r) == nil {
		return directlan.ContextPrepare, initiator, 0
	}
	if !initiator {
		return directlan.ContextStatus, true, directlan.ContextCommit
	}
	return directlan.ContextCommit, true, directlan.ContextStatus
}

// Polling must not cancel an in-flight inbound TLS exchange merely because a
// slow peer has not completed within one UI/controller polling interval.
func (c *Core) armUpgradeInboundLocked(ctx context.Context, owner *contextControlOwner, key string, op directlan.ContextOperation) (*contextExchangeOperation, error) {
	for attempt, existing := range owner.operations {
		if existing.direction == directlan.ContextInbound && existing.operation == op && existing.admission.inputs.PeerKey == key && (existing.epoch.Valid() || existing.responseEpoch.Valid()) && !attempt.Cancelled() {
			return existing, nil
		}
	}
	return c.armContextInboundLocked(ctx, owner, key, op)
}

// The outbound wire may own its full handshake/cleanup bound. During that wait
// an incoming one-shot response can be consumed or rejected, including a lost
// duplicate PREPARE while the peer has not learned the binding. Keep inbound
// service armed independently; never start a second outbound exchange here.
func (c *Core) exchangeUpgradeContext(ctx context.Context, owner *contextControlOwner, outbound *contextExchangeOperation, policy string) error {
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.exchangeContext(run, outbound)
	}()
	// Cancellation stops the exact wire; its cleanup is joined without Core.op.
	// No callback, attempt or goroutine may escape this controller iteration.
	defer func() { cancel(); <-done }()
	return waitUpgradeExchange(ctx, done, func() error {
		return c.refreshUpgradeInbound(run, owner, outbound, policy)
	})
}

func (c *Core) refreshUpgradeInbound(run context.Context, owner *contextControlOwner, outbound *contextExchangeOperation, policy string) error {
	// Skipping one maintenance tick retains current arms. Queueing a waiter can
	// repeatedly steal this mutex immediately before the response's TryLock.
	if !c.op.TryLock() {
		return nil
	}
	defer c.op.Unlock()
	if policy != c.upgradePolicyDigest() {
		return endpointmeta.ErrReview
	}
	if err := c.currentContextOwnerLocked(owner); err != nil {
		return err
	}
	owner.store.mu.Lock()
	model, err := owner.store.endpointModelLocked(time.Now(), false)
	var record endpointmeta.PeerRecord
	local := ""
	if err == nil {
		var i int
		i, err = contextPeer(*model, outbound.admission.inputs.PeerKey)
		if err == nil {
			record, local = model.Peers[i], model.LocalPeer.Key
		}
	}
	owner.store.mu.Unlock()
	if err != nil {
		return err
	}
	if record.ContextConfirmed {
		return nil
	} // next pass performs the handoff
	operation, sends, opposite := upgradeExchangePlan(local, record)
	if !sends {
		_, err = c.armUpgradeInboundLocked(run, owner, record.Peer.Key, operation)
	}
	if err == nil && opposite != 0 {
		_, err = c.armUpgradeInboundLocked(run, owner, record.Peer.Key, opposite)
	}
	if err == nil && operation != directlan.ContextPrepare {
		_, err = c.armUpgradeInboundLocked(run, owner, record.Peer.Key, directlan.ContextPrepare)
	}
	return err
}

// Waiting for an outbound result must not suspend inbound maintenance. The
// refresh function runs synchronously, never concurrently with itself, and
// only until the original bounded context or exact exchange has ended.
func waitUpgradeExchange(ctx context.Context, done <-chan struct{}, refresh func() error) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		case <-ticker.C:
			// Prefer an already-ended exchange over creating an unnecessary arm.
			select {
			case <-done:
				return nil
			default:
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}

func waitUpgradePoll(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
