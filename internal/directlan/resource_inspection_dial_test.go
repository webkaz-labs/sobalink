package directlan

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInspectionWakeupRejectsMissingAuthority(t *testing.T) {
	n := &Node{}
	for _, authentication := range []*managedAuthentication{nil, {}, {peer: &peerState{}}} {
		if err := n.wakeInspectionSession(context.Background(), authentication); !errors.Is(err, ErrUntrusted) {
			t.Fatal("missing transport authority admitted a wakeup")
		}
	}
}

// An occupied in-memory session gate makes every network path unreachable.
// Cancellation must join without consuming or releasing another caller's gate.
func TestInspectionWakeupRejectsCancelledGate(t *testing.T) {
	n := &Node{}
	p := &peerState{session: newPeerSession(true)}
	p.session.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.wakeInspectionSession(ctx, &managedAuthentication{peer: p}) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || len(p.session.gate) != 1 {
			t.Fatal("cancelled wait consumed another session gate")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled session-gate wait did not join")
	}
	<-p.session.gate
}
