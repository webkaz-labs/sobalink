package endpointmeta

import (
	"reflect"
	"testing"
)

func TestPreparedPairTerminalRemovalRetainsProposal(t *testing.T) {
	f := newContextFixture(t)
	before := migratePairFixture(t, recordedContext(t, f))
	r := before.Peers[0]
	if r.PairContext != nil || r.EndpointState != nil || r.UpgradePending == nil || r.UpgradePending.Context == nil {
		t.Fatal("fixture must be prepared only")
	}
	next, changed, err := RevokeManagedPairV4(before, r, testNow(), modelBudget)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	binding, _ := r.UpgradePending.Context.Binding()
	got := next.Peers[0]
	if got.PairRevocation == nil || got.PairRevocation.PairBinding != binding {
		t.Fatal("proposal binding not denied")
	}
	got.PairRevocation = nil
	if !reflect.DeepEqual(got, r) {
		t.Fatal("retained preparation changed")
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := contextRecord(next, r.Peer.Key, testNow()); err == nil {
		t.Fatal("terminal preparation regained authority")
	}
	again, changed, err := RevokeManagedPairV4(next, next.Peers[0], testNow(), modelBudget)
	if err != nil || changed || !reflect.DeepEqual(next, again) {
		t.Fatal("repeat changed evidence", err)
	}
}
