package lanlink

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func setupFixture(t *testing.T) (Identity, Identity, SetupHello, SetupPlan, time.Time) {
	t.Helper()
	a, b := GenerateIdentity(), GenerateIdentity()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	participant := func(identity Identity, suffix string, address string) SetupParticipant {
		local := netip.MustParseAddr(address)
		return SetupParticipant{
			SessionID: strings.Repeat(suffix, 64), Key: identity.PublicKey(), Alias: "test-device-" + suffix,
			Bootstrap: netip.AddrPortFrom(local, 54440),
			Scope:     SetupScope{LocalAddress: local, Prefix: netip.MustParsePrefix("192.168.50.0/24"), ConsentRevision: strings.Repeat(suffix, 64)},
			AllowHost: true,
			Relay:     &TrustedRelay{Address: netip.AddrPortFrom(local, 54441), CertificateSHA256: strings.Repeat(suffix, 64)},
			Expires:   now.Add(MaxSetupLifetime),
		}
	}
	pa, pb := participant(a, "a", "192.168.50.10"), participant(b, "b", "192.168.50.20")
	hello, err := NewSetupHello(pa, pb)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewSetupPlan(pa, pb, 1, a.PublicKey(), *pa.Relay, now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return a, b, hello, plan, now
}

func cloneSetupTestPlan(p SetupPlan) SetupPlan {
	for i := range p.Participants {
		p.Participants[i] = cloneSetupParticipant(p.Participants[i])
	}
	return p
}

func TestSetupCanonicalTranscriptAndFullFingerprint(t *testing.T) {
	_, _, hello, plan, now := setupFixture(t)
	if plan.Participants[0].Key >= plan.Participants[1].Key || hello.Participants[0].Key >= hello.Participants[1].Key {
		t.Fatal("participant order is not canonical")
	}
	zone := time.FixedZone("synthetic-offset", 9*60*60)
	a, b := cloneSetupParticipant(plan.Participants[1]), cloneSetupParticipant(plan.Participants[0])
	a.Expires, b.Expires = a.Expires.In(zone), b.Expires.In(zone)
	reordered, err := NewSetupPlan(a, b, plan.Revision, plan.HostKey, plan.Relay, plan.Issued.In(zone), plan.Expires.In(zone))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	other, err := reordered.Canonical()
	if err != nil || !bytes.Equal(raw, other) {
		t.Fatal("equivalent participant order/time zones changed transcript", err)
	}
	fingerprint, err := plan.Fingerprint()
	want := sha256.Sum256(raw)
	if err != nil || len(fingerprint) != 64 || fingerprint != hex.EncodeToString(want[:]) {
		t.Fatal("fingerprint is not full canonical SHA-256", err)
	}
	helloFingerprint, err := hello.Fingerprint()
	if err != nil || helloFingerprint == fingerprint || len(helloFingerprint) != 64 {
		t.Fatal("hello and plan fingerprints are not separated", err)
	}
	if bytes.Contains(raw, []byte("interface_index")) || bytes.Contains(raw, []byte("synthetic-offset")) || !bytes.Contains(raw, []byte("consent_revision")) {
		t.Fatal("transcript leaked local index or omitted opaque scope consent")
	}
	if plan.Validate(now) != nil || plan.Validate(plan.Expires) == nil || plan.Validate(now.Add(-time.Nanosecond)) == nil {
		t.Fatal("plan liveness is not exact")
	}
}

func TestSetupCopiesCandidatePointers(t *testing.T) {
	_, _, hello, plan, _ := setupFixture(t)
	before, _ := plan.Fingerprint()
	participant, ok := plan.Participant(plan.HostKey)
	if !ok {
		t.Fatal("missing host")
	}
	participant.Relay.CertificateSHA256 = strings.Repeat("c", 64)
	hello.Participants[0].Relay.CertificateSHA256 = strings.Repeat("d", 64)
	after, err := plan.Fingerprint()
	if err != nil || before != after {
		t.Fatal("returned participant or independently built hello edited plan", err)
	}
}

func TestSetupEveryReviewedFieldBindsFingerprint(t *testing.T) {
	_, _, _, base, _ := setupFixture(t)
	before, _ := base.Fingerprint()
	mutations := map[string]func(*SetupPlan){
		"session-id": func(p *SetupPlan) { p.Participants[0].SessionID = strings.Repeat("c", 64) },
		"alias":      func(p *SetupPlan) { p.Participants[0].Alias = "another-explicit-alias" },
		"bootstrap": func(p *SetupPlan) {
			p.Participants[0].Bootstrap = netip.AddrPortFrom(p.Participants[0].Scope.LocalAddress, 54442)
		},
		"scope-prefix":   func(p *SetupPlan) { p.Participants[0].Scope.Prefix = netip.MustParsePrefix("192.168.50.0/23") },
		"scope-consent":  func(p *SetupPlan) { p.Participants[0].Scope.ConsentRevision = strings.Repeat("c", 64) },
		"session-expiry": func(p *SetupPlan) { p.Participants[0].Expires = p.Participants[0].Expires.Add(-time.Second) },
		"existing-host":  func(p *SetupPlan) { p.Participants[0].ExistingHost = true },
		"revision":       func(p *SetupPlan) { p.Revision++ },
		"issued":         func(p *SetupPlan) { p.Issued = p.Issued.Add(time.Second) },
		"expires":        func(p *SetupPlan) { p.Expires = p.Expires.Add(-time.Second) },
		"host": func(p *SetupPlan) {
			for _, participant := range p.Participants {
				if participant.Key != p.HostKey {
					p.HostKey, p.Relay = participant.Key, *participant.Relay
					break
				}
			}
		},
		"pin": func(p *SetupPlan) {
			p.Relay.CertificateSHA256 = strings.Repeat("c", 64)
			for i := range p.Participants {
				if p.Participants[i].Key == p.HostKey {
					p.Participants[i].Relay.CertificateSHA256 = p.Relay.CertificateSHA256
				}
			}
		},
		"non-host-permission": func(p *SetupPlan) {
			for i := range p.Participants {
				if p.Participants[i].Key != p.HostKey {
					p.Participants[i].AllowHost = false
				}
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := cloneSetupTestPlan(base)
			mutate(&p)
			after, err := p.Fingerprint()
			if err != nil || after == before {
				t.Fatal("reviewed field not bound", err)
			}
		})
	}
}

func TestSetupRejectsInvalidPlanAndScope(t *testing.T) {
	_, _, _, base, now := setupFixture(t)
	mutations := map[string]func(*SetupPlan){
		"version":              func(p *SetupPlan) { p.Version++ },
		"domain":               func(p *SetupPlan) { p.Domain = setupHelloDomain },
		"backend":              func(p *SetupPlan) { p.Backend = "tailnet" },
		"revision-zero":        func(p *SetupPlan) { p.Revision = 0 },
		"self":                 func(p *SetupPlan) { p.Participants[1].Key = p.Participants[0].Key },
		"same-session":         func(p *SetupPlan) { p.Participants[1].SessionID = p.Participants[0].SessionID },
		"unsorted":             func(p *SetupPlan) { p.Participants[0], p.Participants[1] = p.Participants[1], p.Participants[0] },
		"bad-key":              func(p *SetupPlan) { p.Participants[0].Key = "short" },
		"empty-alias":          func(p *SetupPlan) { p.Participants[0].Alias = "" },
		"control-alias":        func(p *SetupPlan) { p.Participants[0].Alias = "line\nbreak" },
		"bidi-alias":           func(p *SetupPlan) { p.Participants[0].Alias = "right\u202eleft" },
		"separator-alias":      func(p *SetupPlan) { p.Participants[0].Alias = "line\u2028break" },
		"long-alias":           func(p *SetupPlan) { p.Participants[0].Alias = strings.Repeat("a", 81) },
		"zero-session":         func(p *SetupPlan) { p.Participants[0].SessionID = strings.Repeat("0", 64) },
		"uppercase-session":    func(p *SetupPlan) { p.Participants[0].SessionID = strings.Repeat("A", 64) },
		"no-scope-consent":     func(p *SetupPlan) { p.Participants[0].Scope.ConsentRevision = "" },
		"unmasked-prefix":      func(p *SetupPlan) { p.Participants[0].Scope.Prefix = netip.MustParsePrefix("192.168.50.1/24") },
		"wide-prefix":          func(p *SetupPlan) { p.Participants[0].Scope.Prefix = netip.MustParsePrefix("0.0.0.0/0") },
		"outside-prefix":       func(p *SetupPlan) { p.Participants[0].Scope.Prefix = netip.MustParsePrefix("192.168.51.0/24") },
		"mismatched-bootstrap": func(p *SetupPlan) { p.Participants[0].Bootstrap = netip.MustParseAddrPort("192.168.50.30:54440") },
		"privileged-bootstrap": func(p *SetupPlan) {
			p.Participants[0].Bootstrap = netip.AddrPortFrom(p.Participants[0].Scope.LocalAddress, 443)
		},
		"no-session-expiry":     func(p *SetupPlan) { p.Participants[0].Expires = time.Time{} },
		"long-session-expiry":   func(p *SetupPlan) { p.Participants[0].Expires = now.Add(MaxSetupLifetime + time.Nanosecond) },
		"beyond-session-expiry": func(p *SetupPlan) { p.Participants[0].Expires = p.Expires.Add(-time.Nanosecond) },
		"zero-lifetime":         func(p *SetupPlan) { p.Expires = p.Issued },
		"long-lifetime":         func(p *SetupPlan) { p.Expires = p.Issued.Add(MaxSetupLifetime + time.Nanosecond) },
		"unknown-host":          func(p *SetupPlan) { p.HostKey = strings.Repeat("e", 64) },
		"unoffered-pin":         func(p *SetupPlan) { p.Relay.CertificateSHA256 = strings.Repeat("e", 64) },
		"no-host-permission": func(p *SetupPlan) {
			for i := range p.Participants {
				p.Participants[i].AllowHost = false
			}
		},
		"no-candidates": func(p *SetupPlan) {
			for i := range p.Participants {
				p.Participants[i].Relay = nil
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := cloneSetupTestPlan(base)
			mutate(&p)
			if !errors.Is(p.Validate(now), ErrSetup) {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	for _, address := range []string{"0.0.0.0", "224.0.0.1", "192.0.2.1", "::ffff:192.168.50.10", "fe80::1", "fe80::1%synthetic", "ff02::1"} {
		p := base.Participants[0]
		p.Scope.LocalAddress = netip.MustParseAddr(address)
		if p.Validate(now) == nil {
			t.Fatal("invalid local scope accepted")
		}
	}
}

func TestSetupRoundTripEveryFiniteAction(t *testing.T) {
	a, b, _, plan, now := setupFixture(t)
	for _, action := range []string{SetupActionHelloProof, SetupActionApprove, SetupActionInvitation, SetupActionPaired, SetupActionConnectedReceipt, SetupActionCancel} {
		t.Run(action, func(t *testing.T) {
			var payload []byte
			if action == SetupActionInvitation {
				payload = []byte("synthetic-private-invitation")
			}
			for _, direction := range [][2]Identity{{a, b}, {b, a}} {
				frame, err := SealSetupMessage(direction[0], plan, action, payload, now)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(frame, []byte("synthetic-private-invitation")) || bytes.Contains(frame, []byte("192.168.50")) {
					t.Fatal("private payload or transcript sent in plaintext")
				}
				opened, err := OpenSetupMessage(direction[1], plan, direction[0].PublicKey(), action, frame, now)
				if err != nil || !bytes.Equal(opened, payload) {
					t.Fatal("round trip failed", err)
				}
				// The stateless codec deliberately leaves same-phase duplicate policy
				// to Core; successful decryption is not a local authorization event.
				if _, err := OpenSetupMessage(direction[1], plan, direction[0].PublicKey(), action, frame, now); err != nil {
					t.Fatal("codec incorrectly owns phase replay state", err)
				}
			}
		})
	}
}

func TestSetupRejectsTamperReflectionAndChangedBinding(t *testing.T) {
	a, b, _, plan, now := setupFixture(t)
	frame, err := SealSetupMessage(a, plan, SetupActionApprove, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"wrong-action": func() error {
			_, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionCancel, frame, now)
			return err
		},
		"unknown-action": func() error {
			_, err := OpenSetupMessage(b, plan, a.PublicKey(), "automatic-approve", frame, now)
			return err
		},
		"reflected": func() error {
			_, err := OpenSetupMessage(a, plan, b.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"self-sender": func() error {
			_, err := OpenSetupMessage(b, plan, b.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"unknown-recipient": func() error {
			_, err := OpenSetupMessage(GenerateIdentity(), plan, a.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"unknown-sender": func() error {
			_, err := OpenSetupMessage(b, plan, GenerateIdentity().PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"expired": func() error {
			_, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, plan.Expires)
			return err
		},
		"future": func() error {
			_, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, now.Add(-time.Nanosecond))
			return err
		},
		"changed-session": func() error {
			p := cloneSetupTestPlan(plan)
			p.Participants[0].SessionID = strings.Repeat("c", 64)
			_, err := OpenSetupMessage(b, p, a.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"changed-revision": func() error {
			p := plan
			p.Revision++
			_, err := OpenSetupMessage(b, p, a.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"changed-alias": func() error {
			p := plan
			p.Participants[1].Alias = "changed-alias"
			_, err := OpenSetupMessage(b, p, a.PublicKey(), SetupActionApprove, frame, now)
			return err
		},
		"tampered-ciphertext": func() error {
			var envelope pairEnvelope
			if json.Unmarshal(frame, &envelope) != nil {
				t.Fatal("fixture envelope")
			}
			envelope.Box[len(envelope.Box)-1] ^= 1
			changed, _ := json.Marshal(envelope)
			_, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, changed, now)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(check(), ErrSetup) {
				t.Fatal("invalid proof accepted")
			}
		})
	}
}

func TestSetupRejectsAuthenticatedWrongEnvelopeFields(t *testing.T) {
	a, b, _, plan, now := setupFixture(t)
	base, err := setupMessageBinding(a, plan, a.PublicKey(), SetupActionApprove, now)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*setupMessage){
		"version":             func(m *setupMessage) { m.Version++ },
		"domain":              func(m *setupMessage) { m.Domain = setupHelloProofDomain },
		"action":              func(m *setupMessage) { m.Action = SetupActionCancel },
		"sender":              func(m *setupMessage) { m.Sender = b.PublicKey() },
		"recipient":           func(m *setupMessage) { m.Recipient = a.PublicKey() },
		"sender-session":      func(m *setupMessage) { m.SenderSessionID = strings.Repeat("c", 64) },
		"recipient-session":   func(m *setupMessage) { m.RecipientSessionID = strings.Repeat("c", 64) },
		"revision":            func(m *setupMessage) { m.Revision++ },
		"fingerprint":         func(m *setupMessage) { m.Fingerprint = strings.Repeat("c", 64) },
		"expiry":              func(m *setupMessage) { m.Expires = m.Expires.Add(time.Second) },
		"expiry-zone":         func(m *setupMessage) { m.Expires = m.Expires.In(time.FixedZone("synthetic", 3600)) },
		"empty-payload-alias": func(m *setupMessage) { m.Payload = make([]byte, 0) },
		"unexpected-payload":  func(m *setupMessage) { m.Payload = []byte("unrequested-data") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := base
			mutate(&m)
			frame, _, err := sealMessage(a, b.PublicKey(), m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, now); !errors.Is(err, ErrSetup) {
				t.Fatal("incorrect authenticated field accepted", err)
			}
		})
	}
}

func TestSetupRejectsAmbiguousAndOversizedMessages(t *testing.T) {
	a, b, _, plan, now := setupFixture(t)
	m, _ := setupMessageBinding(a, plan, a.PublicKey(), SetupActionApprove, now)
	plain, _ := json.Marshal(m)
	for name, changed := range map[string][]byte{
		"duplicate":       append([]byte(`{"action":"approve",`), plain[1:]...),
		"case-alias":      bytes.Replace(plain, []byte(`"action"`), []byte(`"Action"`), 1),
		"unknown-field":   append([]byte(`{"local_permission":true,`), plain[1:]...),
		"whitespace":      append([]byte(" "), plain...),
		"trailing-object": append(bytes.Clone(plain), []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			pub, _ := parseNodePublic(b.PublicKey())
			frame, _ := json.Marshal(pairEnvelope{Sender: a.PublicKey(), Box: a.Key.SealTo(pub, changed)})
			if _, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, frame, now); !errors.Is(err, ErrSetup) {
				t.Fatal("noncanonical authenticated JSON accepted", err)
			}
		})
	}
	for _, payload := range [][]byte{nil, make([]byte, MaxSetupPayload+1)} {
		if _, err := SealSetupMessage(a, plan, SetupActionInvitation, payload, now); !errors.Is(err, ErrSetup) {
			t.Fatal("invalid invitation payload bound accepted", err)
		}
	}
	payload := bytes.Repeat([]byte("x"), MaxSetupPayload)
	frame, err := SealSetupMessage(a, plan, SetupActionInvitation, payload, now)
	if err != nil {
		t.Fatal("maximum bounded payload does not fit primitive", err)
	}
	if got, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionInvitation, frame, now); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("maximum bounded payload failed", err)
	}
	if _, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionApprove, make([]byte, maxPairMessage+1), now); !errors.Is(err, ErrSetup) {
		t.Fatal("oversized outer frame accepted", err)
	}
}

func TestSetupHelloProofBeforeHostElection(t *testing.T) {
	a, b, hello, plan, now := setupFixture(t)
	for i := range hello.Participants {
		hello.Participants[i].AllowHost = false
		hello.Participants[i].Relay = nil
	}
	frame, err := SealSetupHello(a, hello, now)
	if err != nil || OpenSetupHello(b, hello, a.PublicKey(), frame, now) != nil {
		t.Fatal("possession proof requires host election", err)
	}
	if OpenSetupHello(a, hello, b.PublicKey(), frame, now) == nil || OpenSetupHello(b, hello, b.PublicKey(), frame, now) == nil {
		t.Fatal("hello reflection accepted")
	}
	changed := hello
	changed.Participants[0].Scope.ConsentRevision = strings.Repeat("c", 64)
	if OpenSetupHello(b, changed, a.PublicKey(), frame, now) == nil {
		t.Fatal("hello accepted changed local consent")
	}
	changed = hello
	changed.Participants[1].SessionID = strings.Repeat("c", 64)
	if OpenSetupHello(b, changed, a.PublicKey(), frame, now) == nil {
		t.Fatal("hello replayed across sessions")
	}
	if OpenSetupHello(b, hello, a.PublicKey(), frame, hello.Participants[0].Expires) == nil {
		t.Fatal("hello accepted expired session")
	}
	if _, err := OpenSetupMessage(b, plan, a.PublicKey(), SetupActionHelloProof, frame, now); err == nil {
		t.Fatal("hello substituted for final plan proof")
	}
	planFrame, err := SealSetupMessage(a, plan, SetupActionHelloProof, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if OpenSetupHello(b, hello, a.PublicKey(), planFrame, now) == nil {
		t.Fatal("plan proof substituted for hello")
	}
}

func TestSetupSessionIDRandomnessAndCanonicalForm(t *testing.T) {
	a, err := NewSetupSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSetupSessionID()
	if err != nil || !validKey(a) || !validKey(b) || a == b {
		t.Fatal("invalid fresh session IDs", err)
	}
}

func TestSetupParticipantAliasLifetimeAndPortBoundaries(t *testing.T) {
	_, _, hello, _, now := setupFixture(t)
	base := hello.Participants[0]
	for _, alias := range []string{"", " example", "example ", "\u00a0example", "example\u3000", strings.Repeat("a", 81), strings.Repeat("日", 27), "bad\x00name", "bad\x7fname", "bad\x85name", "bad\u061cname", "bad\u200ename", "bad\u200fname", "bad\u202aname", "bad\u202ename", "bad\u2066name", "bad\u2069name", "bad\u2028name", "bad\u2029name", "bad\xff"} {
		p := cloneSetupParticipant(base)
		p.Alias = alias
		if p.Validate(now) == nil {
			t.Fatal("unsafe alias accepted")
		}
	}
	for _, port := range []uint16{0, 443, 54543, 54544, 54545} {
		p := cloneSetupParticipant(base)
		p.Bootstrap = netip.AddrPortFrom(p.Scope.LocalAddress, port)
		if p.Validate(now) == nil {
			t.Fatal("reserved bootstrap port accepted")
		}
		p = cloneSetupParticipant(base)
		p.Relay.Address = netip.AddrPortFrom(p.Scope.LocalAddress, port)
		if p.Validate(now) == nil {
			t.Fatal("reserved relay port accepted")
		}
	}
	p := cloneSetupParticipant(base)
	p.Relay.Address = p.Bootstrap
	if p.Validate(now) == nil {
		t.Fatal("bootstrap and relay socket collision accepted")
	}
	p = cloneSetupParticipant(base)
	p.Expires = now.Add(MaxSetupLifetime + time.Nanosecond)
	if p.Validate(now) == nil {
		t.Fatal("unbounded participant lifetime accepted")
	}
	p.Expires = now.Add(MaxSetupLifetime)
	if p.Validate(now) != nil || p.Validate(p.Expires) == nil {
		t.Fatal("exact participant lifetime boundary rejected")
	}
}
