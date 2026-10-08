//go:build endpoint_following_acceptance

package core

import (
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"net/netip"
)

type endpointAcceptanceDiagnosticTarget struct {
	label    string
	core     *Core
	expected netip.AddrPort
	writes   *endpointAcceptanceWrites
}
type endpointAcceptancePeerSnapshot struct {
	index                              int
	expected                           bool
	received, issued, authority, state string
}
type endpointAcceptanceTransactionSnapshot struct {
	present, native, joined, durable, published, staging, candidate, constructed, receiptSame bool
	failure                                                                                   string
}
type endpointAcceptanceSnapshot struct {
	operationBusy, coreBusy, storeBusy, backendPresent                bool
	job, replacing, stopped, recovery, receiptPresent, receiptCurrent bool
	revision                                                          uint64
	modelPresent, pending                                             bool
	modelRevision                                                     string
	peers                                                             []endpointAcceptancePeerSnapshot
	transaction                                                       endpointAcceptanceTransactionSnapshot
	completionPresent, completionStopped, epochValid                  bool
	node                                                              *directlan.Node // private attribution for a later copied observer read; never logged
}

// Copy only bounded scalar fields while holding their ordinary owning locks.
// There is no formatting, testing output, filesystem/network I/O, or callback.
// Every return releases all production locks before the caller can log a field.
func captureEndpointAcceptanceSnapshot(target endpointAcceptanceDiagnosticTarget, expected string) endpointAcceptanceSnapshot {
	var out endpointAcceptanceSnapshot
	c := target.core
	if !c.op.TryLock() {
		out.operationBusy = true
		return out
	}
	defer c.op.Unlock()
	if !c.mu.TryRLock() {
		out.coreBusy = true
		return out
	}
	var b *directLANBackend
	switch root := c.node.(type) {
	case *directLANBackend:
		b = root
	case *mixedBackend:
		b, _ = root.nodes["direct-lan"].(*directLANBackend)
	}
	out.job = c.endpointJob != nil
	c.mu.RUnlock()
	if b == nil || b.store == nil {
		return out
	}
	out.backendPresent = true
	out.node = b.Node
	s := b.store
	if !s.mu.TryLock() {
		out.storeBusy = true
		return out
	}
	defer s.mu.Unlock()
	receipt := s.contextPublication
	out.receiptPresent = receipt != nil
	out.receiptCurrent = receipt != nil && receipt.store == s && receipt.process == c.lanStartNonce && receipt.file == s.fileDigest && receipt.state == privateRevision(s.state) && receipt.writeRevision == s.reviewRevision && !s.recovery
	out.replacing, out.stopped, out.recovery, out.revision = b.replacing.Load() != nil, b.endpointStopped.Load(), s.recovery, s.reviewRevision
	if m := s.state.Metadata; m != nil {
		out.modelPresent, out.modelRevision, out.pending = true, m.Revision, m.PendingChange != nil
		out.peers = make([]endpointAcceptancePeerSnapshot, 0, len(m.Peers))
		for i, r := range m.Peers {
			if e := r.EndpointState; e != nil {
				out.peers = append(out.peers, endpointAcceptancePeerSnapshot{i, r.Peer.Endpoint == expected, e.ReceivedHighwater, e.IssuedHighwater, e.AuthorityRevision, e.ReceiveStatus})
			}
		}
	}
	transaction := b.endpointTransaction
	if transaction == nil && target.writes != nil {
		if target.writes.mu.TryLock() {
			transaction = target.writes.transaction
			target.writes.mu.Unlock()
		}
	}
	if transaction != nil {
		_, native := transaction.old.(*endpointNodeOwner)
		out.transaction = endpointAcceptanceTransactionSnapshot{present: true, native: native, joined: transaction.joined, durable: transaction.durable, published: transaction.published, staging: transaction.staging, candidate: transaction.candidate != nil, constructed: transaction.constructionReturned, receiptSame: transaction.receipt == receipt, failure: endpointAcceptanceErrorCode(transaction.failure)}
	}
	if owner := b.currentCompletion(); owner != nil {
		out.completionPresent, out.completionStopped, out.epochValid = true, owner.stopped.Load(), owner.authority.Load().Valid()
	}
	return out
}

// No identifiers, addresses, proof bytes, raw errors or filesystem paths leave
// this snapshot. Trace reads return copied events with their mutex released.
// All formatting and output below occurs after every production/trace unlock.
func (f *endpointAcceptanceFixture) dumpEndpointDiagnostics(reason string) {
	f.t.Helper()
	f.t.Logf("endpoint diagnostic reason=%s", reason)
	for _, target := range f.diagnosticTargets {
		snapshot := captureEndpointAcceptanceSnapshot(target, target.expected.String())
		events, dropped := readEndpointAcceptanceTrace(target.core)
		var wireEvents []directlan.AcceptanceEndpointEvent
		var wireDropped int
		if snapshot.node != nil {
			wireEvents, wireDropped = f.observer.EndpointEvents(snapshot.node)
		}
		f.t.Logf("endpoint side=%s core_events=%v dropped=%d", target.label, events, dropped)
		if snapshot.operationBusy {
			f.t.Logf("endpoint side=%s operation_busy=true", target.label)
			continue
		}
		if snapshot.coreBusy {
			f.t.Logf("endpoint side=%s core_busy=true", target.label)
			continue
		}
		if !snapshot.backendPresent {
			f.t.Logf("endpoint side=%s backend_present=false job_present=%v", target.label, snapshot.job)
			continue
		}
		f.t.Logf("endpoint side=%s transport_events=%v dropped=%d", target.label, wireEvents, wireDropped)
		if snapshot.storeBusy {
			f.t.Logf("endpoint side=%s store_busy=true", target.label)
			continue
		}
		f.t.Logf("endpoint side=%s job_present=%v replacing=%v stopped=%v recovery=%v store_write_revision=%d receipt_present=%v receipt_matches_memory=%v", target.label, snapshot.job, snapshot.replacing, snapshot.stopped, snapshot.recovery, snapshot.revision, snapshot.receiptPresent, snapshot.receiptCurrent)
		if snapshot.modelPresent {
			f.t.Logf("endpoint side=%s model_revision=%s pending_fence=%v", target.label, snapshot.modelRevision, snapshot.pending)
			for _, peer := range snapshot.peers {
				f.t.Logf("endpoint side=%s peer_index=%d expected_endpoint=%v received_highwater=%s issued_highwater=%s authority_revision=%s receive_state=%s", target.label, peer.index, peer.expected, peer.received, peer.issued, peer.authority, peer.state)
			}
		}
		if tx := snapshot.transaction; tx.present {
			f.t.Logf("endpoint side=%s transaction_native=%v joined=%v durable=%v published=%v staging=%v candidate_present=%v construction_returned=%v receipt_same=%v failure_code=%s", target.label, tx.native, tx.joined, tx.durable, tx.published, tx.staging, tx.candidate, tx.constructed, tx.receiptSame, tx.failure)
		}
		if snapshot.completionPresent {
			f.t.Logf("endpoint side=%s completion_stopped=%v epoch_valid=%v", target.label, snapshot.completionStopped, snapshot.epochValid)
		}
	}
}
