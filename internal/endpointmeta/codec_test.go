package endpointmeta

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (PairContext, Envelope, ed25519.PrivateKey) {
	t.Helper()
	b, err := os.ReadFile("testdata/pair.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePairContext(b)
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("testdata/update.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParseEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	return p, e, ed25519.NewKeyFromSeed(seed)
}

func testNow() time.Time { return time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC) }

func TestFixedIndependentVector(t *testing.T) {
	p, e, key := fixture(t)
	b, err := os.ReadFile("testdata/vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		PairBinding  string `json:"pair_binding"`
		ScopeDigest  string `json:"scope_digest"`
		UpdateDigest string `json:"update_digest"`
		Signature    string `json:"signature"`
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	binding, _ := p.Binding()
	scope, _ := p.JoinerScope.Digest()
	hash, _ := e.Digest()
	if binding != want.PairBinding || scope != want.ScopeDigest || hash != want.UpdateDigest || e.Signature != want.Signature {
		t.Fatal("fixed domain-separated vector changed")
	}
	signed, err := Sign(e.Update, key)
	if err != nil || signed != e {
		t.Fatal("signature vector mismatch", err)
	}
	if err := Inspect(e, p, p.JoinerKey, testNow()); err != nil {
		t.Fatal(err)
	}
	text, err := e.Text()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseUpdateText(text)
	if err != nil || decoded != e {
		t.Fatal("text round trip", err)
	}
	text, _ = signed.Text()
	if _, err := ParseUpdateText(text + "="); err == nil {
		t.Fatal("accepted padded text")
	}
}

func TestCanonicalRejectsAlternateGrammar(t *testing.T) {
	p, e, _ := fixture(t)
	b, _ := Encode(p)
	for name, input := range map[string]string{
		"space": " " + string(b), "newline": string(b) + "\n", "trailing": string(b) + "{}",
		"unknown":   strings.Replace(string(b), `"version":1`, `"extra":0,"version":1`, 1),
		"duplicate": strings.Replace(string(b), `"version":1`, `"version":1,"version":1`, 1),
		"case":      strings.Replace(string(b), `"version":1`, `"Version":1`, 1),
		"float":     strings.Replace(string(b), `"version":1`, `"version":1.0`, 1),
		"null":      strings.Replace(string(b), `"host_scope":{`, `"host_scope":null,"discard":{`, 1),
		"order":     strings.Replace(string(b), `"version":1,`, "", 1)[:len(b)-13] + `}`, // malformed also must fail
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePairContext([]byte(input)); err == nil {
				t.Fatal("accepted noncanonical context")
			}
		})
	}
	eb, _ := Encode(e)
	for _, input := range [][]byte{append(eb, '\n'), bytes.Replace(eb, []byte(`"expires":"2030-01-02T00:00:00Z"`), []byte(`"expires":null`), 1), bytes.Replace(eb, []byte(`"sequence":"7"`), []byte(`"sequence":"07"`), 1), bytes.Replace(eb, []byte(`"version":1`), []byte(`"version":2`), 1), bytes.Repeat([]byte("x"), MaxFrameBytes+1)} {
		if _, err := ParseEnvelope(input); err == nil {
			t.Fatal("accepted malformed envelope")
		}
	}
}

func TestUpdateIdentityTimeAndScope(t *testing.T) {
	p, e, key := fixture(t)
	for name, change := range map[string]func(*UpdateBody){
		"pair":              func(u *UpdateBody) { u.PairBinding = strings.Repeat("0", 64) },
		"recipient":         func(u *UpdateBody) { u.Recipient = strings.Repeat("1", 64) },
		"tunnel":            func(u *UpdateBody) { u.IssuerTunnelKey = p.JoinerTunnelKey },
		"scope":             func(u *UpdateBody) { u.ScopeDigest = strings.Repeat("2", 64) },
		"outside":           func(u *UpdateBody) { u.Endpoint = "10.0.0.1:20003" },
		"mapped":            func(u *UpdateBody) { u.Endpoint = "[::ffff:127.0.0.1]:20003" },
		"reserved":          func(u *UpdateBody) { u.Endpoint = "127.0.0.3:54544" },
		"overflow":          func(u *UpdateBody) { u.Sequence = "18446744073709551616" },
		"zero":              func(u *UpdateBody) { u.Sequence = "0" },
		"uppercase":         func(u *UpdateBody) { u.PairBinding = strings.ToUpper(u.PairBinding) },
		"zone":              func(u *UpdateBody) { u.Endpoint = "[fe80::1%lo]:20003" },
		"wildcard":          func(u *UpdateBody) { u.Endpoint = "0.0.0.0:20003" },
		"hostname":          func(u *UpdateBody) { u.Endpoint = "localhost:20003" },
		"public":            func(u *UpdateBody) { u.Endpoint = "192.0.2.1:20003" },
		"time-offset":       func(u *UpdateBody) { u.Issued = "2030-01-01T00:00:00+00:00" },
		"time-precision":    func(u *UpdateBody) { u.Issued = "2030-01-01T00:00:00.000Z" },
		"no-lifetime":       func(u *UpdateBody) { u.Lifetime = "" },
		"overflow-interval": func(u *UpdateBody) { u.Expires = "9999-01-01T00:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			u := e.Update
			change(&u)
			altered, err := Sign(u, key)
			if err == nil && Verify(altered, p, p.JoinerKey) == nil {
				t.Fatal("accepted invalid proof")
			}
		})
	}
	if Inspect(e, p, p.JoinerKey, testNow().Add(24*time.Hour)) != ErrExpired {
		t.Fatal("accepted expired input")
	}
	if Inspect(e, p, p.JoinerKey, testNow().Add(-2*time.Hour)) != ErrExpired {
		t.Fatal("accepted future input")
	}
	if Verify(e, p, p.HostKey) == nil {
		t.Fatal("accepted reflected direction")
	}
	p.HostNonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	if Verify(e, p, p.JoinerKey) == nil {
		t.Fatal("accepted old pair context")
	}
}

