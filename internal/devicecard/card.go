// Package devicecard implements a bounded, canonical public device-card format.
// A card is an unverified hint: neither its fields nor its content digest prove
// identity, possession of a key, freshness, reachability, or permission to act.
package devicecard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
)

const (
	Prefix          = "soba-card1."
	MaxDecodedBytes = 768
	MaxEncodedBytes = 1035 // Includes Prefix and unpadded base64url.
	MaxInputBytes   = 1040 // Checked before trimming the ASCII paste envelope.
)

var (
	ErrInvalid      = errors.New("invalid device card")
	ErrTooLarge     = errors.New("device card exceeds its size limit")
	ErrMode         = errors.New("device card mode must be lan or direct-lan")
	ErrModeMismatch = errors.New("device card does not match the expected mode")
)

// RelayHint is deliberately not a transport offer or saved relay identity.
// Validation checks its shape only, including for public IPs and low ports.
type RelayHint struct {
	Address           string `json:"address"`
	CertificateSHA256 string `json:"certificateSHA256"`
}

// Card field order and encoding/json escaping are part of the version-1 wire
// format. Only these public fields may be serialized; never embed private state.
type Card struct {
	Version   int        `json:"version"`
	Mode      string     `json:"mode"`
	PublicKey string     `json:"publicKey"`
	Name      string     `json:"name"`
	Endpoint  string     `json:"endpoint,omitempty"`
	Relay     *RelayHint `json:"relay,omitempty"`
}

// Inspection adds explicit uncertainty labels. ContentDigest is SHA-256 of the
// complete canonical card text, not a fingerprint or authentication proof.
type Inspection struct {
	Card
	ContentDigest string `json:"contentDigest"`
	Verification  string `json:"verification"`
	Freshness     string `json:"freshness"`
}

func ValidMode(mode string) bool { return mode == "lan" || mode == "direct-lan" }

func validHex32(raw string) bool {
	if len(raw) != 64 || raw == strings.Repeat("0", 64) {
		return false
	}
	b, err := hex.DecodeString(raw)
	return err == nil && hex.EncodeToString(b) == raw
}

func validName(name string) bool {
	if len(name) < 1 || len(name) > 80 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func canonicalEndpoint(raw string) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(raw)
	if err != nil || ap.String() != raw {
		return netip.AddrPort{}, false
	}
	a := ap.Addr()
	return ap, ap.Port() != 0 && !a.IsUnspecified() && !a.IsMulticast() && !a.Is4In6() && a.Zone() == ""
}

func (c Card) Validate() error {
	if !ValidMode(c.Mode) {
		return ErrMode
	}
	if c.Version != 1 || !validHex32(c.PublicKey) || !validName(c.Name) {
		return ErrInvalid
	}
	if c.Mode == "direct-lan" {
		if c.Relay != nil {
			return ErrInvalid
		}
		if c.Endpoint != "" {
			ap, ok := canonicalEndpoint(c.Endpoint)
			if !ok || (!ap.Addr().IsPrivate() && !ap.Addr().IsLoopback()) || ap.Port() < 1024 || (ap.Port() >= 54543 && ap.Port() <= 54545) {
				return ErrInvalid
			}
		}
	} else {
		if c.Endpoint != "" {
			return ErrInvalid
		}
		if c.Relay != nil {
			ap, ok := canonicalEndpoint(c.Relay.Address)
			if !ok || !validHex32(c.Relay.CertificateSHA256) {
				return ErrInvalid
			}
			// This does not approve a relay, pin, network change or connection.
			if (lanlink.TrustedRelay{Address: ap, CertificateSHA256: c.Relay.CertificateSHA256}).Validate() != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

func Encode(c Card) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", ErrInvalid
	}
	if len(raw) > MaxDecodedBytes {
		return "", ErrTooLarge
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Parse permits only space/tab/CR/LF outside the card. All successful parses
// round-trip byte-for-byte; duplicate, unknown, null, reordered, differently
// escaped, or otherwise noncanonical JSON is rejected before returning fields.
func Parse(input string) (Card, error) {
	if len(input) > MaxInputBytes {
		return Card{}, ErrTooLarge
	}
	text := strings.Trim(input, " \t\r\n")
	if len(text) > MaxEncodedBytes {
		return Card{}, ErrTooLarge
	}
	if !strings.HasPrefix(text, Prefix) {
		return Card{}, ErrInvalid
	}
	encoded := strings.TrimPrefix(text, Prefix)
	for _, b := range []byte(encoded) {
		if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return Card{}, ErrInvalid
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return Card{}, ErrInvalid
	}
	if len(raw) > MaxDecodedBytes {
		return Card{}, ErrTooLarge
	}
	if !utf8.Valid(raw) {
		return Card{}, ErrInvalid
	}
	var card Card // Fresh destination; no fields can survive a prior parse.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&card) != nil {
		return Card{}, ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Card{}, ErrInvalid
	}
	if err := card.Validate(); err != nil {
		return Card{}, err
	}
	canonical, err := json.Marshal(card)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Card{}, ErrInvalid
	}
	return card, nil
}

func Inspect(input, expectedMode string) (Inspection, error) {
	if !ValidMode(expectedMode) {
		return Inspection{}, ErrMode
	}
	card, err := Parse(input)
	if err != nil {
		return Inspection{}, err
	}
	if card.Mode != expectedMode {
		return Inspection{}, ErrModeMismatch
	}
	digest := sha256.Sum256([]byte(strings.Trim(input, " \t\r\n")))
	return Inspection{Card: card, ContentDigest: hex.EncodeToString(digest[:]), Verification: "unverified", Freshness: "unknown"}, nil
}
