package devicecard

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testCard(mode string) Card {
	return Card{Version: 1, Mode: mode, PublicKey: strings.Repeat("a", 64), Name: "Example device"}
}
func wrapJSON(raw string) string { return Prefix + base64.RawURLEncoding.EncodeToString([]byte(raw)) }
func mustEncode(t *testing.T, c Card) string {
	t.Helper()
	text, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestGoldenCanonicalJSONAndDigest(t *testing.T) {
	for _, tc := range []struct {
		card Card
		json string
	}{
		{Card{1, "lan", strings.Repeat("a", 64), "日本語 <&>\"\\", "", nil}, `{"version":1,"mode":"lan","publicKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"日本語 \u003c\u0026\u003e\"\\"}`},
		{Card{1, "direct-lan", strings.Repeat("b", 64), "Example device", "[fd00::1]:55446", nil}, `{"version":1,"mode":"direct-lan","publicKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"Example device","endpoint":"[fd00::1]:55446"}`},
		{Card{1, "lan", strings.Repeat("c", 64), "Example device", "", &RelayHint{"192.0.2.1:443", strings.Repeat("d", 64)}}, `{"version":1,"mode":"lan","publicKey":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","name":"Example device","relay":{"address":"192.0.2.1:443","certificateSHA256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}`},
	} {
		text := mustEncode(t, tc.card)
		if text != wrapJSON(tc.json) {
			t.Fatalf("wire format changed: %s", text)
		}
		parsed, err := Parse(text)
		if err != nil || !reflect.DeepEqual(parsed, tc.card) {
			t.Fatalf("round trip: %#v %v", parsed, err)
		}
		view, err := Inspect(" \t"+text+"\r\n", tc.card.Mode)
		digest := sha256.Sum256([]byte(text))
		if err != nil || view.ContentDigest != hex.EncodeToString(digest[:]) || view.Verification != "unverified" || view.Freshness != "unknown" {
			t.Fatalf("inspection: %#v %v", view, err)
		}
	}
}

func TestParseRejectsNoncanonicalAndMalformedJSON(t *testing.T) {
	card := testCard("lan")
	raw, _ := json.Marshal(card)
	base := string(raw)
	bad := []string{
		"", "null", "[]", "{}", base + "{}", base + "\n", " " + base,
		strings.Replace(base, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(base, `"version":1`, `"Version":1`, 1),
		strings.Replace(base, `"version":1`, `"version":1.0`, 1),
		strings.Replace(base, `"version":1`, `"version":1e0`, 1),
		strings.Replace(base, `"version":1`, `"version":"1"`, 1),
		strings.Replace(base, `"version":1,`, "", 1),
		strings.Replace(base, `"mode":"lan"`, `"mode":null`, 1),
		strings.Replace(base, `"name":"Example device"`, `"name":null`, 1),
		strings.Replace(base, `"name":"Example device"`, `"name":"Example\u0020device"`, 1),
		strings.Replace(base, `"version":1,"mode":"lan"`, `"mode":"lan","version":1`, 1),
		strings.TrimSuffix(base, "}") + `,"unknown":1}`,
		strings.TrimSuffix(base, "}") + `,"endpoint":""}`,
		strings.TrimSuffix(base, "}") + `,"endpoint":null}`,
		strings.TrimSuffix(base, "}") + `,"relay":null}`,
		strings.TrimSuffix(base, "}") + `,"relay":{}}`,
		strings.Replace(base, "Example device", "\xff", 1),
		strings.Replace(base, "Example device", `\ud800`, 1),
	}
	relay := testCard("lan")
	relay.Relay = &RelayHint{"127.0.0.1:80", strings.Repeat("b", 64)}
	relayJSON, _ := json.Marshal(relay)
	bad = append(bad,
		strings.Replace(string(relayJSON), `"address":"127.0.0.1:80"`, `"address":"127.0.0.1:80","address":"127.0.0.1:80"`, 1),
		strings.Replace(string(relayJSON), `"address":"127.0.0.1:80"`, `"address":"127.0.0.1:80","extra":1`, 1),
		strings.Replace(string(relayJSON), `"certificateSHA256"`, `"certificate_sha256"`, 1),
	)
	for i, value := range bad {
		if got, err := Parse(wrapJSON(value)); err == nil || !reflect.DeepEqual(got, Card{}) {
			t.Errorf("case %d accepted or returned partial data: %#v %v", i, got, err)
		}
	}
}

func TestNamesAndKeys(t *testing.T) {
	for _, name := range []string{"", " example", "example ", "\u00a0example", "example\u3000", strings.Repeat("a", 81), strings.Repeat("日", 27), "bad\x00name", "bad\x7fname", "bad\x85name", "bad\u061cname", "bad\u200ename", "bad\u200fname", "bad\u202aname", "bad\u202ename", "bad\u2066name", "bad\u2069name", "bad\u2028name", "bad\u2029name", "bad\xff"} {
		card := testCard("lan")
		card.Name = name
		if _, err := Encode(card); err == nil {
			t.Errorf("accepted unsafe name %q", name)
		}
	}
	for _, name := range []string{"a", strings.Repeat("a", 80), strings.Repeat("日", 26) + "ab", "A <&> \" \\", "端末 📱"} {
		card := testCard("lan")
		card.Name = name
		if _, err := Parse(mustEncode(t, card)); err != nil {
			t.Errorf("rejected valid name %q: %v", name, err)
		}
	}
	for _, key := range []string{"", strings.Repeat("0", 64), strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64)} {
		card := testCard("lan")
		card.PublicKey = key
		if _, err := Encode(card); err == nil {
			t.Errorf("accepted invalid key %q", key)
		}
	}
}

func TestEndpointsAndRelayShapeOnly(t *testing.T) {
	for _, endpoint := range []string{"localhost:55446", "192.168.1.1:055446", "[fd00:0::1]:55446", "[fd00::1%en0]:55446", "[::ffff:127.0.0.1]:55446", "0.0.0.0:55446", "[::]:55446", "224.0.0.1:55446", "[ff02::1]:55446", "192.0.2.1:55446", "169.254.1.1:55446", "[fe80::1]:55446", "127.0.0.1:0", "127.0.0.1:1023", "127.0.0.1:54543", "127.0.0.1:54544", "127.0.0.1:54545", "127.0.0.1:65536"} {
		card := testCard("direct-lan")
		card.Endpoint = endpoint
		if _, err := Encode(card); err == nil {
			t.Errorf("accepted unsafe direct endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{"127.0.0.1:1024", "192.168.1.1:65535", "10.0.0.1:55446", "[::1]:55446", "[fd00::1]:55446"} {
		card := testCard("direct-lan")
		card.Endpoint = endpoint
		mustEncode(t, card)
	}
	for _, endpoint := range []string{"192.0.2.1:1", "127.0.0.1:80", "[2001:db8::1]:443", "[::1]:54544"} {
		card := testCard("lan")
		card.Relay = &RelayHint{endpoint, strings.Repeat("b", 64)}
		view, err := Inspect(mustEncode(t, card), "lan")
		if err != nil || view.Verification != "unverified" || view.Freshness != "unknown" {
			t.Fatal("shape validation claimed trust", view, err)
		}
	}
	for _, endpoint := range []string{"relay.example:443", "192.0.2.1:0", "[::ffff:127.0.0.1]:443", "0.0.0.0:443", "[ff02::1]:443", "[fe80::1%en0]:443", "[2001:db8:0::1]:443"} {
		card := testCard("lan")
		card.Relay = &RelayHint{endpoint, strings.Repeat("b", 64)}
		if _, err := Encode(card); err == nil {
			t.Errorf("accepted malformed relay %q", endpoint)
		}
	}
	for _, pin := range []string{"", strings.Repeat("0", 64), strings.Repeat("A", 64), strings.Repeat("b", 63)} {
		card := testCard("lan")
		card.Relay = &RelayHint{"192.0.2.1:443", pin}
		if _, err := Encode(card); err == nil {
			t.Errorf("accepted malformed pin %q", pin)
		}
	}
	card := testCard("lan")
	card.Endpoint = "127.0.0.1:55446"
	if _, err := Encode(card); err == nil {
		t.Fatal("lan endpoint accepted")
	}
	card = testCard("direct-lan")
	card.Relay = &RelayHint{"127.0.0.1:443", strings.Repeat("a", 64)}
	if _, err := Encode(card); err == nil {
		t.Fatal("direct relay accepted")
	}
}

func TestBoundsAlphabetAndExpectedMode(t *testing.T) {
	text := mustEncode(t, testCard("lan"))
	if _, err := Parse(strings.Repeat(" ", MaxInputBytes-len(text)) + text); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(strings.Repeat(" ", MaxInputBytes+1-len(text)) + text); !errors.Is(err, ErrTooLarge) {
		t.Fatal("raw bound after trim", err)
	}
	for _, bad := range []string{text + "=", text[:20] + "\n" + text[20:], text[:20] + "\r" + text[20:], "\u00a0" + text, text + "\v", strings.Replace(text, Prefix, "soba-card2.", 1), Prefix + "/w", Prefix + "+w", Prefix + "_x", Prefix + strings.Repeat("A", 1025)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted bad alphabet/envelope %q", bad)
		}
	}
	if MaxEncodedBytes != len(Prefix)+base64.RawURLEncoding.EncodedLen(MaxDecodedBytes) {
		t.Fatal("encoded bound drift")
	}
	if _, err := Parse(wrapJSON(strings.Repeat(" ", MaxDecodedBytes+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatal("decoded bound", err)
	}
	for _, mode := range []string{"", "mixed", "tailnet", "LAN"} {
		if _, err := Inspect(text, mode); !errors.Is(err, ErrMode) {
			t.Fatal(mode, err)
		}
	}
	if got, err := Inspect(text, "direct-lan"); !errors.Is(err, ErrModeMismatch) || got.ContentDigest != "" {
		t.Fatal("mode mismatch", got, err)
	}
	// HTML escaping is the worst case for a permitted one-byte name character.
	max := testCard("lan")
	max.Name = strings.Repeat("<", 80)
	max.Relay = &RelayHint{"[2001:db8:aaaa:bbbb:cccc:dddd:eeee:ffff]:65535", strings.Repeat("b", 64)}
	encoded := mustEncode(t, max)
	if len(encoded) > MaxEncodedBytes {
		t.Fatal("maximum valid card exceeds wire bound")
	}
}

func FuzzParse(f *testing.F) {
	for _, mode := range []string{"lan", "direct-lan"} {
		text, _ := Encode(testCard(mode))
		f.Add(text)
	}
	f.Add(Prefix + "_x")
	f.Add("\xff")
	f.Add(strings.Repeat(" ", MaxInputBytes+1))
	f.Fuzz(func(t *testing.T, input string) {
		card, err := Parse(input)
		if err != nil {
			if !reflect.DeepEqual(card, Card{}) {
				t.Fatal("partial result on failure")
			}
			return
		}
		text, err := Encode(card)
		if err != nil || text != strings.Trim(input, " \t\r\n") {
			t.Fatal("noncanonical accepted")
		}
		if len(input) > MaxInputBytes || len(text) > MaxEncodedBytes {
			t.Fatal("bound exceeded")
		}
	})
}