func TestScopeAndIPv6(t *testing.T) {
	for _, s := range []Scope{{"ipv4", nil}, {"ipv4", []string{"127.0.0.0/8", "127.0.0.0/8"}}, {"ipv4", []string{"192.168.0.0/16", "10.0.0.0/8"}}, {"ipv4", []string{"10.0.0.1/8"}}, {"ipv4", []string{"0.0.0.0/0"}}, {"ipv4", []string{"126.0.0.0/7"}}, {"ipv6", []string{"fe80::/10"}}, {"ipv6", []string{"fc00::/6"}}} {
		if _, err := Encode(s); err == nil {
			t.Fatal("accepted invalid scope", s)
		}
	}
	p, e, key := fixture(t)
	p.HostScope = Scope{"ipv6", []string{"fc00::/7"}}
	p.JoinerScope = p.HostScope
	p.HostEndpoint = "[fd00::1]:20001"
	p.JoinerEndpoint = "[fd00::2]:20002"
	e.Update.PairBinding, _ = p.Binding()
	e.Update.ScopeDigest, _ = p.JoinerScope.Digest()
	e.Update.PriorEndpoint = p.HostEndpoint
	e.Update.Endpoint = "[fd00::3]:20003"
	e, err := Sign(e.Update, key)
	if err != nil || Verify(e, p, p.JoinerKey) != nil {
		t.Fatal("valid IPv6 proof", err)
	}
}

func TestControlShapesAndFrames(t *testing.T) {
	p, e, _ := fixture(t)
	binding, _ := p.Binding()
	peer := PeerWire{p.JoinerKey, "", p.JoinerEndpoint, p.JoinerTunnelKey}
	requests := []Request{PairRequest{2, "pair", p.HostNonce, peer, p.JoinerNonce, p.JoinerScope}, PrepareRequest{2, "pair-context-prepare", p.HostKey, p.JoinerKey, p.HostTunnelKey, p.JoinerTunnelKey, p.HostNonce, p.HostEndpoint, p.JoinerEndpoint, p.HostScope}, BoundRequest{2, "pair-context-commit", binding}, BoundRequest{2, "pair-context-status", binding}, BoundRequest{2, "session", binding}, e}
	for _, req := range requests {
		b, err := Encode(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseRequest(b); err != nil {
			t.Fatal(err)
		}
		frame, err := Frame(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := ReadFrame(frame)
		if err != nil || !bytes.Equal(body, b) {
			t.Fatal("frame", err)
		}
		if _, err := ReadFrame(append(frame, frame...)); err == nil {
			t.Fatal("accepted second frame")
		}
		if _, isUpdate := req.(Envelope); !isUpdate {
			if _, err := ParseRequest(bytes.Replace(b, []byte(`"version":2`), []byte(`"version":1`), 1)); err == nil {
				t.Fatal("accepted v1")
			}
		}
	}
	replies := []Reply{PairReply{2, "pair", true, PeerWire{p.HostKey, "", p.HostEndpoint, p.HostTunnelKey}, p, binding}, ContextReply{2, "pair-context-commit", true, binding, "committed"}, ContextReply{2, "pair-context-status", true, binding, "prepared"}, SessionReply{2, "session", true, binding}, UpdateReply{2, "endpoint-update", true, binding, e.Update.Sequence, mustDigest(t, e), "review_required"}, ErrorReply{2, "pair", false, "pair_rejected"}}
	for _, r := range replies {
		b, err := Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		var h struct {
			Operation string `json:"operation"`
		}
		json.Unmarshal(b, &h)
		if _, err := ParseReply(b, h.Operation); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseReply(append(b, ' '), h.Operation); err == nil {
			t.Fatal("noncanonical reply")
		}
	}
	for _, s := range []string{`{"version":2,"operation":"pair","ok":false,"code":"pair_rejected","peer":null}`, `{"version":2,"operation":"session","ok":false,"code":"private path"}`, `{"version":2,"operation":"pair","ok":true,"peer":null}`} {
		if _, err := ParseReply([]byte(s), "pair"); err == nil {
			t.Fatal("accepted bad reply")
		}
	}
	if _, err := Encode(ContextReply{2, "pair-context-commit", true, binding, "prepared"}); err == nil {
		t.Fatal("uncommitted commit reply")
	}
}

func mustDigest(t *testing.T, e Envelope) string {
	t.Helper()
	d, err := e.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
