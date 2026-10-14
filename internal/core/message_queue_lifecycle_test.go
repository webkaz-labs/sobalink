package core

import (
	"context"
	"testing"
	"time"
)

func TestMessageQueuePeerReductionCancelsAndJoinsExactRuns(t *testing.T) {
	for _, action := range []string{"revoke", "pause"} {
		t.Run(action, func(t *testing.T) {
			pair := newCorePair(t)
			trustPair(t, pair)
			c := pair.a
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()
			otherCtx, otherCancel := context.WithCancel(c.ctx)
			defer otherCancel()
			run := &messageQueueRun{id: "synthetic-reduced", ctx: ctx, cancel: cancel, joined: make(chan struct{}), selection: messageQueueSelection{trust: Trust{ID: "peer-b"}}}
			defer func() {
				select {
				case <-run.joined:
				default:
					close(run.joined)
				}
			}()
			other := &messageQueueRun{id: "synthetic-other", ctx: otherCtx, cancel: otherCancel, joined: make(chan struct{}), selection: messageQueueSelection{trust: Trust{ID: "peer-other"}}}
			c.mu.Lock()
			q := c.messageQueueRuntimeLocked()
			q.runs[run.id], q.runs[other.id] = run, other
			c.outgoingActive = 2
			c.mu.Unlock()
			defer func() { c.mu.Lock(); c.messageQueue = nil; c.outgoingActive = 0; c.mu.Unlock() }()
			returned := make(chan error, 1)
			go func() {
				if action == "pause" {
					_, err := command(c, randomID(), "peer.autosave", map[string]any{"peerId": "peer-b", "paused": true})
					returned <- err
				} else {
					returned <- c.revokePeerWithFailure("peer-b", nil)
				}
			}()
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("peer reduction did not cancel the matching text owner")
			}
			select {
			case err := <-returned:
				t.Fatal("peer reduction returned before physical text owner joined", err)
			default:
			}
			c.mu.Lock()
			retained := c.messageQueue.runs[run.id] == run && c.outgoingActive == 2
			c.mu.Unlock()
			if !retained || otherCtx.Err() != nil {
				t.Fatal("reduction released active evidence or cancelled another peer")
			}
			close(run.joined)
			select {
			case err := <-returned:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("peer reduction did not finish after physical join")
			}
		})
	}
}
