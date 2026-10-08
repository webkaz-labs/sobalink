//go:build directlan_activation_native

package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/testfixture"
)

// This opt-in suite starts real fixed-loopback TCP control and ordinary WG/UDP
// owners. It never changes a published endpoint, restarts a process, revokes a
// pair, resumes old consent, or mints authenticated evidence in test code.
// These sources are staged for independent review; their existence is not a run.
type activationNativePair struct {
	t            *testing.T
	ctx          context.Context
	cancel       context.CancelFunc
	cores        [2]*Core
	endpoints    [2]netip.AddrPort
	reservations [2]*testfixture.PortReservation
	once         sync.Once
}

func newActivationNativePair(t *testing.T) *activationNativePair {
	t.Helper()
	if os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1" {
		t.Skip("requires explicit isolated native activation execution")
	}
	if !directLANNetstackReady {
		t.Fatal("native netstack is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	f := &activationNativePair{t: t, ctx: ctx, cancel: cancel}
	t.Cleanup(f.close)
	identities := [2]directlan.Identity{{Seed: strings.Repeat("61", 32)}, {Seed: strings.Repeat("62", 32)}}
	// Both deterministic key orders are exercised through the two actual peers;
	// array zero is the controller's lexical initiator, never a presumed key order.
	if identities[0].PublicKey() > identities[1].PublicKey() {
		identities[0], identities[1] = identities[1], identities[0]
	}
	for i := range f.cores {
		r, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal("fixed endpoint reservation failed")
		}
		f.reservations[i], f.endpoints[i] = r, r.Endpoint()
	}
	for i := range f.cores {
		dir := t.TempDir()
		t.Cleanup(f.close) // Join before TempDir removal, even on early failure.
		c, err := Open(ctx, Options{Directory: dir, Version: "synthetic-activation", SkipNetworkStart: true})
		if err != nil {
			t.Fatal("offline Core construction failed")
		}
		f.cores[i] = c
		peer := directlan.Peer{Key: identities[1-i].PublicKey(), TunnelKey: identities[1-i].TunnelKey(), Endpoint: f.endpoints[1-i], Name: "synthetic-peer"}
		scope := endpointmeta.Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/8"}}
		model := endpointmeta.Snapshot{Version: 4, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: identities[i].PublicKey(), TunnelKey: identities[i].TunnelKey(), Endpoint: f.endpoints[i].String()}, LocalScope: scope, PreviousLocalEndpoint: f.endpoints[i].String(), ObservedAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{{Peer: directLANPeerWire(peer), Revision: "1"}}}
		state := directLANState{Version: 4, Identity: identities[i], Selection: DirectLANSelection{Listen: f.endpoints[i].String(), Prefixes: scope.Prefixes}, Peers: []directlan.Peer{peer}, Metadata: &model}
		if err := validateDirectLANState(state); err != nil {
			t.Fatal("invalid synthetic initial state")
		}
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "direct-lan.json")
		if err := config.AtomicWritePrivate(path, append(data, '\n')); err != nil {
			t.Fatal("fixture state write failed")
		}
		store, err := readDirectLANStore(path, 1<<20, 16)
		if err != nil {
			t.Fatal("fixture state read failed")
		}
		c.mu.Lock()
		c.directLAN = store
		c.profile.Settings.Network = "direct-lan"
		c.profile.Settings.Hostname = "synthetic-activation"
		c.mu.Unlock()
		// Leave store.write nil: all subsequent transitions use the real sole
		// AtomicWrite publisher, including its receipt and file digest boundaries.
	}
	return f
}
func (f *activationNativePair) close() {
	f.once.Do(func() {
		f.cancel()
		done := make(chan error, 1)
		go func() {
			var err error
			for _, c := range f.cores {
				if c != nil {
					err = errors.Join(err, c.Close())
				}
			}
			for _, r := range f.reservations {
				if r != nil {
					err = errors.Join(err, r.Close())
				}
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				f.t.Error("native activation cleanup failed")
			}
		case <-time.After(10 * time.Second):
			f.t.Error("native activation cleanup did not join")
		}
	})
}
func (f *activationNativePair) release(i int) {
	f.t.Helper()
	if err := f.reservations[i].Close(); err != nil {
		f.t.Fatal("endpoint release failed")
	}
}
func (f *activationNativePair) command(i int, name string, value any) any {
	f.t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := f.cores[i].upgradeCommand(f.ctx, name, raw)
	if err != nil {
		f.t.Fatalf("%s rejected: %s", name, networkErrorCode(err))
	}
	return result
}
func (f *activationNativePair) run(i int, deadline time.Time) *contextUpgradeJob {
	f.t.Helper()
	in := UpgradeIntent{PeerID: f.cores[1-i].directLAN.copy().Identity.PublicKey(), Deadline: deadline.UTC().Format(time.RFC3339Nano)}
	review := f.command(i, "direct-lan.upgrade.review", in).(UpgradeReview)
	if review.RestartRequired || review.ResumePreparation {
		f.t.Fatal("fixture left fresh initial-activation scope")
	}
	in.ExpectedRevision = review.Revision
	f.release(i)
	f.command(i, "direct-lan.upgrade.run", in)
	c := f.cores[i]
	c.mu.RLock()
	job := c.contextUpgrade
	c.mu.RUnlock()
	if job == nil {
		f.t.Fatal("controller did not create job")
	}
	return job
}
func (f *activationNativePair) await(job *contextUpgradeJob) {
	f.t.Helper()
	select {
	case <-job.done:
	case <-f.ctx.Done():
		f.t.Fatal("controller failed to join before fixture deadline")
	}
}
func (f *activationNativePair) assertOrdinary(i int, job *contextUpgradeJob) {
	f.t.Helper()
	c := f.cores[i]
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.RLock()
	phase := job.view.State
	owner := c.contextControl
	node := c.node
	c.mu.RUnlock()
	b, ok := node.(*directLANBackend)
	if phase != "network-started" || owner != nil || !ok || b.completion == nil {
		f.t.Fatal("ordinary ownership handoff incomplete")
	}
	if b.ctx.Err() != nil || c.ctx.Err() != nil || b.Node.Endpoint() != f.endpoints[i] {
		f.t.Fatal("ordinary lifetime or fixed endpoint changed")
	}
	s := c.directLAN
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.contextPublicationCurrentLocked(c.lanStartNonce) || b.completion.receipt != s.contextPublication || !b.completion.epoch.Valid() || !s.state.Metadata.Peers[0].ContextConfirmed {
		f.t.Fatal("ordinary authority lacks current sole-publication receipt")
	}
	initial, err := endpointmeta.InitialState(*s.state.Metadata.Peers[0].PairContext, s.state.Identity.PublicKey())
	if err != nil || !reflect.DeepEqual(initial, *s.state.Metadata.Peers[0].EndpointState) {
		f.t.Fatal("initial endpoint state changed")
	}
}

