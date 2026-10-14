package directlan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/device"
)

// A currentness gate is not authentication. These inert cells exercise only
// rejection predicates; no verified exchange or transcript is manufactured.
func diagnosticCurrentFixture() (*ContextAttempt, *contextAttemptData, chan struct{}, time.Time) {
	n := &Node{contextControl: true, started: true, contextAttempts: make(map[contextAttemptKey]*ContextAttempt)}
	g := &runtimeGeneration{origin: &generationOrigin{}, controlPeers: make(map[string]*contextPeerRegistration)}
	g.controlOpen.Store(true)
	n.generation.Store(g)
	registration := &contextPeerRegistration{g: g, peer: Peer{Key: "synthetic-peer"}}
	g.controlPeers[registration.peer.Key] = registration
	d := &contextAttemptData{n: n, g: g, origin: g.origin, registration: registration, key: contextAttemptKey{peer: registration.peer.Key}, epoch: NewContextEpoch(), nodeDone: make(chan struct{})}
	a := &ContextAttempt{cell: &contextAttemptCell{cancelled: make(chan struct{})}}
	a.cell.original = a
	a.cell.data.Store(d)
	n.contextAttempts[d.key] = a
	return a, d, make(chan struct{}), time.Now().Add(time.Hour)
}

func TestContextClaimDiagnosticCurrentnessBranches(t *testing.T) {
	cases := []struct {
		name     string
		locked   bool
		change   func(**ContextAttempt, **contextAttemptData, *chan struct{}, *time.Time)
		response bool
	}{
		{"initial-attempt-missing", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) { *a = nil }, false},
		{"initial-data-missing", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) { *d = nil }, false},
		{"initial-epoch", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			(*d).epoch.Invalidate()
		}, false},
		{"initial-exchange-done", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			close(*done)
		}, false},
		{"initial-attempt-cancelled", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			(*a).Cancel()
		}, false},
		{"initial-deadline", false, func(a **ContextAttempt, d **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			*deadline = time.Now().Add(-time.Hour)
		}, false},
		{"node-mode", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.contextControl = false
		}, false},
		{"node-closed", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.closed = true
		}, false},
		{"node-closing", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.closing.Store(true)
		}, false},
		{"node-recovery", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.recovery = true
		}, false},
		{"node-not-started", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.started = false
		}, false},
		{"generation-identity", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.n.generation.Store(&runtimeGeneration{})
		}, false},
		{"generation-sealed", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.sealed = true
		}, false},
		{"generation-engine", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.engine.Store(&device.OwnedDevice{})
		}, false},
		{"generation-bind", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.bind = &lanBind{}
		}, false},
		{"generation-tunnel", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.tunnel = &userspaceTunnel{}
		}, false},
		{"generation-control-closed", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.controlOpen.Store(false)
		}, false},
		{"origin-identity", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.g.origin = &generationOrigin{}
		}, false},
		{"registration-missing", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.registration = nil
		}, false},
		{"registration-generation", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.registration.g = &runtimeGeneration{}
		}, false},
		{"registration-identity", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			delete(d.g.controlPeers, d.key.peer)
		}, false},
		{"registration-peer", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.registration.peer.Key = "synthetic-other"
		}, false},
		{"attempt-identity", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			delete(d.n.contextAttempts, d.key)
		}, false},
		{"attempt-data", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			a.cell.data.Store(nil)
		}, false},
		{"locked-epoch", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			d.epoch.Invalidate()
		}, false},
		{"locked-exchange-done", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			close(*done)
		}, false},
		{"locked-attempt-cancelled", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			a.Cancel()
		}, false},
		{"node-done", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			ch := make(chan struct{})
			close(ch)
			d.nodeDone = ch
		}, false},
		{"locked-deadline", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
			*deadline = time.Now().Add(-time.Hour)
		}, false},
		{"response-unclaimed", true, func(ap **ContextAttempt, dp **contextAttemptData, done *chan struct{}, deadline *time.Time) {
			a, d := *ap, *dp
			_, _ = a, d
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, observed := range []bool{false, true} {
				a, d, done, deadline := diagnosticCurrentFixture()
				tc.change(&a, &d, &done, &deadline)
				var result ContextClaimObservation
				var observation *ContextClaimObservation
				if observed {
					observation = &result
				}
				var current bool
				if tc.locked {
					d.n.mu.Lock()
					d.g.mu.Lock()
					current = contextCurrentLockedObserved(a, d, d.epoch, deadline, done, tc.response, observation)
					d.g.mu.Unlock()
					d.n.mu.Unlock()
				} else {
					var epoch *ContextEpoch
					if d != nil {
						epoch = d.epoch
					}
					current = tryContextCurrentObserved(a, d, epoch, deadline, done, tc.response, observation)
				}
				if current {
					t.Fatal("negative branch admitted")
				}
				if observed && result.Site != ContextClaimSite(tc.name) {
					t.Fatalf("site=%s want=%s", result.Site, tc.name)
				}
				if !observed && result.Site != "" {
					t.Fatal("nil observation wrote a result")
				}
			}
		})
	}
	for _, observed := range []bool{false, true} {
		a, d, done, deadline := diagnosticCurrentFixture()
		var result ContextClaimObservation
		var observation *ContextClaimObservation
		if observed {
			observation = &result
		}
		if !tryContextCurrentObserved(a, d, d.epoch, deadline, done, false, observation) || a.cancelled() || a.cell.claimed.Load() {
			t.Fatal("positive gate changed attempt authority")
		}
		if observed && result.Site != "current" {
			t.Fatal("positive currentness not recorded")
		}
		a.cell.claimed.Store(true)
		if !tryContextCurrentObserved(a, d, d.epoch, deadline, done, true, observation) {
			t.Fatal("current response rejected")
		}
	}
}

