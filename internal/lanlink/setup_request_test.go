package lanlink

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func requestSetupFixture(t *testing.T, verification string) (Identity, Identity, SetupHello, SetupPlan, time.Time) {
	t.Helper()
	a, b, old, _, now := setupFixture(t)
	pa, _ := func() (SetupParticipant, bool) {
		for _, p := range old.Participants {
			if p.Key == a.PublicKey() {
				return p, true
			}
		}
		return SetupParticipant{}, false
	}()
	var pb SetupParticipant
	for _, p := range old.Participants {
		if p.Key == b.PublicKey() {
			pb = p
		}
	}
	pa.Protocol, pa.Mode, pa.Verification = 2, "outbound", verification
	pa.AllowHost, pa.ExistingHost, pa.Relay = false, false, nil
	pa.Bootstrap = netip.AddrPortFrom(pa.Scope.LocalAddress, 0)
	pb.Protocol, pb.Mode, pb.Verification = 2, "receive", verification
	pa.Expires, pb.Expires = now.Add(20*time.Minute), now.Add(20*time.Minute)
	hello, e := NewSetupHello(pa, pb)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := NewSetupPlan(pa, pb, 1, pb.Key, *pb.Relay, now, pa.Expires)
	if e != nil {
		t.Fatal(e)
	}
	return a, b, hello, plan, now
}
func TestSetupRequestVersionLifetimeAndProofBinding(t *testing.T) {
	a, b, hello, plan, now := requestSetupFixture(t, "tofu")
	if hello.Version != 2 || plan.Version != 2 || plan.Verification != "tofu" || len(plan.RequestID) != 64 || plan.Expires.Sub(plan.Issued) != 20*time.Minute {
		t.Fatal("request transcript incomplete")
	}
	proof, e := SealSetupHello(a, hello, now)
	if e != nil || OpenSetupHello(b, hello, a.PublicKey(), proof, now) != nil {
		t.Fatal("hello proof", e)
	}
	frame, e := SealSetupMessage(a, plan, SetupActionApprove, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, now.Add(19*time.Minute)); e != nil {
		t.Fatal("review cannot last 19 minutes", e)
	}
	if _, e = OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, plan.Expires); e == nil {
		t.Fatal("approval survived expiry")
	}
	raw, _ := plan.Canonical()
	if !bytes.Contains(raw, []byte("setup plan v2")) {
		t.Fatal("v2 domain missing")
	}
	for name, mutate := range map[string]func(*SetupPlan){
		"downgrade": func(p *SetupPlan) { p.Version = 1; p.Domain = setupPlanDomain },
		"trust":     func(p *SetupPlan) { p.Verification = "full-sha256" },
		"request":   func(p *SetupPlan) { p.RequestID = strings.Repeat("c", 64) },
		"extension": func(p *SetupPlan) { p.Expires = p.Expires.Add(time.Second) },
		"role": func(p *SetupPlan) {
			if p.Participants[0].Mode == "receive" {
				p.Participants[0].Mode = "request"
			} else {
				p.Participants[0].Mode = "receive"
			}
		},
		"scope":   func(p *SetupPlan) { p.Participants[0].Scope.ConsentRevision = strings.Repeat("d", 64) },
		"session": func(p *SetupPlan) { p.Participants[0].SessionID = strings.Repeat("e", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneSetupTestPlan(plan)
			mutate(&changed)
			if _, e := OpenSetupMessage(b, changed, a.PublicKey(), SetupActionApprove, frame, now); e == nil {
				t.Fatal("accepted changed approval binding")
			}
		})
	}
}
func TestSetupRequestRequiresExplicitCompatibleModes(t *testing.T) {
	_, _, hello, _, now := requestSetupFixture(t, "full-sha256")
	for _, mutate := range []func(*SetupParticipant){
		func(p *SetupParticipant) { p.Protocol = 0; p.Mode = ""; p.Verification = "" },
		func(p *SetupParticipant) { p.Verification = "tofu" },
		func(p *SetupParticipant) { p.Protocol = 3 },
		func(p *SetupParticipant) { p.Expires = now.Add(20*time.Minute + time.Second) },
		func(p *SetupParticipant) { p.Mode = "outbound"; p.AllowHost = true },
	} {
		changed := cloneSetupParticipant(hello.Participants[0])
		mutate(&changed)
		h, e := NewSetupHello(changed, hello.Participants[1])
		if e == nil && h.Validate(now) == nil {
			t.Fatal("accepted incompatible/malformed participant")
		}
	}
}
func TestSetupLegacyCanonicalDoesNotAcquireRequestFields(t *testing.T) {
	_, _, hello, plan, _ := setupFixture(t)
	for _, raw := range []func() ([]byte, error){hello.Canonical, plan.Canonical} {
		b, e := raw()
		if e != nil {
			t.Fatal(e)
		}
		for _, field := range []string{"request_id", "verification", "protocol", "mode"} {
			if bytes.Contains(b, []byte(`"`+field+`"`)) {
				t.Fatal("v1 encoding changed", field)
			}
		}
	}
}