func TestActivationNativeBilateralReviewedController(t *testing.T) {
	f := newActivationNativePair(t)
	deadline := time.Now().Add(45 * time.Second)
	a, b := f.run(0, deadline), f.run(1, deadline)
	f.await(a)
	f.await(b)
	f.assertOrdinary(0, a)
	f.assertOrdinary(1, b)
	// runContextUpgrade cancels both completed workflow contexts. The ordinary
	// owner must remain Core-owned after that cancellation, without another Start.
	if a.view.State != "network-started" || b.view.State != "network-started" {
		t.Fatal("controller did not finish")
	}
}

func TestActivationNativeControllerCancellationAndDeadline(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newActivationNativePair(t)
			deadline := time.Now().Add(3 * time.Second)
			if mode == "cancel" {
				deadline = time.Now().Add(30 * time.Second)
			}
			job := f.run(1, deadline) // responder cannot complete without the other peer
			if mode == "cancel" {
				f.command(1, "direct-lan.upgrade.cancel", UpgradeIntent{})
			}
			f.await(job)
			c := f.cores[1]
			c.op.Lock()
			defer c.op.Unlock()
			c.mu.RLock()
			node, owner, phase := c.node, c.contextControl, job.view.State
			c.mu.RUnlock()
			if node != nil || owner != nil || (phase != "cancelled" && phase != "failed") {
				t.Fatal("cancelled/expired controller retained runnable authority")
			}
			state := c.directLAN.copy()
			if state.Metadata.Peers[0].ContextConfirmed || state.Metadata.Peers[0].PairContext != nil {
				t.Fatal("unanswered controller fabricated confirmation")
			}
		})
	}
}

