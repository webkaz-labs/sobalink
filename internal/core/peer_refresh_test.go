package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

func refreshFixture(count int) identity.State {
	var state identity.State
	for i := count - 1; i >= 0; i-- {
		state.Snapshot.Peers = append(state.Snapshot.Peers, policy.Peer{ID: fmt.Sprintf("peer-%04d", i)})
	}
	return state
}

func TestPeerRefreshRotatesStableBatchesBeyond128(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(400)
	var mu sync.Mutex
	seen := map[string]int{}
	probe := func(_ context.Context, id string) error { mu.Lock(); seen[id]++; mu.Unlock(); return nil }
	for i := 0; i < 4; i++ {
		c.refreshPeerBatch(context.Background(), state, probe)
		// Changing source order must not change fair scheduling.
		state.Snapshot.Peers = append(state.Snapshot.Peers[1:], state.Snapshot.Peers[0])
	}
	if len(seen) != 400 {
		t.Fatalf("only %d of 400 peers examined", len(seen))
	}
	for _, count := range seen {
		if count < 1 || count > 2 {
			t.Fatal("peer starvation or duplicate scheduling")
		}
	}
}

func TestPeerRefreshDeadlineKeepsUnexaminedCandidates(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(20)
	ctx, cancel := context.WithCancel(context.Background())
	var active, maximum, started atomic.Int64
	fourStarted := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		c.refreshPeerBatch(ctx, state, func(ctx context.Context, id string) error {
			current := active.Add(1)
			for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
			}
			if started.Add(1) == 4 {
				close(fourStarted)
			}
			<-ctx.Done()
			active.Add(-1)
			return ctx.Err()
		})
	}()
	select {
	case <-fourStarted:
	case <-time.After(time.Second):
		t.Fatal("four probes did not start")
	}
	cancel()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not cancel")
	}
	if maximum.Load() != 4 || started.Load() != 4 || c.refreshCursor != "peer-0003" {
		t.Fatalf("wrong canceled progress: active=%d started=%d cursor=%s", maximum.Load(), started.Load(), c.refreshCursor)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	c.refreshPeerBatch(context.Background(), state, func(_ context.Context, id string) error { mu.Lock(); seen[id] = true; mu.Unlock(); return nil })
	if !seen["peer-0004"] || !seen["peer-0019"] {
		t.Fatal("unexamined candidates were skipped")
	}
}

func TestPeerRefreshSkipsExpiredAndRecentlyConfirmedWithoutStarvation(t *testing.T) {
	c := &Core{confirmed: map[string]time.Time{}}
	state := refreshFixture(260)
	for i := 0; i < 128; i++ {
		c.confirmed[fmt.Sprintf("peer-%04d", i)] = time.Now()
	}
	state.Snapshot.Peers = append(state.Snapshot.Peers, policy.Peer{ID: "expired", Expired: true}, policy.Peer{}, policy.Peer{ID: "peer-0259"})
	var mu sync.Mutex
	seen := map[string]bool{}
	probe := func(_ context.Context, id string) error { mu.Lock(); seen[id] = true; mu.Unlock(); return nil }
	for i := 0; i < 3; i++ {
		c.refreshPeerBatch(context.Background(), state, probe)
	}
	if seen[""] || seen["expired"] || seen["peer-0000"] || !seen["peer-0259"] {
		t.Fatal("eligibility filter or rotating schedule failed")
	}
}
