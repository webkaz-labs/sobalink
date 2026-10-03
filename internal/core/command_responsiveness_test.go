package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func testCommand(t *testing.T, id, name string, payload any) webui.Command {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return webui.Command{RequestID: id, Name: name, Payload: raw}
}

func awaitCommandError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("control operation remained blocked")
		return nil
	}
}

func TestQueuedCancelledCommandDoesNotChangeProfile(t *testing.T) {
	c := offlineDefinitionCore(t)
	c.op.Lock()
	locked := true
	defer func() {
		if locked {
			c.op.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := testCommand(t, "cancelled-edit", "settings.update", map[string]string{"theme": "dark"})
	done := make(chan error, 1)
	go func() { _, err := c.Command(ctx, cmd); done <- err }()
	// Observe admission before cancelling: this exercises cancellation while
	// queued behind a mutation, rather than only a pre-cancelled request.
	until := time.Now().Add(time.Second)
	for {
		c.requestMu.Lock()
		pending := c.inflightRequests[cmd.RequestID] != nil
		c.requestMu.Unlock()
		if pending {
			break
		}
		if time.Now().After(until) {
			t.Fatal("request did not enter the mutation queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	c.op.Unlock()
	locked = false
	if err := awaitCommandError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued command error = %v", err)
	}
	if c.profileCopy().Settings.Theme != "system" {
		t.Fatal("cancelled queued mutation changed the profile")
	}
}

type blockedSourcePlan struct {
	sourcePlan
	entered chan struct{}
	release chan struct{}
	err     error
}

func (p *blockedSourcePlan) Hash(ctx context.Context) (transfer.Manifest, []transfer.Source, error) {
	close(p.entered)
	select {
	case <-ctx.Done():
		return transfer.Manifest{}, nil, ctx.Err()
	case <-p.release:
		return transfer.Manifest{}, nil, p.err
	}
}

func blockCommandStaging(t *testing.T, c *Core) (webui.Command, *blockedSourcePlan, *atomic.Int32) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "selection.txt")
	if err := os.WriteFile(file, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := &blockedSourcePlan{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("test staging stopped")}
	count := &atomic.Int32{}
	c.mu.Lock()
	c.planSources = func(ctx context.Context, id string, paths []string, limits transfer.Limits) (sourcePlan, error) {
		count.Add(1)
		plan, err := transfer.PlanSources(ctx, id, paths, limits)
		if err != nil {
			return nil, err
		}
		blocked.sourcePlan = plan
		return blocked, nil
	}
	c.mu.Unlock()
	cmd := testCommand(t, "same-transfer", "transfer.send", map[string]any{"peerId": "peer-b", "paths": []string{file}})
	return cmd, blocked, count
}

// Observing Done proves that the duplicate entered the cancellable wait rather
// than being rejected by the pre-cancelled-request check at Command entry.
type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestTransferCommandDeduplicatesWithoutHoldingMutationLock(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	cmd, stage, count := blockCommandStaging(t, p.a)
	first := make(chan error, 1)
	go func() { _, err := p.a.Command(context.Background(), cmd); first <- err }()
	<-stage.entered

	duplicateBase, cancelDuplicate := context.WithCancel(context.Background())
	duplicateCtx := &observedWaitContext{Context: duplicateBase, waiting: make(chan struct{})}
	duplicate := make(chan error, 1)
	go func() { _, err := p.a.Command(duplicateCtx, cmd); duplicate <- err }()
	<-duplicateCtx.waiting
	cancelDuplicate()
	if err := awaitCommandError(t, duplicate); !errors.Is(err, context.Canceled) {
		t.Fatalf("duplicate caller cancellation = %v", err)
	}
	conflict := cmd
	conflict.Payload = json.RawMessage(`{"peerId":"peer-b","paths":["different"]}`)
	if _, err := p.a.Command(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("pending request ID accepted different content: %v", err)
	}

	control := make(chan error, 1)
	go func() {
		_, err := command(p.a, "independent-edit", "settings.update", map[string]string{"theme": "dark"})
		control <- err
	}()
	if err := awaitCommandError(t, control); err != nil {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { _, err := p.a.Command(context.Background(), cmd); second <- err }()
	close(stage.release)
	for _, result := range []<-chan error{first, second} {
		if err := awaitCommandError(t, result); !errors.Is(err, stage.err) {
			t.Fatalf("duplicate did not receive original result: %v", err)
		}
	}
	if count.Load() != 1 {
		t.Fatalf("duplicate transfer staged %d times", count.Load())
	}
}

func TestControlCancellationInterruptsCommandStaging(t *testing.T) {
	for _, action := range []string{"revoke", "pause", "stop", "close"} {
		t.Run(action, func(t *testing.T) {
			p := newCorePair(t)
			trustPair(t, p)
			cmd, stage, _ := blockCommandStaging(t, p.a)
			staging := make(chan error, 1)
			go func() { _, err := p.a.Command(context.Background(), cmd); staging <- err }()
			<-stage.entered
			duplicateCtx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
			duplicate := make(chan error, 1)
			go func() { _, err := p.a.Command(duplicateCtx, cmd); duplicate <- err }()
			<-duplicateCtx.waiting
			control := make(chan error, 1)
			go func() {
				var err error
				switch action {
				case "revoke":
					_, err = command(p.a, "revoke", "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
				case "pause":
					_, err = command(p.a, "pause", "peer.autosave", map[string]any{"peerId": "peer-b", "paused": true})
				case "stop":
					_, err = command(p.a, "stop", "application.stop", map[string]any{})
				case "close":
					err = p.a.Close()
				}
				control <- err
			}()
			if err := awaitCommandError(t, control); err != nil {
				t.Fatal(err)
			}
			for _, result := range []<-chan error{staging, duplicate} {
				if err := awaitCommandError(t, result); !errors.Is(err, context.Canceled) {
					t.Fatalf("original or duplicate staging result after %s: %v", action, err)
				}
			}
			p.a.mu.RLock()
			remaining := len(p.a.outgoing)
			p.a.mu.RUnlock()
			if remaining != 0 {
				t.Fatal("cancelled staging retained a reservation")
			}
		})
	}
}

func TestOutgoingAdmissionRechecksApprovalAfterPlanning(t *testing.T) {
	for _, change := range []string{"revoke", "generation", "network", "pause"} {
		t.Run(change, func(t *testing.T) {
			c := outgoingCapacityCore()
			batch := outgoingForCapacity("selected", 1)
			switch change {
			case "revoke":
				c.profile.Peers = nil
			case "generation":
				c.profile.Peers[0].Generation++
			case "network":
				c.profile.Settings.Network = "lan"
			case "pause":
				c.profile.Peers[0].Paused = true
			}
			if _, err := c.reserveOutgoing(context.Background(), batch, transferLimits()); err == nil {
				t.Fatalf("%s during planning allowed staging admission", change)
			}
			if len(c.outgoing) != 0 || batch.reserved != 0 {
				t.Fatal("rejected approval consumed staging capacity")
			}
		})
	}
}

func TestRevocationDuringPlanningRejectsCommandStaging(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	file := filepath.Join(t.TempDir(), "selection.txt")
	if err := os.WriteFile(file, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	planned, release := make(chan struct{}), make(chan struct{})
	p.a.mu.Lock()
	p.a.planSources = func(ctx context.Context, id string, paths []string, limits transfer.Limits) (sourcePlan, error) {
		plan, err := transfer.PlanSources(ctx, id, paths, limits)
		close(planned)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return plan, err
		}
	}
	p.a.mu.Unlock()
	cmd := testCommand(t, "planning-transfer", "transfer.send", map[string]any{"peerId": "peer-b", "paths": []string{file}})
	done := make(chan error, 1)
	go func() { _, err := p.a.Command(context.Background(), cmd); done <- err }()
	<-planned
	mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
	close(release)
	if err := awaitCommandError(t, done); err == nil || !strings.Contains(err.Error(), "permission changed") {
		t.Fatalf("revocation while planning did not block admission: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.a.dir, "outgoing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked selection created a staging directory: %v", err)
	}
}

type heartbeatBlockedNode struct {
	NetworkBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (n *heartbeatBlockedNode) State(ctx context.Context) (identity.State, error) {
	n.once.Do(func() { close(n.entered) })
	select {
	case <-ctx.Done():
		return identity.State{}, ctx.Err()
	case <-n.release:
		return n.NetworkBackend.State(ctx)
	}
}

func TestLeaseRenewalDuringBlockedNetworkMaintenance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newCorePair(t)
		spec := saveDefinitionFixture(t, p.b, definitionFixture("leased", "tcp", "8080"))
		selection := mustCommand(t, p.b, "service.selection", map[string]any{"ids": []string{spec.ID}}).(map[string]any)
		mustCommand(t, p.b, "services.start", map[string]any{"ids": []string{spec.ID}, "expectedRevision": selection["revision"], "owner": "task-owner", "leaseSeconds": 30})
		node := &heartbeatBlockedNode{NetworkBackend: p.nb, entered: make(chan struct{}), release: make(chan struct{})}
		p.b.mu.Lock()
		active := p.b.active[spec.ID]
		before := *active.leaseExpires.Load()
		p.b.node = node
		p.b.mu.Unlock()
		defer close(node.release)
		<-node.entered // The maintenance ticker now owns c.op inside State.
		cmd := testCommand(t, "lease-heartbeat", "services.renew", map[string]any{"ids": []string{spec.ID}, "owner": "task-owner", "leaseSeconds": 30})
		renewed := make(chan error, 1)
		go func() { _, err := p.b.Command(context.Background(), cmd); renewed <- err }()
		if err := awaitCommandError(t, renewed); err != nil {
			t.Fatal(err)
		}
		after := *active.leaseExpires.Load()
		if !after.After(before) {
			t.Fatal("lease was not extended during blocked maintenance")
		}
		time.Sleep(time.Second)
		if _, err := p.b.Command(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		if !active.leaseExpires.Load().Equal(after) {
			t.Fatal("duplicate heartbeat extended its lease again")
		}
	})
}

func TestLeaseRenewalIsAtomicAndCannotRevivePermissions(t *testing.T) {
	for _, invalid := range []string{"expired", "stopped", "owner", "duration", "permission-expired"} {
		t.Run(invalid, func(t *testing.T) {
			c := outgoingCapacityCore()
			c.active = map[string]*activeService{}
			initial := time.Now().Add(10 * time.Second)
			for _, id := range []string{"first", "second"} {
				spec := ServiceSpec{ID: id, Direction: "forward", Lifetime: "until-stopped"}
				c.profile.Services = append(c.profile.Services, spec)
				a := &activeService{spec: spec, ctx: context.Background(), owner: "task-owner", leaseSeconds: 30}
				a.leaseExpires.Store(&initial)
				c.active[id] = a
			}
			second := c.active["second"]
			switch invalid {
			case "expired":
				expired := time.Now().Add(-time.Second)
				second.leaseExpires.Store(&expired)
			case "stopped":
				delete(c.active, "second")
			case "owner":
				second.owner = "other-owner"
			case "duration":
				second.leaseSeconds = 60
			case "permission-expired":
				second.spec.Lifetime = "finite"
				second.expires = time.Now().Add(-time.Second)
			}
			_, err := command(c, randomID(), "services.renew", map[string]any{"ids": []string{"first", "second"}, "owner": "task-owner", "leaseSeconds": 30})
			if err == nil {
				t.Fatalf("renewal accepted %s permission", invalid)
			}
			if !c.active["first"].leaseExpires.Load().Equal(initial) {
				t.Fatal("failed selection partially renewed a lease")
			}
		})
	}
}
