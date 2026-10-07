// Package endpointmeta defines inert endpoint metadata contracts. It has no
// networking, application authorization, profile loading, or activation hooks.
package endpointmeta

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxFrameBytes = 8192

var (
	ErrInvalid  = errors.New("invalid endpoint metadata")
	ErrCapacity = errors.New("endpoint metadata capacity exceeded")
	ErrIdentity = errors.New("endpoint metadata identity mismatch")
	ErrExpired  = errors.New("endpoint metadata is not currently valid")
	ErrPolicy   = errors.New("endpoint metadata outside selected scope")
	ErrStale    = errors.New("stale endpoint sequence")
	ErrConflict = errors.New("conflicting endpoint sequence")
	ErrRecovery = errors.New("endpoint metadata reconciliation required")
	ErrReview   = errors.New("endpoint metadata review required")
)

type wireValue interface{ validate() error }

// Encode returns the sole canonical wire representation of a supported value.
func Encode(v wireValue) ([]byte, error) {
	if v == nil || reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil() {
		return nil, ErrInvalid
	}
	if !fitsWire(v) {
		return nil, ErrCapacity
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(b) > MaxFrameBytes {
		return nil, ErrCapacity
	}
	return b, nil
}

func decode(b []byte, v wireValue) error {
	if len(b) > MaxFrameBytes {
		return ErrCapacity
	}
	if len(b) == 0 || !utf8.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	canonical, err := Encode(v)
	if err != nil {
		return err
	}
	// Re-encoding rejects duplicate/case-aliased/missing keys, nulls, alternate
	// ordering, escaping, whitespace, number formats, and trailing data.
	if !bytes.Equal(b, canonical) {
		return ErrInvalid
	}
	return nil
}

func digest(domain string, b []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func validHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

func validTunnel(s string) bool {
	if !validHex(s) {
		return false
	}
	b, _ := hex.DecodeString(s)
	pub, err := ecdh.X25519().NewPublicKey(b)
	if err != nil {
		return false
	}
	var seed [32]byte
	seed[0] = 1
	priv, _ := ecdh.X25519().NewPrivateKey(seed[:])
	_, err = priv.ECDH(pub)
	return err == nil
}

func rawBytes(s string, n int) ([]byte, error) {
	if len(s) != base64.RawURLEncoding.EncodedLen(n) {
		return nil, ErrInvalid
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != n || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, ErrInvalid
	}
	return b, nil
}

func validName(s string) bool {
	return len(s) <= 128 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

func endpoint(s string) (netip.AddrPort, error) {
	if len(s) > 47 {
		return netip.AddrPort{}, ErrPolicy
	}
	a, err := netip.ParseAddrPort(s)
	if err != nil || a.String() != s || a.Port() < 1024 || a.Port() >= 54543 && a.Port() <= 54545 {
		return netip.AddrPort{}, ErrPolicy
	}
	ip := a.Addr()
	if ip.Is4In6() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return netip.AddrPort{}, ErrPolicy
	}
	return a, nil
}

func sequence(s string, zero bool) (uint64, error) {
	if len(s) == 0 || len(s) > 20 {
		return 0, ErrInvalid
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s || (!zero && n == 0) {
		return 0, ErrInvalid
	}
	return n, nil
}

func instant(s string) (time.Time, error) {
	if len(s) < 20 || len(s) > 30 {
		return time.Time{}, ErrInvalid
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.UTC().Format(time.RFC3339Nano) != s {
		return time.Time{}, ErrInvalid
	}
	return t, nil
}

func validity(issued, lifetime, expires string) error {
	t, err := instant(issued)
	if err != nil {
		return err
	}
	switch lifetime {
	case "until-revoked":
		if expires != "" {
			return ErrInvalid
		}
	case "finite":
		e, err := instant(expires)
		if err != nil || !e.After(t) || !t.Add(e.Sub(t)).Equal(e) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func current(issued, lifetime, expires string, now time.Time) bool {
	t, err := instant(issued)
	if err != nil || now.IsZero() || now.Before(t) {
		return false
	}
	if lifetime == "until-revoked" {
		return true
	}
	e, err := instant(expires)
	return err == nil && now.Before(e)
}

func decodeText(s, prefix string) ([]byte, error) {
	if !strings.HasPrefix(s, prefix) || len(s) > len(prefix)+base64.RawURLEncoding.EncodedLen(MaxFrameBytes) {
		return nil, ErrCapacity
	}
	text := strings.TrimPrefix(s, prefix)
	b, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(b) > MaxFrameBytes || base64.RawURLEncoding.EncodeToString(b) != text {
		return nil, ErrInvalid
	}
	return b, nil
}
