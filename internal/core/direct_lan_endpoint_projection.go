package core

import (
	"math"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

func directLANActiveMetadataSchema(s directLANState) bool {
	return (s.Version == directLANMetadataStateVersion || s.Version == directLANPairRecordStateVersion) &&
		s.Metadata != nil && s.Metadata.Version == s.Version
}

// An endpoint publisher cannot accept an arbitrary structurally valid snapshot.
// Reconstruct the only legal deltas from their retained fence or signed proof,
// and compare the complete value, including schema, terminal records and history.
func validateEndpointProjection(before, next endpointmeta.Snapshot) error {
	if before.Version != next.Version || (before.Version != endpointmeta.SnapshotVersionV3 && before.Version != endpointmeta.SnapshotVersionV4) {
		return endpointmeta.ErrIdentity
	}
	if reflect.DeepEqual(before, next) {
		return nil
	}
	if len(before.Peers) != len(next.Peers) {
		return endpointmeta.ErrIdentity
	}
	for i, old := range before.Peers {
		if !reflect.DeepEqual(old.PairRevocation, next.Peers[i].PairRevocation) || old.PairRevocation != nil && !reflect.DeepEqual(old, next.Peers[i]) {
			return endpointmeta.ErrIdentity
		}
	}
	if next.PendingChange != nil && before.PendingChange == nil {
		p := next.PendingChange
		now, err := time.Parse(time.RFC3339Nano, p.FencedAt)
		if err != nil {
			return endpointmeta.ErrInvalid
		}
		want, err := endpointmeta.Fence(before, p.Mutation, p.ReviewDigest, p.TransactionID, now, math.MaxInt)
		if err == nil && reflect.DeepEqual(want, next) {
			return nil
		}
		return endpointmeta.ErrIdentity
	}
	now, err := time.Parse(time.RFC3339Nano, next.ObservedAt)
	if err != nil {
		return endpointmeta.ErrInvalid
	}
	if before.PendingChange != nil && next.PendingChange == nil {
		for _, cancel := range []bool{false, true} {
			want, err := endpointmeta.FinishPending(before, cancel, now, math.MaxInt)
			if err == nil && reflect.DeepEqual(want, next) {
				return nil
			}
		}
		return endpointmeta.ErrIdentity
	}
	if before.PendingChange == nil && next.PendingChange == nil {
		for i, r := range next.Peers {
			if reflect.DeepEqual(before.Peers[i], r) {
				continue
			}
			if r.PairRevocation != nil || r.EndpointState == nil || r.EndpointState.IssuedProof == nil {
				return endpointmeta.ErrIdentity
			}
			want, err := endpointmeta.ProposePreparedIssued(before, r.Peer.Key, *r.EndpointState.IssuedProof, now, math.MaxInt)
			if err == nil && reflect.DeepEqual(want, next) {
				return nil
			}
			return endpointmeta.ErrIdentity
		}
	}
	return endpointmeta.ErrIdentity
}
