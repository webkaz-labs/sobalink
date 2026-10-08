package endpointmeta

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestLocalRecordDenialCanonicalRoundtripAndExactSize(t *testing.T) {
	f := newContextFixture(t)
	for _, reviewed := range []Snapshot{f.legacy, f.reviewed} {
		before := migratePairFixture(t, reviewed)
		// Expired review is retained evidence; negative removal never renews it.
		now := testNow().Add(2 * time.Hour)
		next, changed, err := RevokeManagedPairV4(before, before.Peers[0], now, modelBudget)
		if err != nil || !changed {
			t.Fatal(changed, err)
		}
		marker := next.Peers[0].PairRevocation
		digest, _ := LocalRecordDenialDigest(before.Peers[0])
		if marker.Kind != "local-record" || marker.PairBinding != "" || marker.PeerKey != before.Peers[0].Peer.Key || marker.RecordRevision != before.Peers[0].Revision || marker.RecordDigest != digest {
			t.Fatal(marker)
		}
		retained := next.Peers[0]
		retained.PairRevocation = nil
		if !reflect.DeepEqual(retained, before.Peers[0]) {
			t.Fatal("review transcript rewritten")
		}
		wire, err := EncodeSnapshot(next, modelBudget)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Snapshot
		if err = json.Unmarshal(wire, &decoded); err != nil || decoded.Validate() != nil || !reflect.DeepEqual(decoded, next) {
			t.Fatal("local denial roundtrip", err)
		}
		w := wireSizer{left: len(wire)}
		if !next.measure(&w) || w.left != 0 {
			t.Fatal("canonical size mismatch", w.left)
		}
		if _, _, err = RevokeManagedPairV4(before, before.Peers[0], now, len(wire)); err != nil {
			t.Fatal("exact marker budget", err)
		}
		if _, _, err = RevokeManagedPairV4(before, before.Peers[0], now, len(wire)-1); err == nil {
			t.Fatal("marker budget ignored")
		}
		if _, _, err = RevokeManagedPairV4(next, before.Peers[0], now, modelBudget); err == nil {
			t.Fatal("stale pre-marker review accepted")
		}
		if _, err = contextRecord(next, before.Peers[0].Peer.Key, now); err == nil {
			t.Fatal("local denial became context authority")
		}
	}
}

func TestLocalRecordDenialRejectsMixedVariantsAndStaleReview(t *testing.T) {
	f := newContextFixture(t)
	before := migratePairFixture(t, f.reviewed)
	next, _, err := RevokeManagedPairV4(before, before.Peers[0], testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	changes := []func(*PeerRecord){
		func(r *PeerRecord) { r.PairRevocation.Kind = "unknown" },
		func(r *PeerRecord) { r.PairRevocation.Kind = "" },
		func(r *PeerRecord) { r.PairRevocation.PairBinding = "invented-binding" },
		func(r *PeerRecord) { r.PairRevocation.PeerKey = next.LocalPeer.Key },
		func(r *PeerRecord) { r.PairRevocation.RecordRevision = "2" },
		func(r *PeerRecord) { r.PairRevocation.RecordDigest = "changed" },
		func(r *PeerRecord) {
			r.UpgradePending.PrepareDeadline = testNow().Add(time.Hour).Format(time.RFC3339Nano)
		},
		func(r *PeerRecord) { r.ContextConfirmed = true },
	}
	for _, change := range changes {
		altered := cloneSnapshot(next)
		change(&altered.Peers[0])
		if altered.Validate() == nil {
			t.Fatal("mixed/stale denial accepted")
		}
	}
	stale := before.Peers[0]
	stale.Revision = "2"
	if _, _, err := RevokeManagedPairV4(before, stale, testNow(), modelBudget); err == nil {
		t.Fatal("stale review accepted")
	}
	proposal := migratePairFixture(t, recordedContext(t, f))
	proposal.Peers[0].PairRevocation = next.Peers[0].PairRevocation
	if proposal.Validate() == nil {
		t.Fatal("local denial used despite an existing proposal binding")
	}
}

func TestPairBindingMarkerEncodingUnchanged(t *testing.T) {
	marker := PairRevocation{PairBinding: "binding", Revision: "2", RevokedAt: "2030-01-02T03:04:05Z"}
	raw, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(raw, []byte(`{"pair_binding":"binding","revision":"2","revoked_at":"2030-01-02T03:04:05Z"}`)) {
		t.Fatal("old marker bytes changed", string(raw), err)
	}
}
