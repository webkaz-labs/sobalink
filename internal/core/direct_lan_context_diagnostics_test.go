package core

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

func diagnosticCoreOwner() (*Core, *contextControlOwner) {
	s := &directLANStore{}
	o := &contextControlOwner{node: &directlan.Node{}, store: s, ctx: context.Background(), process: "synthetic-process", operations: make(map[*directlan.ContextAttempt]*contextExchangeOperation)}
	c := &Core{ctx: context.Background(), directLAN: s, lanStartNonce: o.process, contextControl: o}
	return c, o
}

func TestContextCompletionDiagnosticOwnerReasons(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Core, **contextControlOwner)
	}{
		{"owner-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; *op = nil }},
		{"owner-node-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; o.node = nil }},
		{"owner-store-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; o.store = nil }},
		{"owner-context-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; o.ctx = nil }},
		{"owner-process-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; o.process = "" }},
		{"owner-identity", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.contextControl = &contextControlOwner{} }},
		{"store-identity", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.directLAN = &directLANStore{} }},
		{"process-identity", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.lanStartNonce = "synthetic-other" }},
		{"ordinary-node", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.node = &directLANBackend{} }},
		{"network-attempted", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.attemptedNetwork = "synthetic-network" }},
		{"core-closing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.closing = true }},
		{"core-context-missing", func(c *Core, op **contextControlOwner) { o := *op; _ = o; c.ctx = nil }},
		{"core-context-ended", func(c *Core, op **contextControlOwner) {
			o := *op
			_ = o
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			c.ctx = ctx
		}},
		{"owner-context-ended", func(c *Core, op **contextControlOwner) {
			o := *op
			_ = o
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			o.ctx = ctx
		}},
		{"owner-stopping", func(c *Core, op **contextControlOwner) { o := *op; _ = o; o.stopping.Store(true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, observed := range []bool{false, true} {
				c, o := diagnosticCoreOwner()
				tc.change(c, &o)
				var result contextCompletionObservation
				var observation *contextCompletionObservation
				if observed {
					observation = &result
				}
				response, err := c.completeContextExchangeObserved(context.Background(), o, &directlan.ContextAttempt{}, directlan.VerifiedContextExchange{}, observation)
				if !errors.Is(err, directlan.ErrUnavailable) || response.Reply != nil || response.Admit != nil {
					t.Fatal("owner rejection changed")
				}
				if observed && (result.Site != "owner" || result.Owner != contextCompletionSite(tc.name)) {
					t.Fatalf("observation=%+v", result)
				}
				if !observed && result != (contextCompletionObservation{}) {
					t.Fatal("nil observer wrote")
				}
			}
		})
	}
	c, o := diagnosticCoreOwner()
	var result contextCompletionObservation
	if c.currentContextOwnerObservedLocked(o, &result) != nil || result != (contextCompletionObservation{}) || c.currentContextOwnerLocked(o) != nil {
		t.Fatal("positive owner observation changed admission")
	}
}

func TestContextCompletionDiagnosticEarlyReturns(t *testing.T) {
	for _, name := range []string{"attempt-missing", "association", "retained-response", "claim"} {
		t.Run(name, func(t *testing.T) {
			for _, observed := range []bool{false, true} {
				c, o := diagnosticCoreOwner()
				attempt := &directlan.ContextAttempt{}
				operation := &contextExchangeOperation{owner: o, attempt: attempt}
				o.operations[attempt] = operation
				want := endpointmeta.ErrReview
				switch name {
				case "attempt-missing":
					attempt = nil
					want = endpointmeta.ErrInvalid
				case "association":
					operation.owner = nil
				case "retained-response":
					operation.responseEpoch = directlan.NewContextEpoch()
				case "claim":
					want = directlan.ErrUntrusted
				}
				var result contextCompletionObservation
				var observation *contextCompletionObservation
				if observed {
					observation = &result
				}
				response, err := c.completeContextExchangeObserved(context.Background(), o, attempt, directlan.VerifiedContextExchange{}, observation)
				if !errors.Is(err, want) || response.Reply != nil || response.Admit != nil {
					t.Fatal("completion result changed")
				}
				if observed && result.Site != contextCompletionSite(name) {
					t.Fatalf("site=%s want=%s", result.Site, name)
				}
				if observed && name == "claim" && result.Claim.Site != "proof-empty" {
					t.Fatal("claim reason lost")
				}
				if (name == "claim" || name == "retained-response") && len(o.operations) != 0 {
					t.Fatal("consumed operation retained")
				}
			}
		})
	}
}

func TestContextCompletionDiagnosticCoreContentionAndIsolation(t *testing.T) {
	for _, marker := range []uint64{0, 1, 2} {
		for _, observed := range []bool{false, true} {
			c, o := diagnosticCoreOwner()
			var sequence atomic.Uint64
			sequence.Store(marker)
			o.diagnosticRefresh = &sequence
			var result contextCompletionObservation
			var observation *contextCompletionObservation
			if observed {
				observation = &result
			}
			before := contextCompletionObservation{Site: "inbound-success"}
			c.op.Lock()
			done := make(chan error, 1)
			go func() {
				_, err := c.completeContextExchangeObserved(context.Background(), o, &directlan.ContextAttempt{}, directlan.VerifiedContextExchange{}, observation)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, directlan.ErrUnavailable) {
					t.Error("contention result changed")
				}
			case <-time.After(time.Second):
				c.op.Unlock()
				<-done
				t.Fatal("observation waited for Core lock")
			}
			c.op.Unlock()
			if observed && (result.Site != "core-lock" || result.RefreshOverlap != (marker%2 == 1)) {
				t.Fatalf("wrong lock observation: %+v", result)
			}
			if before.Site != "inbound-success" || sequence.Load() != marker {
				t.Fatal("observation changed another result or marker")
			}
		}
	}
}