// The fault cases below use the same real constructor/Start and completion
// callback as startContextControl, but install a test-local wrapper BEFORE
// publishing the Node. This is intentionally not command-entry coverage.
// The wrapper can only return an error after real completion; it never claims,
// creates or changes verified evidence, replies, receipts or stored state.
func (f *activationNativePair) preparedOwner(i int, lifetime context.Context, suppressFirst bool, before ...func()) (*contextControlOwner, *atomic.Bool) {
	f.t.Helper()
	c := f.cores[i]
	s := c.directLAN
	c.op.Lock()
	s.mu.Lock()
	model, err := s.endpointModelLocked(time.Now(), false)
	var in contextInputs
	if err == nil {
		in, err = s.contextPreparationInputsLocked(*model, s.state.Peers[0].Key, time.Now().Add(45*time.Second).UTC().Format(time.RFC3339Nano))
	}
	s.mu.Unlock()
	if err == nil {
		var a contextAdmission
		a, err = c.reviewContextLocked(in)
		if err == nil {
			_, err = c.applyContextLocked(lifetime, a, contextTranscript{})
		}
	}
	if err != nil {
		c.op.Unlock()
		f.t.Fatal("reviewed preparation failed")
	}
	s.mu.Lock()
	cfg, digest, err := s.contextConfigLocked(time.Now(), directRuntimeResources(c.capacityPolicy()).Invitations)
	s.mu.Unlock()
	if err != nil {
		c.op.Unlock()
		f.t.Fatal("context projection failed")
	}
	ctx, cancel := context.WithCancel(lifetime)
	o := &contextControlOwner{store: s, process: c.lanStartNonce, configuration: digest, ctx: ctx, cancel: cancel, started: make(chan struct{}), operations: make(map[*directlan.ContextAttempt]*contextExchangeOperation)}
	dropped := &atomic.Bool{}
	cfg.Completion = func(ctx context.Context, a *directlan.ContextAttempt, v directlan.VerifiedContextExchange) (directlan.ContextResponse, error) {
		for _, gate := range before {
			gate()
		}
		response, err := c.completeContextExchange(ctx, o, a, v)
		if err == nil && suppressFirst {
			if _, ok := response.Reply.(endpointmeta.PrepareReply); ok && dropped.CompareAndSwap(false, true) {
				return directlan.ContextResponse{}, errors.New("synthetic lost preparation response")
			}
		}
		return response, err
	}
	o.node, err = directlan.NewContextControl(cfg)
	if err != nil {
		cancel()
		c.op.Unlock()
		f.t.Fatal("context constructor failed")
	}
	c.mu.Lock()
	c.contextControl = o
	c.mu.Unlock()
	c.op.Unlock()
	f.release(i)
	err = o.node.Start(ctx)
	close(o.started)
	if err != nil {
		f.t.Fatal("fixed context Start failed")
	}
	return o, dropped
}
func (f *activationNativePair) drive(i int, ctx context.Context) *contextUpgradeJob {
	c := f.cores[i]
	deadline, _ := ctx.Deadline()
	run, cancel := context.WithCancel(ctx)
	job := &contextUpgradeJob{intent: UpgradeIntent{PeerID: f.cores[1-i].directLAN.copy().Identity.PublicKey(), Deadline: deadline.UTC().Format(time.RFC3339Nano)}, policy: c.upgradePolicyDigest(), cancel: cancel, done: make(chan struct{})}
	c.mu.Lock()
	c.contextUpgrade = job
	c.mu.Unlock()
	go func() {
		defer close(job.done)
		defer cancel()
		if err := c.driveContextUpgrade(run, job); err != nil {
			c.upgradePhase(job, "failed", err)
		}
	}()
	return job
}
func TestActivationNativeLostFirstPrepareReplyRecovery(t *testing.T) {
	f := newActivationNativePair(t)
	ctx, cancel := context.WithTimeout(f.ctx, 45*time.Second)
	defer cancel()
	initiator, _ := f.preparedOwner(0, ctx, false)
	responder, dropped := f.preparedOwner(1, ctx, true)
	left, right := f.cores[0], f.cores[1]
	right.op.Lock()
	_, err := right.armContextInboundLocked(ctx, responder, left.directLAN.copy().Identity.PublicKey(), directlan.ContextPrepare)
	right.op.Unlock()
	if err != nil {
		t.Fatal("initial prepare arm failed")
	}
	left.op.Lock()
	out, err := left.prepareContextOutboundLocked(ctx, initiator, right.directLAN.copy().Identity.PublicKey(), directlan.ContextPrepare)
	left.op.Unlock()
	if err != nil {
		t.Fatal("initial outbound prepare failed")
	}
	if left.exchangeContext(ctx, out) == nil || !dropped.Load() {
		t.Fatal("first genuine prepare response was not lost")
	}
	l, r := left.directLAN.copy(), right.directLAN.copy()
	if contextSavedPair(l.Metadata.Peers[0]) != nil || contextSavedPair(r.Metadata.Peers[0]) == nil || l.Metadata.Peers[0].ContextConfirmed || r.Metadata.Peers[0].ContextConfirmed {
		t.Fatal("lost reply did not produce exact asymmetric durable state")
	}
	before := *contextSavedPair(r.Metadata.Peers[0])
	oldLeft, oldRight := left.directLAN.contextEpoch, right.directLAN.contextEpoch
	a, b := f.drive(0, ctx), f.drive(1, ctx)
	f.await(a)
	f.await(b)
	f.assertOrdinary(0, a)
	f.assertOrdinary(1, b)
	if oldLeft.Valid() || oldRight.Valid() {
		t.Fatal("handoff retained old response epoch")
	}
	if !reflect.DeepEqual(before, *right.directLAN.copy().Metadata.Peers[0].PairContext) {
		t.Fatal("retry changed saved binding/nonces")
	}
}

