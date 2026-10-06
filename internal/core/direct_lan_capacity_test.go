package core

import (
	"errors"
	"os"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func TestDirectLANLiveLogicalCapacityPreservesSavedPairs(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	policy := capacity.Defaults()
	policy.Logical["trustedPeers"] = capacity.Unlimited()
	if e := applyLANTestPolicy(t, c, policy); e != nil {
		t.Fatal(e)
	}
	next := s.copy()
	for range 257 {
		next.Peers = append(next.Peers, directLANTestPeer(t))
	}
	if e := s.save(next); e != nil {
		t.Fatal("unlimited admission retained old256 ceiling", e)
	}
	before, e := os.ReadFile(s.path)
	if e != nil {
		t.Fatal(e)
	}
	policy.Logical["trustedPeers"] = capacity.Limited(1)
	if e = applyLANTestPolicy(t, c, policy); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(s.path)
	if string(before) != string(after) {
		t.Fatal("lowering policy removed peers")
	}
	if s.transportPeerLimit() != 1 {
		t.Fatal("transport admission did not update")
	}
	tooMany := s.copy()
	tooMany.Peers = append(tooMany.Peers, directLANTestPeer(t))
	if e = s.save(tooMany); !errors.Is(e, directlan.ErrCapacity) {
		t.Fatal("new pair exceeded lowered limit", e)
	}
	fewer := s.copy()
	fewer.Peers = fewer.Peers[1:]
	if e = s.save(fewer); e != nil {
		t.Fatal("lowering blocked explicit revocation", e)
	}
	if _, e = readDirectLANStore(s.path, s.currentCapacity().bytes, 1); e != nil {
		t.Fatal("saved peers became unreadable under lower admission limit", e)
	}
}
func TestDirectLANSelectedResourceBudgets(t *testing.T) {
	p := capacity.Defaults()
	p.Resources["tcpConnections"] = capacity.Limited(700)
	p.Resources["udpSessions"] = capacity.Limited(300)
	p.Resources["materializedListeners"] = capacity.Limited(91)
	p.Resources["transferPending"] = capacity.Limited(77)
	p.Resources["udpQueuePackets"] = capacity.Limited(123)
	got := directRuntimeResources(p)
	if got.Flows != 1008 || got.Listeners != 93 || got.Invitations != 77 || got.PacketQueue != 123 {
		t.Fatal("selected resource budget ignored", got)
	}
}
func TestDirectLANStoragePolicyCannotStrandPrivateState(t *testing.T) {
	c := openLANTestCore(t)
	s := configureDirectLANTest(t, c)
	before, e := os.ReadFile(s.path)
	if e != nil {
		t.Fatal(e)
	}
	p := capacity.Defaults()
	p.Resources["lanStateBytes"] = capacity.Limited(int64(len(before) - 1))
	if e = applyLANTestPolicy(t, c, p); networkErrorCode(e) != "policy_in_use" {
		t.Fatal("reduced budget stranded private state", e)
	}
	if s.currentCapacity().bytes != capacity.Defaults().Number("resources", "lanStateBytes") {
		t.Fatal("rejected policy updated store limits")
	}
}
