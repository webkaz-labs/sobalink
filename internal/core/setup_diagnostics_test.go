package core

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

// Diagnostics retain only fixed labels, never an error, invitation, or identity.
// Four slots bound retention independently of retries and callback concurrency.
type setupTestDiagnostics struct {
	mu     sync.Mutex
	causes [4]string
}

func setupTestStageIndex(stage string) int {
	switch stage {
	case "construct":
		return 0
	case "start":
		return 1
	case "issue_invitation":
		return 2
	case "pair_invitation":
		return 3
	default:
		return -1
	}
}

func setupTestCause(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, lanlink.ErrRemotePairedLocalSave):
		return "remote_paired_local_save"
	case errors.Is(err, lanlink.ErrPairReplyUncertain):
		return "pair_reply_uncertain"
	case errors.Is(err, config.ErrAtomicRecovery):
		return "atomic_recovery"
	case errors.Is(err, config.ErrAtomicCommitted):
		return "atomic_committed"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	case errors.Is(err, lanlink.ErrInvite):
		return "invalid_invitation"
	case errors.Is(err, lanlink.ErrRelayMismatch):
		return "relay_mismatch"
	case errors.Is(err, lanlink.ErrUntrusted):
		return "untrusted"
	default:
		return "other"
	}
}

func (d *setupTestDiagnostics) record(stage, cause string) {
	i := setupTestStageIndex(stage)
	if i < 0 {
		return
	}
	switch cause {
	case "pending", "ok", "remote_paired_local_save", "pair_reply_uncertain", "atomic_recovery", "atomic_committed", "deadline", "canceled", "permission", "closed", "invalid_invitation", "relay_mismatch", "untrusted", "other":
	default:
		return
	}
	d.mu.Lock()
	d.causes[i] = cause
	d.mu.Unlock()
}

func (d *setupTestDiagnostics) snapshot() [4]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.causes
}

// Wrap the real engine, not the backend, preserving concrete backend identity
// and all its optional interfaces. Each observed call is forwarded exactly once.
type setupTestObservedEngine struct {
	lanEngine
	diagnostics *setupTestDiagnostics
}

func (n *setupTestObservedEngine) IssueSetupInvitation(ctx, consent context.Context, peer lanlink.Peer, name string, expires time.Time) (lanlink.Invitation, error) {
	n.diagnostics.record("issue_invitation", "pending")
	inv, err := n.lanEngine.IssueSetupInvitation(ctx, consent, peer, name, expires)
	n.diagnostics.record("issue_invitation", setupTestCause(err))
	return inv, err
}

func (n *setupTestObservedEngine) PairInvitation(ctx context.Context, inv lanlink.Invitation) error {
	n.diagnostics.record("pair_invitation", "pending")
	err := n.lanEngine.PairInvitation(ctx, inv)
	n.diagnostics.record("pair_invitation", setupTestCause(err))
	return err
}

func logSetupTestSession(t *testing.T, c *Core) {
	t.Helper()
	if !c.mu.TryRLock() {
		t.Log("setup evidence: observation_unavailable")
		return
	}
	s := c.setup
	c.mu.RUnlock()
	if s == nil {
		t.Log("setup evidence: session=false")
		return
	}
	if !s.mu.TryLock() {
		t.Log("setup evidence: observation_unavailable")
		return
	}
	defer s.mu.Unlock()
	t.Logf("setup evidence: session=true commit_started=%t host=%t invitation=%t pairing_attempted=%t paired=%t remote_paired=%t local_connected=%t remote_connected=%t promoted=%t canceled=%t", s.commitStarted, s.plan != nil && s.plan.HostKey == s.local.Key, s.invitation != nil, s.pairingAttempted, s.paired, s.remotePaired, s.localConnected, s.remoteConnected, s.promoted, s.ctx.Err() != nil)
}