func (f *activationNativePair) exchange(i int, ctx context.Context, owners [2]*contextControlOwner, operation directlan.ContextOperation) error {
	remote, local := f.cores[1-i], f.cores[i]
	remote.op.Lock()
	_, err := remote.armContextInboundLocked(ctx, owners[1-i], local.directLAN.copy().Identity.PublicKey(), operation)
	remote.op.Unlock()
	if err != nil {
		return err
	}
	local.op.Lock()
	out, err := local.prepareContextOutboundLocked(ctx, owners[i], remote.directLAN.copy().Identity.PublicKey(), operation)
	local.op.Unlock()
	if err != nil {
		return err
	}
	return local.exchangeContext(ctx, out)
}
func TestActivationNativeOrdinaryCompletionHandoff(t *testing.T) {
	f := newActivationNativePair(t)
	ctx, cancel := context.WithTimeout(f.ctx, 45*time.Second)
	defer cancel()
	a, _ := f.preparedOwner(0, ctx, false)
	b, _ := f.preparedOwner(1, ctx, false)
	owners := [2]*contextControlOwner{a, b}
	if err := f.exchange(0, ctx, owners, directlan.ContextPrepare); err != nil {
		t.Fatal("genuine prepare failed")
	}
	if err := f.exchange(0, ctx, owners, directlan.ContextCommit); err != nil {
		t.Fatal("genuine commit failed")
	}
	l, r := f.cores[0].directLAN.copy(), f.cores[1].directLAN.copy()
	if !l.Metadata.Peers[0].ContextConfirmed || r.Metadata.Peers[0].ContextConfirmed || r.Metadata.Peers[0].PairContext == nil {
		t.Fatal("asymmetric confirmation boundary missing")
	}
	left := f.drive(0, ctx)
	f.await(left)
	f.assertOrdinary(0, left)
	// Only now start the second driver: no context responder remains on the
	// first endpoint. Its sole completion source is the genuine ordinary backend.
	right := f.drive(1, ctx)
	f.await(right)
	f.assertOrdinary(1, right)
	if a.ctx.Err() == nil || b.ctx.Err() == nil {
		t.Fatal("old context owners were not stopped")
	}
}

func TestActivationNativeFinalPublicationDeniesEndedLifetime(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newActivationNativePair(t)
			initiator, _ := f.preparedOwner(0, f.ctx, false)
			lifetime, cancel := context.WithCancel(f.ctx)
			if mode == "deadline" {
				cancel()
				lifetime, cancel = context.WithTimeout(f.ctx, 3*time.Second)
			}
			defer cancel()
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			responder, _ := f.preparedOwner(1, lifetime, false, func() {
				close(reached)
				select {
				case <-release:
				case <-f.ctx.Done():
				}
			})
			right, left := f.cores[1], f.cores[0]
			right.op.Lock()
			_, err := right.armContextInboundLocked(f.ctx, responder, left.directLAN.copy().Identity.PublicKey(), directlan.ContextPrepare)
			right.op.Unlock()
			if err != nil {
				t.Fatal("prepare arm failed")
			}
			// Snapshot AFTER arm-time publication, BEFORE authenticated callback save.
			before := right.directLAN.copy()
			right.directLAN.mu.Lock()
			receipt := right.directLAN.contextPublication
			right.directLAN.mu.Unlock()
			left.op.Lock()
			out, err := left.prepareContextOutboundLocked(f.ctx, initiator, right.directLAN.copy().Identity.PublicKey(), directlan.ContextPrepare)
			left.op.Unlock()
			if err != nil {
				t.Fatal("prepare outbound failed")
			}
			done := make(chan error, 1)
			go func() { done <- left.exchangeContext(f.ctx, out) }()
			select {
			case <-reached:
			case <-f.ctx.Done():
				t.Fatal("authenticated request did not reach completion barrier")
			}
			if mode == "cancel" {
				cancel()
			} else {
				select {
				case <-lifetime.Done():
				case <-f.ctx.Done():
					t.Fatal("workflow deadline was not observed")
				}
			}
			unblock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("ended lifetime released successful reply")
				}
			case <-f.ctx.Done():
				t.Fatal("rejected exchange did not return")
			}
			// Join the real callback before inspecting post-denial state.
			right.op.Lock()
			err = right.stopContextControlLocked()
			right.op.Unlock()
			if err != nil {
				t.Fatal("denied owner did not join")
			}
			after := right.directLAN.copy()
			right.directLAN.mu.Lock()
			sameReceipt := receipt == right.directLAN.contextPublication
			right.directLAN.mu.Unlock()
			if !reflect.DeepEqual(before, after) || !sameReceipt {
				t.Fatal("ended lifetime published authenticated request")
			}
		})
	}
}
