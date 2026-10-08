package endpointmeta

import (
	"reflect"
	"time"
)

// MigrateManagedV4 proposes a separate lossless schema migration. It creates no
// runtime authority, terminal marker, approval, nonce, receipt or application
// grant. Publication belongs to Core's separately admitted whole-file writer.
func MigrateManagedV4(before Snapshot, now time.Time, budget int) (Snapshot, error) {
	if before.Version != SnapshotVersionV3 {
		return Snapshot{}, ErrReview
	}
	if before.PendingChange != nil {
		return Snapshot{}, ErrRecovery
	}
	if err := before.ValidateAt(now); err != nil {
		return Snapshot{}, err
	}
	revision, err := nextCounter(before.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	next := before
	next.Version, next.Revision = SnapshotVersionV4, revision
	next.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	w := wireSizer{left: budget}
	if budget <= 0 || !next.measure(&w) {
		return Snapshot{}, ErrCapacity
	}
	if err := next.Validate(); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(next), nil
}

// RevokeManagedPairV4 retains the complete saved old binding as historical
// evidence. expected must equal the whole current record, including any marker;
// a stale review is not an idempotent request. Changed describes model values,
// never publication or durability. A saved preparation with an exact proposal
// binding can be terminally cancelled without committing it or deleting evidence.
// Records without a proposal receive a distinct exact local-record denial.
// This includes legacy records with no earlier context review.
// New-context re-pair remains separate.
func RevokeManagedPairV4(before Snapshot, expected PeerRecord, now time.Time, budget int) (Snapshot, bool, error) {
	if before.Version != SnapshotVersionV4 {
		return Snapshot{}, false, ErrReview
	}
	if before.PendingChange != nil {
		return Snapshot{}, false, ErrRecovery
	}
	if err := before.ValidateAt(now); err != nil {
		return Snapshot{}, false, err
	}
	i, err := recordForKey(before, expected.Peer.Key)
	if err != nil {
		return Snapshot{}, false, err
	}
	r := before.Peers[i]
	if !reflect.DeepEqual(r, expected) {
		return Snapshot{}, false, ErrReview
	}
	binding, bindingErr := TerminalPairBinding(r)
	var review string
	if bindingErr != nil {
		review, err = LocalRecordDenialDigest(r)
		if err != nil {
			return Snapshot{}, false, err
		}
	}
	if r.PairRevocation != nil {
		w := wireSizer{left: budget}
		if budget <= 0 || !before.measure(&w) {
			return Snapshot{}, false, ErrCapacity
		}
		return cloneSnapshot(before), false, nil
	}
	revision, err := nextCounter(before.Revision)
	if err != nil {
		return Snapshot{}, false, err
	}
	next := before
	next.Revision, next.ObservedAt = revision, now.UTC().Format(time.RFC3339Nano)
	r.PairRevocation = &PairRevocation{PairBinding: binding, Revision: revision, RevokedAt: next.ObservedAt}
	if bindingErr != nil {
		r.PairRevocation.Kind = "local-record"
		r.PairRevocation.PeerKey, r.PairRevocation.RecordRevision, r.PairRevocation.RecordDigest = r.Peer.Key, r.Revision, review
	}
	w := wireSizer{left: budget}
	if budget <= 0 || !next.measureReplacing(&w, i, &r, false) {
		return Snapshot{}, false, ErrCapacity
	}
	// Allocate only after accounting for the new revision/time/marker widths.
	next.Peers = append([]PeerRecord(nil), before.Peers...)
	next.Peers[i] = r
	if err := next.Validate(); err != nil {
		return Snapshot{}, false, err
	}
	return cloneSnapshot(next), true, nil
}

// TerminalPairBinding selects retained evidence only. It grants no active
// context authority and must never be used for session admission.
func TerminalPairBinding(r PeerRecord) (string, error) {
	if r.PairContext != nil && r.EndpointState != nil {
		return r.PairContext.Binding()
	}
	if r.PairContext == nil && r.EndpointState == nil && r.UpgradePending != nil && r.UpgradePending.Context != nil {
		return r.UpgradePending.Context.Binding()
	}
	return "", ErrReview
}

// LocalRecordDenialDigest binds the exact retained local record when no pair
// binding ever existed. It is negative identity authority only: it neither
// invents a pair binding nor permits legacy fallback or future re-pair.
func LocalRecordDenialDigest(r PeerRecord) (string, error) {
	if r.PairContext != nil || r.EndpointState != nil || r.ContextConfirmed || r.UpgradePending != nil && r.UpgradePending.Context != nil {
		return "", ErrReview
	}
	r.PairRevocation = nil
	return modelDigest(r), nil
}
