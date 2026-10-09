package resourcegrant

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestManagementFenceExactImmutableScope(t *testing.T) {
	m := managementGrantFixture()
	now := time.Unix(150, 250000000)
	f, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	selector, relationship := f.selector, m.Record.Relationship
	m.Record.Actions[0] = "arbitrary"
	m.Record.Fields[0] = "*"
	m.Record.ID = "mutated"
	for _, action := range []string{Inspect, PreviewAction, ApplyAction, StatusAction} {
		if !f.admit(selector, action, relationship, managementFenceAt(now)) {
			t.Fatalf("denied exact %s", action)
		}
	}
	wrong := selector
	wrong.GrantRevision++
	if f.admit(wrong, ApplyAction, relationship, managementFenceAt(now)) {
		t.Fatal("revision substituted")
	}
	wrong = selector
	wrong.ProtocolVersion = ProtocolVersion
	if f.admit(wrong, ApplyAction, relationship, managementFenceAt(now)) {
		t.Fatal("legacy protocol admitted")
	}
	wrong = selector
	wrong.GrantID = fixtureRecord().Target.ResourceID
	if f.admit(wrong, ApplyAction, relationship, managementFenceAt(now)) {
		t.Fatal("grant substituted")
	}
	wrong = selector
	wrong.Target.ResourceID = fixtureRecord().ID
	if f.admit(wrong, ApplyAction, relationship, managementFenceAt(now)) {
		t.Fatal("target substituted")
	}
	for _, mutate := range []func(*Relationship){
		func(r *Relationship) { r.PeerKey = r.TargetKey },
		func(r *Relationship) { r.TargetKey = r.PeerKey },
		func(r *Relationship) { r.PairBinding = r.TargetKey },
		func(r *Relationship) { r.Backend = "other" },
	} {
		r := relationship
		mutate(&r)
		if f.admit(selector, ApplyAction, r, managementFenceAt(now)) {
			t.Fatal("relationship substituted")
		}
	}
	for _, action := range []string{"", "*", "resource.apply", "revoke"} {
		if f.admit(selector, action, relationship, managementFenceAt(now)) {
			t.Fatal("arbitrary action admitted")
		}
	}
}

func TestManagementFenceTerminalLifetime(t *testing.T) {
	m := managementGrantFixture()
	now := time.Unix(150, 250000000)
	for _, tc := range []struct {
		at                 time.Time
		expired, uncertain bool
	}{
		{time.Unix(99, 0), false, true}, {time.Unix(190, 0), true, false}, {time.Unix(200, 0), true, false},
	} {
		f, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
		if err != nil {
			t.Fatal(err)
		}
		if f.admit(f.selector, ApplyAction, m.Record.Relationship, managementFenceAt(tc.at)) {
			t.Fatal("invalid time admitted")
		}
		expired, uncertain := f.TimingObservation(f.selector)
		if expired != tc.expired || uncertain != tc.uncertain {
			t.Fatal("terminal reason lost")
		}
		if f.admit(f.selector, ApplyAction, m.Record.Relationship, managementFenceAt(now)) {
			t.Fatal("terminal time reopened")
		}
		wrong := f.selector
		wrong.GrantRevision++
		if e, u := f.TimingObservation(wrong); e || u {
			t.Fatal("other selector read terminal reason")
		}
	}
	f, err := newManagementFenceBefore(m, time.Unix(300, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if !f.deadline.Equal(time.Unix(m.Record.ExpiresAt, 0)) {
		t.Fatal("original expiry extended")
	}
	for _, cutoff := range []time.Time{{}, now, now.Add(-time.Second)} {
		if _, err := newManagementFenceBefore(m, cutoff, now); err == nil {
			t.Fatal("invalid cutoff accepted")
		}
	}
	for _, mutate := range []func(*ManagementRecord){
		func(m *ManagementRecord) { m.Record.State = Revoked },
		func(m *ManagementRecord) { m.Record.Actions = []string{Inspect} },
		func(m *ManagementRecord) { m.Record.Fields = []string{FilesField} },
		func(m *ManagementRecord) { m.Scope = "inspect" },
	} {
		changed := managementGrantFixture()
		mutate(&changed)
		if _, err := newManagementFenceBefore(changed, time.Unix(190, 0), now); err == nil {
			t.Fatal("invalid record minted fence")
		}
	}
}

func TestManagementFenceCloseAndSeparateDisclosure(t *testing.T) {
	m := managementGrantFixture()
	now := time.Unix(150, 0)
	f, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if !f.admit(f.selector, ApplyAction, m.Record.Relationship, managementFenceAt(now)) {
		t.Fatal("initial admission denied")
	}
	f.Close()
	f.Close()
	for _, action := range []string{Inspect, PreviewAction, ApplyAction, StatusAction} {
		if f.admit(f.selector, action, m.Record.Relationship, managementFenceAt(now)) {
			t.Fatal("preadmission granted later disclosure")
		}
	}
	var nilFence *ManagementFence
	nilFence.Close()
	if nilFence.admit(f.selector, ApplyAction, m.Record.Relationship, managementFenceAt(now)) {
		t.Fatal("nil admitted")
	}
	if e, u := nilFence.TimingObservation(f.selector); e || u {
		t.Fatal("nil timing")
	}
}

func TestManagementFenceConcurrentTerminalClose(t *testing.T) {
	m := managementGrantFixture()
	now := time.Unix(150, 0)
	f, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				f.admit(f.selector, ApplyAction, m.Record.Relationship, managementFenceAt(now))
			}
		}()
	}
	f.Close()
	wg.Wait()
	if f.admit(f.selector, StatusAction, m.Record.Relationship, managementFenceAt(now)) {
		t.Fatal("close lost")
	}
}

