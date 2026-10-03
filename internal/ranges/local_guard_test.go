package ranges

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDynamicLocalLeaseGuardsFlowWithoutChangingFixedPlan(t *testing.T) {
	var calls atomic.Int32
	var lease atomic.Pointer[time.Time]
	live := time.Now().Add(time.Hour)
	lease.Store(&live)
	e := newTestEngine(t, Options{LocalGuard: func(id string) error {
		calls.Add(1)
		if id != "leased" {
			return errors.New("wrong grant")
		}
		until := lease.Load()
		if until == nil || !time.Now().Before(*until) {
			return errors.New("lease expired")
		}
		return nil
	}})
	rule := testPolicy(t, "leased", "8080")
	rule.UntilRevoked = true
	rule.ExpiresAt = time.Time{}
	replaceTestPlan(t, e, rule)
	if handler, handled := e.Handle(testSource, endpoint(8080)); handler == nil || !handled || calls.Load() != 0 {
		t.Fatal("stack callback ran a local guard or rejected valid fixed scope")
	}
	f := &flow{engine: e, permit: e.current.Load().permits[0], source: testSource, destination: endpoint(8080), ctx: context.Background()}
	if err := f.permitted(); err != nil {
		t.Fatal(err)
	}
	old := e.current.Load()
	expired := time.Now().Add(-time.Second)
	lease.Store(&expired)
	if err := f.permitted(); err == nil {
		t.Fatal("already-open flow retained permission after renewable lease expiry")
	}
	renewed := time.Now().Add(time.Hour)
	lease.Store(&renewed)
	if err := f.permitted(); err != nil || e.current.Load() != old {
		t.Fatal("dynamic check replaced fixed plan or rejected live lease", err)
	}
	// This direct guard check models a still-valid renewal only. The owning Core
	// rejects renewing an already expired/stopped grant and revokes its flows.
}