func TestSetupTestDiagnosticsRejectUnlistedLabels(t *testing.T) {
	d := new(setupTestDiagnostics)
	for _, untrusted := range []string{"/synthetic/private/file", "token=synthetic", "peer-synthetic", "deadline\nextra", "", "unknown"} {
		d.record(untrusted, "ok")
		d.record("pair_invitation", untrusted)
		if d.snapshot() != [4]string{} {
			t.Fatal("unlisted label retained")
		}
		if setupTestCause(errors.New(untrusted)) != "other" {
			t.Fatal("raw error classified by text")
		}
	}
	cases := []struct {
		err  error
		want string
	}{
		{nil, "ok"}, {lanlink.ErrRemotePairedLocalSave, "remote_paired_local_save"}, {lanlink.ErrPairReplyUncertain, "pair_reply_uncertain"}, {config.ErrAtomicRecovery, "atomic_recovery"}, {config.ErrAtomicCommitted, "atomic_committed"}, {context.DeadlineExceeded, "deadline"}, {context.Canceled, "canceled"}, {os.ErrPermission, "permission"}, {net.ErrClosed, "closed"}, {lanlink.ErrInvite, "invalid_invitation"}, {lanlink.ErrRelayMismatch, "relay_mismatch"}, {lanlink.ErrUntrusted, "untrusted"},
	}
	for _, tc := range cases {
		if setupTestCause(tc.err) != tc.want {
			t.Fatal("incorrect fixed cause")
		}
		if tc.err != nil && setupTestCause(errors.Join(errors.New("synthetic private detail"), tc.err)) != tc.want {
			t.Fatal("wrapped cause lost or raw detail retained")
		}
		d.record("pair_invitation", tc.want)
		if d.snapshot()[3] != tc.want {
			t.Fatal("known cause rejected")
		}
	}
}

type setupTestForwardEngine struct {
	lanEngine
	issue func(context.Context, context.Context, lanlink.Peer, string, time.Time) (lanlink.Invitation, error)
	pair  func(context.Context, lanlink.Invitation) error
}

func (n *setupTestForwardEngine) IssueSetupInvitation(ctx, consent context.Context, peer lanlink.Peer, name string, expires time.Time) (lanlink.Invitation, error) {
	return n.issue(ctx, consent, peer, name, expires)
}
func (n *setupTestForwardEngine) PairInvitation(ctx context.Context, inv lanlink.Invitation) error {
	return n.pair(ctx, inv)
}

func TestSetupTestObservedEngineForwardsExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consent, stop := context.WithCancel(context.Background())
	defer stop()
	peer, expires := lanlink.Peer{Key: "synthetic-key", Name: "synthetic-peer"}, time.Now()
	inv := lanlink.Invitation{Token: "synthetic-token"}
	for _, resultErr := range []error{nil, errors.New("synthetic private error")} {
		issueCalls, pairCalls := 0, 0
		d := new(setupTestDiagnostics)
		engine := &setupTestForwardEngine{}
		engine.issue = func(gotCtx, gotConsent context.Context, gotPeer lanlink.Peer, name string, gotExpiry time.Time) (lanlink.Invitation, error) {
			issueCalls++
			if gotCtx != ctx || gotConsent != consent || gotPeer != peer || name != "synthetic-name" || gotExpiry != expires {
				t.Fatal("issuance arguments changed")
			}
			if d.snapshot()[2] != "pending" {
				t.Fatal("missing issuance entry observation")
			}
			return inv, resultErr
		}
		engine.pair = func(gotCtx context.Context, gotInv lanlink.Invitation) error {
			pairCalls++
			if gotCtx != ctx || gotInv != inv {
				t.Fatal("pairing arguments changed")
			}
			if d.snapshot()[3] != "pending" {
				t.Fatal("missing pairing entry observation")
			}
			return resultErr
		}
		observed := &setupTestObservedEngine{engine, d}
		got, err := observed.IssueSetupInvitation(ctx, consent, peer, "synthetic-name", expires)
		if got != inv || err != resultErr {
			t.Fatal("issuance result changed")
		}
		if observed.PairInvitation(ctx, inv) != resultErr {
			t.Fatal("pairing result changed")
		}
		if issueCalls != 1 || pairCalls != 1 {
			t.Fatal("unexpected forwarding count")
		}
		if d.snapshot()[2] != setupTestCause(resultErr) || d.snapshot()[3] != setupTestCause(resultErr) {
			t.Fatal("missing completion observations")
		}
	}
}

func TestSetupTestDiagnosticsBoundedConcurrentObservations(t *testing.T) {
	d := new(setupTestDiagnostics)
	var workers sync.WaitGroup
	for _, stage := range []string{"construct", "start", "issue_invitation", "pair_invitation"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				d.record(stage, "pending")
				_ = d.snapshot()
				d.record(stage, "ok")
			}
		}()
	}
	workers.Wait()
	if d.snapshot() != [4]string{"ok", "ok", "ok", "ok"} {
		t.Fatal("bounded observations lost completed stage")
	}
}

func TestSetupTestSessionObservationDoesNotWaitForLocks(t *testing.T) {
	c := &Core{}
	c.mu.Lock()
	logSetupTestSession(t, c)
	c.mu.Unlock()
	c.setup = &setupSession{ctx: context.Background()}
	c.setup.mu.Lock()
	logSetupTestSession(t, c)
	c.setup.mu.Unlock()
	logSetupTestSession(t, c)
}