func TestContextClaimDiagnosticPostClaimEpoch(t *testing.T) {
	for _, valid := range []bool{false, true} {
		for _, observed := range []bool{false, true} {
			a, d, _, _ := diagnosticCurrentFixture()
			if !valid {
				d.epoch.Invalidate()
			}
			var result ContextClaimObservation
			var observation *ContextClaimObservation
			if observed {
				observation = &result
			}
			if got := claimContextEpochCurrent(a, d.epoch, observation); got != valid {
				t.Fatal("epoch gate changed")
			}
			if !a.cell.claimed.Load() || a.cancelled() == valid {
				t.Fatal("claim/cancel signals changed")
			}
			if observed && !valid && result.Site != "post-claim-epoch" {
				t.Fatal("post-claim boundary lost")
			}
		}
	}
}

func TestContextClaimDiagnosticAuthenticClaimParity(t *testing.T) {
	for _, observed := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(map[bool]string{false: "nil", true: "observed"}[observed]+map[bool]string{false: "-accept", true: "-reject"}[reject], func(t *testing.T) {
				called := false
				var f *contextExchangeFixture
				f = contextExchangeNew(t, func(_ context.Context, a *ContextAttempt, v VerifiedContextExchange) (ContextResponse, error) {
					called = true
					var result ContextClaimObservation
					var observation *ContextClaimObservation
					if observed {
						observation = &result
					}
					if reject {
						f.n.mu.Lock()
						defer f.n.mu.Unlock()
					}
					_, err := v.TryClaimForObserved(a, observation)
					if (err != nil) != reject {
						t.Error("observation changed claim result")
					}
					if observed {
						want := ContextClaimSite("claimed")
						if reject {
							want = "node-lock"
						}
						if result.Site != want {
							t.Errorf("site=%s want=%s", result.Site, want)
						}
					}
					if _, err := v.TryClaimFor(a); !errors.Is(err, ErrUntrusted) {
						t.Error("observation changed single-use claim")
					}
					return ContextResponse{}, ErrUnavailable
				})
				f.arm(t)
				_, _ = f.send(t)
				if !called {
					t.Fatal("authentic completion not reached")
				}
			})
		}
	}
}

func TestContextClaimDiagnosticObserverIsolation(t *testing.T) {
	first := ContextClaimObservation{Site: "claimed"}
	second := ContextClaimObservation{}
	if _, err := (VerifiedContextExchange{}).TryClaimForObserved(nil, &second); !errors.Is(err, ErrUntrusted) || second.Site != "proof-empty" || first.Site != "claimed" {
		t.Fatal("observations were shared or changed authority")
	}
	if _, err := (VerifiedContextExchange{}).TryClaimForObserved(nil, nil); !errors.Is(err, ErrUntrusted) || first.Site != "claimed" || second.Site != "proof-empty" {
		t.Fatal("nil observer altered another result")
	}
}

func TestContextClaimDiagnosticNonwaitingLocks(t *testing.T) {
	for _, name := range []string{"node-lock", "generation-lock"} {
		for _, observed := range []bool{false, true} {
			a, d, done, deadline := diagnosticCurrentFixture()
			var result ContextClaimObservation
			var observation *ContextClaimObservation
			if observed {
				observation = &result
			}
			unlock := d.n.mu.Unlock
			if name == "node-lock" {
				d.n.mu.Lock()
			} else {
				d.g.mu.Lock()
				unlock = d.g.mu.Unlock
			}
			returned := make(chan bool, 1)
			go func() { returned <- tryContextCurrentObserved(a, d, d.epoch, deadline, done, false, observation) }()
			select {
			case current := <-returned:
				if current {
					t.Error("contended gate accepted")
				}
			case <-time.After(time.Second):
				unlock()
				<-returned
				t.Fatal("observation waited for held lock")
			}
			unlock()
			if observed && result.Site != ContextClaimSite(name) {
				t.Fatalf("site=%s want=%s", result.Site, name)
			}
			if a.cancelled() || a.cell.claimed.Load() {
				t.Fatal("currentness observation changed attempt authority")
			}
		}
	}
}
