package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Source-authored inert checks only. The fixture uses the actual disposable
// file writer and fake old owner, never a Node, native build or transport.
func TestEndpointPublicationRequiresNativeSavedOwner(t *testing.T) {
	c, s, b, old, m, now := endpointTransactionFixture(t)
	tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err = c.publishEndpointTransaction(context.Background(), tx); !errors.Is(err, endpointmeta.ErrReview) {
		t.Fatal("inert old owner could construct", err)
	}
	if tx.staging || tx.candidate != nil || tx.published || s.endpointTransaction != tx || b.endpointTransaction != tx {
		t.Fatal("rejection changed ownership")
	}
}

func TestEndpointPublicationCurrentCompletionAndDeadline(t *testing.T) {
	c, s, b, old, m, now := endpointTransactionFixture(t)
	tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	o := &managedCompletionOwner{core: c, coreDone: c.ctx.Done(), backend: b, root: b, store: s, process: tx.process, receipt: tx.receipt, revision: s.reviewRevision, limits: tx.limits, limitsSource: tx.limitsSource, configuration: contextConfigurationDigest(s.state), currentEndpoints: true, epoch: directlan.NewContextEpoch(), deadline: now.Add(time.Hour)}
	o.authority.Store(o.epoch)
	s.mu.Lock()
	s.contextEpoch = o.epoch
	key := s.state.Metadata.Peers[0].Peer.Key
	pair := s.state.Metadata.Peers[0].PairContext
	binding, _ := pair.Binding()
	reply, err := o.currentReplyLocked(key, endpointmeta.BoundRequest{Version: 2, Operation: "pair-context-status", PairBinding: binding}, now)
	s.mu.Unlock()
	if err != nil || !reply.OK || reply.PairBinding != binding {
		t.Fatal("current receipt projection refused", err)
	}
	if !o.authorityCurrent() {
		t.Fatal("fresh authority absent")
	}
	o.deadline = time.Now().Add(-time.Second) // fixture mutation before any concurrency
	if o.authorityCurrent() {
		t.Fatal("original elapsed cutoff renewed")
	}
	o.deadline = time.Now().Add(time.Hour)
	b.endpointStopped.Store(true)
	if o.authorityCurrent() {
		t.Fatal("Stop signal ignored")
	}
}

func TestEndpointPublicationCleanupSlotEvidence(t *testing.T) {
	c, s, b, old, m, now := endpointTransactionFixture(t)
	tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tx.staging = true
	tx.failure = directlan.ErrUnavailable
	if err = c.finishEndpointTransactionCleanup(c.ctx, tx); !errors.Is(err, endpointmeta.ErrReview) || b.endpointTransaction != tx {
		t.Fatal("running construction released", err)
	}
	// Explicit no-allocation construction failure: no candidate exists to join.
	tx.constructionReturned = true
	if err = c.finishEndpointTransactionCleanup(c.ctx, tx); !errors.Is(err, directlan.ErrUnavailable) {
		t.Fatal(err)
	}
	if b.endpointTransaction != nil || s.endpointTransaction != nil || tx.published || !tx.durable || tx.failure == nil {
		t.Fatal("failed cleanup result lost saved evidence")
	}
}
