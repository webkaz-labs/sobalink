package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// These tests exercise copied reducers, codecs and pre-disruption bookkeeping
// only. They never construct a transport, dispatch a wire or run a Core job.
func TestEndpointFollowingReceiptMismatchRejectsBeforeRetirement(t *testing.T) {
	c, s, b, old, mutation, now := endpointTransactionFixture(t)
	receipt := s.contextPublication
	writes := 0
	s.write = func(string, []byte) error { writes++; return errors.New("unexpected write") }
	transaction, err := c.saveEndpointTransactionWithOwner(context.Background(), b, mutation, old, func() time.Time { return now }, func(store *directLANStore) error {
		if store.contextPublication == receipt {
			return endpointmeta.ErrReview
		}
		return nil
	})
	if !errors.Is(err, endpointmeta.ErrReview) || transaction != nil || old.began || old.waited || writes != 0 || b.endpointTransaction != nil || s.endpointTransaction != nil || s.contextPublication != receipt {
		t.Fatal("rejected observation disrupted owner", err)
	}
}

func TestEndpointMovePreviewCopiesAndRejectsDestinations(t *testing.T) {
	_, s, key := localEndpointExportFixture(t, nil)
	m := *cloneDirectLANMetadata(s.state.Metadata)
	before := *cloneDirectLANMetadata(&m)
	in := endpointMoveInput{Endpoint: "127.0.0.3:22003", Deliveries: []endpointMoveDelivery{{PeerID: key, Lifetime: "until-revoked"}}}
	review, mutation, err := previewEndpointMove(m, in, time.Now(), 1<<20)
	if err != nil || mutation.Kind != "local-endpoint" || review.Destinations[key] != m.Peers[0].Peer.Endpoint || !reflect.DeepEqual(m, before) {
		t.Fatal("preview changed evidence or guessed destination", err)
	}
	review.Deliveries[0].Lifetime = "changed"
	if in.Deliveries[0].Lifetime != "until-revoked" {
		t.Fatal("aliased approval")
	}
	for _, test := range []struct {
		name  string
		input endpointMoveInput
	}{
		{"outside-remote-scope", endpointMoveInput{Endpoint: "127.0.1.3:22003"}},
		{"unchanged", endpointMoveInput{Endpoint: m.LocalPeer.Endpoint}},
		{"unknown-recipient", endpointMoveInput{Endpoint: in.Endpoint, Deliveries: []endpointMoveDelivery{{PeerID: "unknown", Lifetime: "until-revoked"}}}},
		{"duplicate-recipient", endpointMoveInput{Endpoint: in.Endpoint, Deliveries: append(append([]endpointMoveDelivery{}, in.Deliveries...), in.Deliveries...)}},
		{"expired-offer", endpointMoveInput{Endpoint: in.Endpoint, Deliveries: []endpointMoveDelivery{{PeerID: key, Lifetime: "finite", Expires: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := previewEndpointMove(m, test.input, time.Now(), 1<<20); err == nil {
				t.Fatal("accepted invalid preview")
			}
		})
	}
	if !reflect.DeepEqual(m, before) {
		t.Fatal("rejected proposal mutated model")
	}
}

func TestEndpointMoveInputRejectsUnknownAuthority(t *testing.T) {
	for _, raw := range []string{`{"endpoint":"127.0.0.3:22003","confirmed":true}`, `{"endpoint":"127.0.0.3:22003"} {}`, `{"deliveries":[{"peerId":"x","address":"127.0.0.5:9"}]}`} {
		if _, err := decodeEndpointMove(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted caller authority or destination")
		}
	}
}

func TestEndpointRedeliveryPreservesIssuedCutoff(t *testing.T) {
	_, s, key := localEndpointExportFixture(t, nil)
	now := time.Now()
	m := *cloneDirectLANMetadata(s.state.Metadata)
	body, err := endpointmeta.PrepareExport(m, key, endpointmeta.ExportOptions{Operation: "set", Lifetime: "finite", Expires: now.Add(time.Minute).UTC().Format(time.RFC3339Nano)}, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.state.Identity.SignEndpointUpdate(body)
	if err != nil {
		t.Fatal(err)
	}
	next, err := endpointmeta.ProposeIssued(m, key, proof, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s.observeIssuedEndpointDeadlinesLocked(next, now)
	digest, _ := proof.Digest()
	first := s.endpointIssuedDeadlines[digest]
	s.observeIssuedEndpointDeadlinesLocked(next, now.Add(10*time.Second))
	if got := s.endpointIssuedDeadlines[digest]; got != first {
		t.Fatal("retry renewed original cutoff")
	}
	s.observeIssuedEndpointDeadlinesLocked(next, now.Add(2*time.Minute))
	if got := s.endpointIssuedDeadlines[digest]; !got.expired || !got.monotonic.Equal(first.monotonic) {
		t.Fatal("expiry lost first cutoff")
	}
	s.observeIssuedEndpointDeadlinesLocked(next, now.Add(10*time.Second))
	if !s.endpointIssuedDeadlines[digest].expired {
		t.Fatal("observation revived expired delivery")
	}
	if len(s.endpointDeadlines) != 0 {
		t.Fatal("outbound proof created inbound authority")
	}
}

func TestEndpointMoveBatchPreflightCumulativeRevision(t *testing.T) {
	_, s, key := localEndpointExportFixture(t, nil)
	now := time.Now()
	mutation := endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: "127.0.0.3:22003"}
	delivery := endpointMoveDelivery{PeerID: key, Lifetime: "until-revoked"}
	// Repeating a recipient is not admitted by the command preview; the pure
	// batch check independently accounts for every write even for such input.
	s.reviewRevision = ^uint64(0) - 3
	before := privateRevision(s.state)
	if proofs, err := s.preflightEndpointMoveExportsLocked(mutation, []endpointMoveDelivery{delivery}, now); err != nil || len(proofs) != 1 {
		t.Fatal("single fully budgeted batch", err)
	}
	if _, err := s.preflightEndpointMoveExportsLocked(mutation, []endpointMoveDelivery{delivery, delivery}, now); !errors.Is(err, endpointmeta.ErrCapacity) {
		t.Fatal("cumulative writer revision overflow accepted", err)
	}
	if privateRevision(s.state) != before || s.state.Metadata.PendingChange != nil || s.state.Metadata.Peers[0].EndpointState.IssuedProof != nil {
		t.Fatal("private preflight published signed input")
	}
}

func TestPreparedEndpointIssuanceKeepsSignedTimeAndCurrentObservation(t *testing.T) {
	_, s, key := localEndpointExportFixture(t, nil)
	now := time.Now()
	m := *cloneDirectLANMetadata(s.state.Metadata)
	body, err := endpointmeta.PrepareExport(m, key, endpointmeta.ExportOptions{Operation: "set", Lifetime: "finite", Expires: now.Add(time.Minute).UTC().Format(time.RFC3339Nano)}, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.state.Identity.SignEndpointUpdate(body)
	if err != nil {
		t.Fatal(err)
	}
	m.ObservedAt = now.Add(time.Second).UTC().Format(time.RFC3339Nano)
	later := now.Add(2 * time.Second)
	next, err := endpointmeta.ProposePreparedIssued(m, key, proof, later, 1<<20)
	if err != nil || next.Peers[0].EndpointState.IssuedProof == nil || *next.Peers[0].EndpointState.IssuedProof != proof || next.ObservedAt != later.UTC().Format(time.RFC3339Nano) {
		t.Fatal("prepared proof renewed or rejected current observation", err)
	}
	if _, err := endpointmeta.ProposePreparedIssued(m, key, proof, now.Add(2*time.Minute), 1<<20); !errors.Is(err, endpointmeta.ErrExpired) {
		t.Fatal("expired prepared proof accepted", err)
	}
	wrong := proof
	wrong.Update.Sequence = "2"
	if _, err := endpointmeta.ProposePreparedIssued(m, key, wrong, later, 1<<20); err == nil {
		t.Fatal("prepared body mismatch accepted")
	}
}

func TestIssuedEndpointCutoffSurvivesUnadoptedCandidate(t *testing.T) {
	_, s, key := localEndpointExportFixture(t, nil)
	now := time.Now()
	m := *cloneDirectLANMetadata(s.state.Metadata)
	issue := func(model endpointmeta.Snapshot, at time.Time) endpointmeta.Snapshot {
		body, err := endpointmeta.PrepareExport(model, key, endpointmeta.ExportOptions{Operation: "set", Lifetime: "finite", Expires: now.Add(time.Hour).UTC().Format(time.RFC3339Nano)}, at, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := s.state.Identity.SignEndpointUpdate(body)
		if err != nil {
			t.Fatal(err)
		}
		next, err := endpointmeta.ProposeIssued(model, key, proof, at, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	retained := issue(m, now)
	s.state.Metadata = cloneDirectLANMetadata(&retained)
	s.observeIssuedEndpointDeadlinesLocked(retained, now)
	oldDigest, _ := retained.Peers[0].EndpointState.IssuedProof.Digest()
	original := s.endpointIssuedDeadlines[oldDigest]
	original.monotonic = now.Add(10 * time.Second)
	s.endpointIssuedDeadlines[oldDigest] = original
	candidate := issue(retained, now.Add(5*time.Second))
	candidateDigest, _ := candidate.Peers[0].EndpointState.IssuedProof.Digest()
	s.preserveIssuedEndpointCandidateLocked(candidate, now.Add(5*time.Second))
	if len(s.endpointIssuedDeadlines) != 2 || s.endpointIssuedDeadlines[oldDigest] != original {
		t.Fatal("private candidate discarded retained first cutoff")
	}
	// Model an unadopted write: s.state remains the exact old retained snapshot.
	s.pruneIssuedEndpointDeadlinesLocked(now.Add(20 * time.Second))
	got := s.endpointIssuedDeadlines[oldDigest]
	if len(s.endpointIssuedDeadlines) != 1 || !got.monotonic.Equal(original.monotonic) || !got.expired {
		t.Fatal("failed publication renewed retained cutoff")
	}
	if _, ok := s.endpointIssuedDeadlines[candidateDigest]; ok {
		t.Fatal("unadopted private candidate retained")
	}
	s.observeIssuedEndpointDeadlinesLocked(retained, now.Add(25*time.Second))
	if got = s.endpointIssuedDeadlines[oldDigest]; !got.monotonic.Equal(original.monotonic) || !got.expired {
		t.Fatal("reobservation revived old proof")
	}
	// Actual adoption instead preserves the candidate's first observation and
	// only then removes the predecessor's bound.
	s.preserveIssuedEndpointCandidateLocked(candidate, now.Add(30*time.Second))
	candidateBound := s.endpointIssuedDeadlines[candidateDigest]
	s.state.Metadata = cloneDirectLANMetadata(&candidate)
	s.pruneIssuedEndpointDeadlinesLocked(now.Add(35 * time.Second))
	if len(s.endpointIssuedDeadlines) != 1 || s.endpointIssuedDeadlines[candidateDigest] != candidateBound {
		t.Fatal("adopted candidate cutoff renewed")
	}
}
