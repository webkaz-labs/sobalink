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