func TestContextCompletionDiagnosticRefreshMarkerLifetime(t *testing.T) {
	c, o := diagnosticCoreOwner()
	var sequence atomic.Uint64
	o.diagnosticRefresh = &sequence
	// Hold the existing profile read boundary, after refresh acquires Core.op.
	c.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- c.refreshUpgradeInbound(context.Background(), o, nil, "synthetic-policy-mismatch") }()
	until := time.Now().Add(time.Second)
	for sequence.Load() != 1 && time.Now().Before(until) {
		runtime.Gosched()
	}
	if sequence.Load() != 1 {
		c.mu.Unlock()
		<-done
		t.Fatal("refresh never entered marker")
	}
	var result contextCompletionObservation
	_, err := c.completeContextExchangeObserved(context.Background(), o, &directlan.ContextAttempt{}, directlan.VerifiedContextExchange{}, &result)
	c.mu.Unlock()
	if !errors.Is(err, directlan.ErrUnavailable) || result.Site != "core-lock" || !result.RefreshOverlap {
		t.Fatal("actual held refresh not observed")
	}
	if err := <-done; !errors.Is(err, endpointmeta.ErrReview) || sequence.Load() != 2 {
		t.Fatal("refresh exit not marked")
	}
	if !c.op.TryLock() {
		t.Fatal("refresh failed to release lock")
	}
	c.op.Unlock()
	// A skipped tick must not touch the marker or inspect the missing outbound.
	c.op.Lock()
	err = c.refreshUpgradeInbound(context.Background(), o, nil, "")
	c.op.Unlock()
	if err != nil || sequence.Load() != 2 {
		t.Fatal("skipped refresh changed marker")
	}
}

func TestContextCompletionDiagnosticResponseDeadlineBranches(t *testing.T) {
	for _, name := range []string{"success", "response-unbounded", "response-deadline", "response-context-ended"} {
		t.Run(name, func(t *testing.T) {
			for _, observed := range []bool{false, true} {
				ctx := context.Background()
				cancel := func() {}
				switch name {
				case "success":
					ctx, cancel = context.WithTimeout(ctx, time.Hour)
				case "response-deadline":
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Hour))
				case "response-context-ended":
					ctx, cancel = context.WithTimeout(ctx, time.Hour)
					cancel()
				}
				var result contextCompletionObservation
				var observation *contextCompletionObservation
				if observed {
					observation = &result
				}
				deadline, ok := contextResponseDeadlineObserved(ctx, observation)
				if ok != (name == "success") {
					t.Fatal("deadline admission changed")
				}
				expected, _ := ctx.Deadline()
				if !deadline.Equal(expected) {
					t.Fatal("deadline changed")
				}
				if observed && name != "success" && result.Site != contextCompletionSite(name) {
					t.Fatalf("site=%s want=%s", result.Site, name)
				}
				cancel()
			}
		})
	}
}

func TestActivationDiagnosticCompletedRecordsBoundedAndIsolated(t *testing.T) {
	var records activationDiagnosticCompletions
	var workers sync.WaitGroup
	for range 48 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ordinal := records.begin()
			result := contextCompletionObservation{Site: "core-lock", RefreshOverlap: ordinal%2 == 1}
			records.finish(ordinal, result)
			result.Site = "inbound-success"
		}()
	}
	workers.Wait()
	got, omitted, missing := records.snapshot()
	if len(got) != 32 || omitted != 16 || missing != 0 {
		t.Fatal("completed observations are not bounded")
	}
	for i, record := range got {
		if record.Ordinal != uint32(i+1) || record.Result.Site != "core-lock" || record.Result.RefreshOverlap != (record.Ordinal%2 == 1) {
			t.Fatal("callback observations mixed or aliased")
		}
	}
	got[0].Result.Site = "inbound-success"
	next, _, _ := records.snapshot()
	if next[0].Result.Site != "core-lock" {
		t.Fatal("snapshot changed retained observation")
	}
	var independent activationDiagnosticCompletions
	ordinal := independent.begin()
	pending, overflow, missing := independent.snapshot()
	if len(pending) != 0 || overflow != 0 || missing != 1 || reflect.DeepEqual(pending, next) {
		t.Fatal("pending or independent observations leaked")
	}
	independent.finish(ordinal, contextCompletionObservation{Site: "inbound-success"})
	separate, _, _ := independent.snapshot()
	if len(separate) != 1 || separate[0].Result.Site != "inbound-success" {
		t.Fatal("independent observer did not remain isolated")
	}
}
