package resourcegrant

import (
	"sync"
	"testing"
	"time"
)

func TestDisclosureFenceExactScopeAndTerminalRevoke(t *testing.T) {
	record := fixtureRecord()
	now := time.Unix(150, 250000000)
	fence, err := newDisclosureFence(record, now)
	if err != nil {
		t.Fatal(err)
	}
	request := InspectRequest{ProtocolVersion: ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision}
	at := func() time.Time { return now }
	if !fence.admit(request, record.Relationship, at) {
		t.Fatal("exact scope denied")
	}
	wrong := request
	wrong.GrantRevision++
	if fence.admit(wrong, record.Relationship, at) {
		t.Fatal("new revision inherited authority")
	}
	relationship := record.Relationship
	relationship.PairBinding = relationship.TargetKey
	if fence.admit(request, relationship, at) {
		t.Fatal("new pair inherited authority")
	}
	relationship = record.Relationship
	relationship.PeerKey = relationship.TargetKey
	if fence.admit(request, relationship, at) {
		t.Fatal("other peer inherited authority")
	}
	fence.Close()
	fence.Close()
	if fence.admit(request, record.Relationship, at) {
		t.Fatal("closed fence reopened")
	}
	var zero *DisclosureFence
	zero.Close()
	if zero.admit(request, record.Relationship, at) {
		t.Fatal("nil fence admitted")
	}
}

func TestDisclosureFenceFiniteNonextendingDeadline(t *testing.T) {
	record := fixtureRecord()
	now := time.Unix(150, 250000000)
	fence, err := newDisclosureFence(record, now)
	if err != nil {
		t.Fatal(err)
	}
	if !fence.deadline.Equal(time.Unix(record.ExpiresAt, 0)) {
		t.Fatal("fractional second extended expiry")
	}
	request := fence.request
	for _, at := range []time.Time{time.Unix(99, 0), time.Unix(200, 0), time.Unix(201, 0)} {
		candidate, err := newDisclosureFence(record, now)
		if err != nil {
			t.Fatal(err)
		}
		if candidate.admit(request, record.Relationship, func() time.Time { return at }) {
			t.Fatal("outside grant lifetime admitted")
		}
		if candidate.admit(request, record.Relationship, func() time.Time { return now }) {
			t.Fatal("time rollback restored observed expired authority")
		}
	}
	// A fixed process deadline also limits admission if wall time is otherwise
	// eligible. The synthetic seam needs no real clock or waiting.
	fence.deadline = now
	if fence.admit(request, record.Relationship, func() time.Time { return now }) {
		t.Fatal("deadline equality admitted")
	}
	for _, state := range []string{Revoked, "unknown"} {
		record.State = state
		if _, err := newDisclosureFence(record, now); err == nil {
			t.Fatal("inactive authority constructed")
		}
	}
}

func TestDisclosureFenceConcurrentClose(t *testing.T) {
	record := fixtureRecord()
	now := time.Unix(150, 0)
	fence, err := newDisclosureFence(record, now)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			fence.admit(fence.request, record.Relationship, func() time.Time { return now })
		}()
	}
	fence.Close()
	group.Wait()
	for i := 0; i < 8; i++ {
		if fence.admit(fence.request, record.Relationship, func() time.Time { return now }) {
			t.Fatal("post-revoke admission")
		}
	}
}

func TestDisclosureFenceRevocationLinearization(t *testing.T) {
	record := fixtureRecord()
	now := time.Unix(150, 0)
	fence, err := newDisclosureFence(record, now)
	if err != nil {
		t.Fatal(err)
	}
	if !fence.admit(fence.request, record.Relationship, func() time.Time { fence.Close(); return now }) {
		t.Fatal("already admitted response incorrectly described as revoked")
	}
	if fence.admit(fence.request, record.Relationship, func() time.Time { return now }) {
		t.Fatal("later response admitted")
	}
}

func TestDisclosureFenceRetainedCutoffNeverExtends(t *testing.T) {
	now := time.Now()
	record := fixtureRecord()
	record.IssuedAt, record.ExpiresAt = now.Add(-time.Second).Unix(), now.Add(time.Hour).Unix()
	cutoff := now.Add(time.Minute)
	fence, err := NewDisclosureFenceBefore(record, cutoff)
	if err != nil || fence.deadline.After(cutoff) {
		t.Fatal("activation extended retained cutoff", err)
	}
	request := InspectRequest{ProtocolVersion: ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision}
	if fence.admit(request, record.Relationship, func() time.Time { return cutoff }) {
		t.Fatal("retained cutoff admitted disclosure")
	}
	expired, uncertain := fence.TimingObservation(request)
	if !expired || uncertain {
		t.Fatal("cutoff expiry evidence lost")
	}
}

func TestDisclosureFenceTimingEvidenceIsExactAndTerminal(t *testing.T) {
	record := fixtureRecord()
	request := InspectRequest{ProtocolVersion: ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision}
	for _, anomaly := range []bool{false, true} {
		fence, err := newDisclosureFence(record, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		observed := time.Unix(record.ExpiresAt, 0)
		if anomaly {
			observed = time.Unix(record.IssuedAt-1, 0)
		}
		if fence.admit(request, record.Relationship, func() time.Time { return observed }) {
			t.Fatal("terminal observation admitted")
		}
		expired, uncertain := fence.TimingObservation(request)
		if expired == anomaly || uncertain != anomaly {
			t.Fatal("wrong terminal evidence")
		}
		if fence.admit(request, record.Relationship, func() time.Time { return time.Unix(110, 0) }) {
			t.Fatal("plausible later time reopened fence")
		}
		other := request
		other.GrantRevision++
		if expired, uncertain := fence.TimingObservation(other); expired || uncertain {
			t.Fatal("old evidence applied to another grant")
		}
	}
}
