package lanlink

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"tailscale.com/types/key"
)

func routePairFixture() (Identity, Identity, RemotePeer, RemotePeer) {
	a, b := GenerateIdentity(), GenerateIdentity()
	ar, br := key.NewNode(), key.NewNode()
	return a, b, RemotePeer{Peer: Peer{b.PublicKey(), "peer-b"}, ClientPrivate: ar, IncomingClientKey: keyString(br.Public())}, RemotePeer{Peer: Peer{a.PublicKey(), "peer-a"}, ClientPrivate: br, IncomingClientKey: keyString(ar.Public())}
}
func routeFixture(scope, address string) RouteCandidate {
	return RouteCandidate{TrustedRelay{netip.MustParseAddrPort(address), strings.Repeat("a", 64)}, scope}
}
func TestPairedRouteUpdateRoundTripDoesNotGrantPermission(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	candidates := []RouteCandidate{routeFixture("external", "192.0.2.20:443"), routeFixture("local", "192.168.50.2:54446")}
	frame, err := SealRouteUpdate(a, ab, 1, candidates, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(frame, []byte("192.168.50.2")) {
		t.Fatal("update not encrypted")
	}
	update, err := OpenRouteUpdate(b, ba, frame, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if routes, err := PermittedRoutes(update, nil, now); err != nil || len(routes) != 0 {
		t.Fatal("authentication granted permission", err)
	}
	ids := []string{candidates[0].ID(), candidates[1].ID()}
	routes, err := PermittedRoutes(update, ids, now)
	if err != nil || len(routes) != 2 || routes[0].Scope != "local" {
		t.Fatal("local-first exact permission failed", err)
	}
	for _, candidate := range candidates {
		if strings.Contains(update.Summary(), candidate.Relay.Address.String()) {
			t.Fatal("summary leaked endpoint")
		}
	}
}
func TestRouteUpdateRejectsReplayTamperExpiryAndPairChange(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	frame, err := SealRouteUpdate(a, ab, 7, []RouteCandidate{routeFixture("local", "192.168.50.2:54446")}, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"replay":    func() error { _, e := OpenRouteUpdate(b, ba, frame, 7, now); return e },
		"rollback":  func() error { _, e := OpenRouteUpdate(b, ba, frame, 8, now); return e },
		"expired":   func() error { _, e := OpenRouteUpdate(b, ba, frame, 0, now.Add(time.Hour)); return e },
		"future":    func() error { _, e := OpenRouteUpdate(b, ba, frame, 0, now.Add(-time.Second)); return e },
		"recipient": func() error { _, e := OpenRouteUpdate(GenerateIdentity(), ba, frame, 0, now); return e },
		"new-pair": func() error {
			changed := ba
			changed.ClientPrivate = key.NewNode()
			_, e := OpenRouteUpdate(b, changed, frame, 0, now)
			return e
		},
		"tampered": func() error {
			var env pairEnvelope
			if json.Unmarshal(frame, &env) != nil {
				t.Fatal("fixture")
			}
			env.Box[len(env.Box)-1] ^= 1
			raw, _ := json.Marshal(env)
			_, e := OpenRouteUpdate(b, ba, raw, 0, now)
			return e
		},
		"reflected": func() error { _, e := OpenRouteUpdate(a, ab, frame, 0, now); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if check() == nil {
				t.Fatal("accepted invalid update")
			}
		})
	}
}
func TestRouteUpdatePinChangesAndWithdrawalsDoNotReviveApproval(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	old := routeFixture("local", "192.168.50.2:54446")
	changed := old
	changed.Relay.CertificateSHA256 = strings.Repeat("b", 64)
	for _, candidates := range [][]RouteCandidate{nil, {changed}} {
		frame, err := SealRouteUpdate(a, ab, 2, candidates, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		update, err := OpenRouteUpdate(b, ba, frame, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		routes, err := PermittedRoutes(update, []string{old.ID()}, now)
		if err != nil || len(routes) != 0 {
			t.Fatal("withdrawal or pin change reused permission", err)
		}
	}
}
func TestRouteUpdateCandidateValidationAndBounds(t *testing.T) {
	a, _, ab, _ := routePairFixture()
	now := time.Now().UTC()
	local := routeFixture("local", "192.168.50.2:54446")
	cases := [][]RouteCandidate{
		{routeFixture("local", "192.0.2.10:54446")},
		{routeFixture("implicit", "192.168.50.2:54446")},
		{local, local},
		{local, {local.Relay, "external"}},
		{local, local, local, local, local},
	}
	for i, candidates := range cases {
		if _, err := SealRouteUpdate(a, ab, 1, candidates, now, now.Add(time.Hour)); err == nil {
			t.Fatalf("invalid candidate case %d", i)
		}
	}
	for _, expiry := range []time.Time{now, now.Add(MaxRouteUpdateLifetime + time.Second)} {
		if _, err := SealRouteUpdate(a, ab, 1, []RouteCandidate{local}, now, expiry); err == nil {
			t.Fatal("bad lifetime")
		}
	}
	if _, err := SealRouteUpdate(a, ab, 0, []RouteCandidate{local}, now, now.Add(time.Hour)); err == nil {
		t.Fatal("zero sequence")
	}
}
func TestRouteUpdateUnknownFieldsAndWrongDomainRejected(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	binding, _ := PairRouteBinding(a, ab)
	u := RouteUpdate{RouteUpdateVersion, routeUpdateDomain, a.PublicKey(), b.PublicKey(), binding, 1, now, now.Add(time.Hour), nil}
	raw, _ := json.Marshal(u)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["extra"] = true
	frame, _, _ := sealMessage(a, b.PublicKey(), m)
	if _, err := OpenRouteUpdate(b, ba, frame, 0, now); err == nil {
		t.Fatal("unknown field")
	}
	u.Domain = "sobalink pairing v1 request"
	frame, _, _ = sealMessage(a, b.PublicKey(), u)
	if _, err := OpenRouteUpdate(b, ba, frame, 0, now); err == nil {
		t.Fatal("cross-protocol proof")
	}
}

func TestRouteUpdateCanonicalEncodingAndDirectionalBinding(t *testing.T) {
	a, b, ab, ba := routePairFixture()
	now := time.Now().UTC()
	binding, _ := PairRouteBinding(a, ab)
	u := RouteUpdate{RouteUpdateVersion, routeUpdateDomain, a.PublicKey(), b.PublicKey(), binding, 1, now, now.Add(time.Hour), nil}
	raw, _ := json.Marshal(u)
	for _, plain := range [][]byte{
		bytes.Replace(raw, []byte(`"sequence":1`), []byte(`"sequence":1,"sequence":1`), 1),
		bytes.Replace(raw, []byte(`"sequence":1`), []byte(`"Sequence":1`), 1),
	} {
		pub, _ := parseNodePublic(b.PublicKey())
		frame, _ := json.Marshal(pairEnvelope{a.PublicKey(), a.Key.SealTo(pub, plain)})
		if _, err := OpenRouteUpdate(b, ba, frame, 0, now); err == nil {
			t.Fatal("ambiguous encoding accepted")
		}
	}
	swapped := ba
	swapped.ClientPrivate = ab.ClientPrivate
	swapped.IncomingClientKey = ab.IncomingClientKey
	if other, err := PairRouteBinding(b, swapped); err == nil && other == binding {
		t.Fatal("directional roles not bound")
	}
}
