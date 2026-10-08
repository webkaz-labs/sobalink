package core

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"testing"
	"time"
)

// Pure input/phase tests deliberately create no Core, transport or publisher.
// Real process/TLS integration remains a separately authorized test stage.
func TestUpgradeIntentRejectsEvidenceAndUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"peerId":"fixture","deadline":"2026-01-01T00:01:00Z","contextConfirmed":true}`,
		`{"peerId":"fixture","receipt":{}}`,
		`{"peerId":"fixture","transcript":{}}`,
		`{"peerId":"fixture"} {}`,
	} {
		if _, err := decodeUpgradeIntent(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted non-intent input: %s", raw)
		}
	}
}
func TestUpgradeDeadlineNeverRefreshes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	original := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	for _, elapsed := range []time.Duration{0, time.Minute, 4 * time.Minute} {
		got, err := upgradeDeadline(original, now.Add(elapsed))
		if err != nil || !got.Equal(now.Add(5*time.Minute)) {
			t.Fatalf("changed absolute deadline: %v %v", got, err)
		}
	}
	for _, raw := range []string{now.Format(time.RFC3339Nano), now.Add(6 * time.Minute).Format(time.RFC3339Nano), "2026-01-01T00:01:00+00:00"} {
		if _, err := upgradeDeadline(raw, now); err == nil {
			t.Fatalf("accepted invalid deadline %s", raw)
		}
	}
	if _, err := upgradeDeadline(original, now.Add(5*time.Minute)); err == nil {
		t.Fatal("expired intent was renewed")
	}
}

func TestUpgradeExchangeRolesRecoverWithoutSameOperationCollision(t *testing.T) {
	for _, phase := range []string{"reviewed", "prepared", "committed"} {
		for _, initiator := range []bool{true, false} {
			local, remote := "a", "z"
			if !initiator {
				local, remote = remote, local
			}
			r := endpointmeta.PeerRecord{Peer: endpointmeta.PeerWire{Key: remote}}
			if phase != "reviewed" {
				r.UpgradePending = &endpointmeta.UpgradePending{Context: &endpointmeta.PairContext{}}
			}
			if phase == "committed" {
				r.PairContext = r.UpgradePending.Context
			}
			op, outbound, inbound := upgradeExchangePlan(local, r)
			if inbound != 0 && inbound == op {
				t.Fatal("same-operation directions cancel each other")
			}
			if phase == "reviewed" && (op != directlan.ContextPrepare || outbound != initiator || inbound != 0) {
				t.Fatal("prepare role changed")
			}
			if phase == "prepared" && (!outbound || initiator && op != directlan.ContextCommit || !initiator && op != directlan.ContextStatus) {
				t.Fatal("commit role changed")
			}
			if phase == "committed" && (!outbound || inbound == 0) {
				t.Fatal("lost-reply recovery has a waiting cycle")
			}
		}
	}
}

// Inert final-gate coverage: no Core or transport owner is constructed. The
// context chain is the same workflow -> context-owner chain used by Start.
func TestUpgradeCancellationSynchronouslyDeniesInboundPublication(t *testing.T) {
	workflow, cancel := context.WithCancel(context.Background())
	ownerContext, release := context.WithCancel(workflow)
	defer release()
	owner := &contextControlOwner{ctx: ownerContext}
	live := &contextSaveLiveness{ctx: context.Background(), owner: owner}
	if err := live.err(); err != nil {
		t.Fatal(err)
	}
	cancel()
	// No AfterFunc, requestClose, Node cleanup or goroutine scheduling is needed
	// for the already-armed inbound final publication gate to reject the job.
	if err := live.err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled workflow still admitted publication: %v", err)
	}
	if owner.stopping.Load() {
		t.Fatal("test accidentally depended on eventual owner close")
	}
}

func TestUpgradeExpiredLifetimeDeniesInboundPublication(t *testing.T) {
	workflow, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	ownerContext, release := context.WithCancel(workflow)
	defer release()
	live := &contextSaveLiveness{ctx: context.Background(), owner: &contextControlOwner{ctx: ownerContext}}
	if err := live.err(); err == nil {
		t.Fatal("expired workflow admitted inbound publication")
	}
}

func TestUpgradeSupervisorCancellationRequiresExactIntent(t *testing.T) {
	intent := UpgradeIntent{PeerID: "synthetic-peer", Deadline: "2026-01-01T00:01:00Z", ExpectedRevision: "synthetic-review"}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Core{contextUpgrade: &contextUpgradeJob{intent: intent, cancel: cancel}}
	for _, bad := range []UpgradeIntent{{PeerID: "another-peer", Deadline: intent.Deadline, ExpectedRevision: intent.ExpectedRevision}, {PeerID: intent.PeerID, Deadline: "another-deadline", ExpectedRevision: intent.ExpectedRevision}, {PeerID: intent.PeerID, Deadline: intent.Deadline, ExpectedRevision: "another-review"}} {
		if _, err := c.CancelManagedUpgrade(context.Background(), bad); err == nil || lifetime.Err() != nil {
			t.Fatal("unrelated supervisor cancelled the workflow")
		}
	}
	if _, err := c.CancelManagedUpgrade(context.Background(), intent); err != nil || lifetime.Err() == nil {
		t.Fatal("exact supervisor cancellation not observed", err)
	}
}

// No transport: deterministically prove that a pending outbound operation does
// not prevent repeated inbound maintenance, and terminal signals stop it.
func TestUpgradePendingOutboundMaintainsInboundService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	calls := 0
	err := waitUpgradeExchange(ctx, done, func() error {
		calls++
		if calls == 2 {
			close(done)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("pending outbound starved inbound maintenance: calls=%d error=%v", calls, err)
	}
}

func TestUpgradePendingOutboundStopsOnCancellationAndRefreshFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "failure", "already-done"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan struct{})
			if mode == "already-done" {
				close(done)
			}
			failure := errors.New("synthetic inbound maintenance failure")
			calls := 0
			err := waitUpgradeExchange(ctx, done, func() error {
				calls++
				if mode == "failure" {
					return failure
				}
				cancel()
				return nil
			})
			switch mode {
			case "cancel":
				if calls != 1 || !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation did not stop maintenance", calls, err)
				}
			case "failure":
				if calls != 1 || !errors.Is(err, failure) {
					t.Fatal("maintenance error did not stop wait", calls, err)
				}
			case "already-done":
				if calls != 0 || err != nil {
					t.Fatal("completed exchange refreshed an arm", calls, err)
				}
			}
		})
	}
}

func TestUpgradeInboundRefreshRejectsChangedPolicyBeforeArming(t *testing.T) {
	c := &Core{ctx: context.Background()}
	policy := c.upgradePolicyDigest()
	c.profile.Settings.Hostname = "synthetic-policy-change"
	// Missing owner/outbound are intentional: the immutable policy must reject
	// before inspecting an owner, performing store work or creating any arm.
	if err := c.refreshUpgradeInbound(context.Background(), nil, nil, policy); !errors.Is(err, endpointmeta.ErrReview) {
		t.Fatal("changed policy reached inbound owner admission", err)
	}
}

func TestUpgradeInboundRefreshDoesNotQueueBehindCompletion(t *testing.T) {
	c := &Core{ctx: context.Background()}
	c.op.Lock()
	done := make(chan error, 1)
	go func() { done <- c.refreshUpgradeInbound(context.Background(), nil, nil, "") }()
	select {
	case err := <-done:
		c.op.Unlock()
		if err != nil {
			t.Fatal("contended refresh inspected owner instead of yielding", err)
		}
	case <-time.After(time.Second):
		c.op.Unlock()
		<-done
		t.Fatal("inbound maintenance queued behind a completion owner")
	}
}