func TestManagementListenerHelloVersionMatrix(t *testing.T) {
	for version := 0; version < 256; version++ {
		reply, supported, err := ManagementListenerHelloResponse(byte(version))
		if version == 0 {
			if err != ErrProtocol || supported {
				t.Fatal("zero accepted")
			}
			continue
		}
		if err != nil || supported != (version == ManagementProtocolVersion) {
			t.Fatalf("version %d", version)
		}
		got := ReadManagementHelloResponse(bytes.NewReader(reply[:]))
		if supported && got != nil || !supported && got != ErrUnsupported {
			t.Fatalf("reply %d: %v", version, got)
		}
		if version == ProtocolVersion && ReadHelloResponse(bytes.NewReader(reply[:])) != ErrUnsupported {
			t.Fatal("v1 client not explicitly rejected")
		}
	}
	legacy, ok, err := HelloResponse(ProtocolVersion)
	if err != nil || !ok {
		t.Fatal("legacy changed")
	}
	compat, ok, err := ManagementHelloResponse(ProtocolVersion)
	if err != nil || !ok || compat != legacy {
		t.Fatal("pure compatibility changed")
	}
}

// The test clock is private; production samples time.Now only after its
// revocation CAS. Closing from that callback simulates revocation immediately
// after the ordering point without sleeps or scheduler assumptions.
func TestManagementFenceClockAfterRevocationOrdering(t *testing.T) {
	m := managementGrantFixture()
	now := time.Unix(150, 0)
	f, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if !f.admit(f.selector, ApplyAction, m.Record.Relationship, func() time.Time {
		calls++
		f.Close()
		return now
	}) || calls != 1 {
		t.Fatal("later close retracted ordered admission")
	}
	if f.admit(f.selector, ApplyAction, m.Record.Relationship, func() time.Time {
		calls++
		return now
	}) || calls != 1 {
		t.Fatal("closed fence sampled clock or admitted")
	}
	expired, err := newManagementFenceBefore(m, time.Unix(190, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if expired.admit(expired.selector, ApplyAction, m.Record.Relationship, func() time.Time {
		return time.Unix(190, 0)
	}) {
		t.Fatal("fresh clock did not reject expired ordered admission")
	}
}

func managementFenceAt(now time.Time) func() time.Time {
	return func() time.Time { return now }
}
