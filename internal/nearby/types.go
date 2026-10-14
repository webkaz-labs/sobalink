// Package nearby discovers short-lived, unauthenticated setup hints on one
// explicitly selected local scope. It never grants trust or dials a candidate.
package nearby

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Protocol              = "sobalink-setup-v1"
	RequestProtocol       = "sobalink-setup-v2"
	RequestCapability     = "lan-request-v2"
	ReceiveCapability     = "lan-receive-v2"
	Capability            = "lan-bootstrap-v1"
	SessionLifetime       = 120 * time.Second
	CandidateTTL          = 30 * time.Second
	MaxCandidates         = 64
	MaxDatagramBytes      = 1200
	MaxReceivedDatagrams  = 512
	MaxParsedPerSecond    = 16
	MaxSentDatagrams      = 64
	AdvertisementInterval = 5 * time.Second
	QueryInterval         = 15 * time.Second
)

var (
	ErrInvalidScope         = errors.New("nearby: invalid selected scope")
	ErrInvalidAdvertisement = errors.New("nearby: invalid advertisement")
	ErrInvalidPacket        = errors.New("nearby: invalid packet")
	ErrCandidateUnavailable = errors.New("nearby: candidate expired or changed")
	ErrReceiveBudget        = errors.New("nearby: receive budget exhausted")
	ErrSendBudget           = errors.New("nearby: send budget exhausted")
	ErrExpired              = errors.New("nearby: session expired")
	ErrUnsupported          = errors.New("nearby: scoped multicast is unsupported; use manual setup")
	ErrScopeChanged         = errors.New("nearby: selected local binding changed")
)

// Scope is an explicit local choice, not permission inferred from an advert.
// Native factories must revalidate the current interface binding before use.
type Scope struct {
	InterfaceIndex int
	LocalAddress   netip.Addr
	Prefix         netip.Prefix
}

func (s Scope) Validate() error { return (discoveryPolicy{}).validateScope(s) }

func (s Scope) validateLAN() error {
	if s.InterfaceIndex <= 0 || !canonicalPrivate(s.LocalAddress) || !s.Prefix.IsValid() || s.Prefix != s.Prefix.Masked() || !s.Prefix.Contains(s.LocalAddress) || s.Prefix.Addr().BitLen() != s.LocalAddress.BitLen() {
		return ErrInvalidScope
	}
	// An oversized prefix must not turn a private address into permission to
	// observe or contact public or unrelated address ranges.
	roots := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7")}
	for _, root := range roots {
		if root.Contains(s.Prefix.Addr()) && s.Prefix.Bits() >= root.Bits() {
			return nil
		}
	}
	return ErrInvalidScope
}

func canonicalPrivate(a netip.Addr) bool {
	return a.IsValid() && a.IsPrivate() && !a.Is4In6() && a.Zone() == ""
}

// Advertisement deliberately has no permanent identity, certificate pin,
// invitation, hostname, files, service list, or permission fields.
type Advertisement struct {
	SessionID  string
	Alias      string
	Protocol   string
	Capability string
	Bootstrap  netip.AddrPort
}

func NewSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if id == strings.Repeat("0", 64) {
		return "", ErrInvalidAdvertisement
	}
	return id, nil
}

func (a Advertisement) Validate() error { return a.validate(discoveryPolicy{}) }

func (a Advertisement) validate(policy discoveryPolicy) error {
	if len(a.SessionID) != 64 {
		return ErrInvalidAdvertisement
	}
	sid, err := hex.DecodeString(a.SessionID)
	if err != nil || len(sid) != 32 || strings.ToLower(a.SessionID) != a.SessionID || a.SessionID == strings.Repeat("0", 64) || !((a.Protocol == Protocol && a.Capability == Capability) || (a.Protocol == RequestProtocol && (a.Capability == RequestCapability || a.Capability == ReceiveCapability))) || !policy.validAddress(a.Bootstrap.Addr()) || a.Bootstrap.Port() < 1024 || (a.Bootstrap.Port() >= 54543 && a.Bootstrap.Port() <= 54545) {
		return ErrInvalidAdvertisement
	}
	if len(a.Alias) == 0 || len(a.Alias) > 80 || !utf8.ValidString(a.Alias) || strings.TrimSpace(a.Alias) != a.Alias {
		return ErrInvalidAdvertisement
	}
	for _, r := range a.Alias {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return ErrInvalidAdvertisement
		}
	}
	return nil
}

// CandidateProvenance identifies the observation mechanism, never peer trust.
type CandidateProvenance string

const (
	RawPacketProvenance    CandidateProvenance = "raw-packet"
	OSDNSServiceProvenance CandidateProvenance = "os-dns-service"
)

// Candidate is an unauthenticated observation. ID names one immutable local
// observation revision, not an authenticated peer. Consumers must look up the
// exact ID again before using it and authenticate the selected endpoint.
type Candidate struct {
	Provenance    CandidateProvenance
	ID            string
	Advertisement Advertisement
	Scope         Scope
	Source        netip.AddrPort // Actual raw source only; absent for OS DNS-SD.
	ObservedAt    time.Time
	ExpiresAt     time.Time
}

// Packet metadata must come from the receiving socket, never from DNS data.
// Destination is the multicast group; the source port must be the mDNS port.
type Packet struct {
	Data           []byte
	Source         netip.AddrPort
	Destination    netip.Addr
	InterfaceIndex int
	HopLimit       int
}

// Transport is owned by a Session. Close must be idempotent and unblock any
// concurrent read/write. Implementations must bound a read to MaxDatagramBytes
// plus one byte so oversized datagrams can be rejected without allocation.
// Writes always target the single selected multicast group, never an advert.
type Transport interface {
	ReadPacket() (Packet, error)
	WritePacket([]byte) error
	Close() error
}

type TransportFactory func(Scope) (Transport, error)

// Clock supplies monotonic process time in production. Injectable clocks are
// for deterministic tests; peer timestamps are never used for freshness.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }
