package directlan

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrPolicy                = errors.New("explicit numeric endpoint inside selected private or loopback prefixes required")
	ErrIdentity              = errors.New("valid pinned Ed25519 identity required")
	ErrUntrusted             = errors.New("peer is not currently paired")
	ErrInvitation            = errors.New("invalid, expired or consumed direct LAN invitation")
	ErrUnavailable           = errors.New("direct LAN service is unavailable")
	ErrCapacity              = errors.New("direct LAN resource limit reached")
	ErrRecovery              = errors.New("direct LAN state persistence failed; reopen protected state before continuing")
	ErrPairUncertain         = errors.New("pairing reply unavailable; remote may have committed, revoke there before retrying")
	ErrRemotePairedLocalSave = errors.New("remote paired but local state was not saved; revoke remote approval before retrying")
)

// These are adjustable resource defaults, not logical permission ceilings.
const DefaultFlowLimit = 256
const DefaultInvitationLimit = 64
const DefaultListenerLimit = 256
const DefaultPacketQueueLimit = 128

// WireGuard's pinned implementation has this actual peer-table resource bound.
const MaxEnginePeers = 1 << 16
const MaxDatagram = 65507

// Identity is a secret Ed25519 seed. Generate only for an explicit setup and
// persist in the caller's protected store. Never put it in a public status.
type Identity struct {
	Seed string `json:"seed"`
}

func GenerateIdentity() (Identity, error) {
	b := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(b); err != nil {
		return Identity{}, err
	}
	return Identity{hex.EncodeToString(b)}, nil
}
func (i Identity) private() (ed25519.PrivateKey, error) {
	b, err := hex.DecodeString(i.Seed)
	if err != nil || len(b) != ed25519.SeedSize || hex.EncodeToString(b) != i.Seed {
		return nil, ErrIdentity
	}
	return ed25519.NewKeyFromSeed(b), nil
}
func (i Identity) Validate() error { _, err := i.private(); return err }
func (i Identity) PublicKey() string {
	k, err := i.private()
	if err != nil {
		return ""
	}
	return hex.EncodeToString(k.Public().(ed25519.PublicKey))
}
func validName(name string) bool {
	return len(name) <= 128 && utf8.ValidString(name) && strings.TrimSpace(name) == name && strings.IndexFunc(name, unicode.IsControl) < 0
}

func validKey(k string) bool {
	b, e := hex.DecodeString(k)
	return e == nil && len(b) == ed25519.PublicKeySize && hex.EncodeToString(b) == k
}

// Peer pins a tunnel endpoint and cryptographic identity, not an application
// destination. The display Name never participates in authorization.
type Peer struct {
	Key       string         `json:"key"`
	Name      string         `json:"name,omitempty"`
	Endpoint  netip.AddrPort `json:"endpoint"`
	TunnelKey string         `json:"tunnel_key"`
}

type Config struct {
	// PeerLimit is an optional additional logical admission limit. Zero imposes
	// none; the caller's protected-store/trusted-peer policy still applies.
	PeerLimit int
	// PeerLimitCurrent optionally reads a lock-free current logical limit; zero
	// means no extra logical ceiling. It must not re-enter Node or block.
	PeerLimitCurrent func() int
	// Resource limits are finite and configurable. Zero selects the defaults
	// above. Core wires these to the existing adjustable resource budgets.
	FlowLimit       int
	InvitationLimit int
	// ControlLimit bounds pairing/session TLS work independently of app flows.
	// Zero inherits InvitationLimit, including its default.
	ControlLimit     int
	ListenerLimit    int
	PacketQueueLimit int
	Identity         Identity
	Listen           netip.AddrPort
	AllowedPrefixes  []netip.Prefix
	Peers            []Peer
	// Persist must atomically save the entire peer snapshot and return nil only
	// after durable success. It must not re-enter Node. Any error fails closed;
	// reopen from saved state to reconcile a possibly published replacement.
	Persist func([]Peer) error
}

func (c Config) Validate() error {
	if err := c.Identity.Validate(); err != nil {
		return err
	}
	if len(c.AllowedPrefixes) == 0 {
		return ErrPolicy
	}
	for _, p := range c.AllowedPrefixes {
		if !p.IsValid() || p != p.Masked() || p.Bits() == 0 || p.Addr().Is4In6() || p.Addr().Zone() != "" {
			return ErrPolicy
		}
		last := prefixLast(p)
		if !(p.Addr().IsPrivate() && last.IsPrivate()) && !(p.Addr().IsLoopback() && last.IsLoopback()) {
			return ErrPolicy
		}
	}
	if !c.permits(c.Listen, true) {
		return ErrPolicy
	}
	if c.PeerLimit < 0 || c.FlowLimit < 0 || c.InvitationLimit < 0 || c.ControlLimit < 0 || c.ListenerLimit < 0 || c.PacketQueueLimit < 0 {
		return ErrCapacity
	}
	if len(c.Peers) > MaxEnginePeers || (c.PeerLimit > 0 && len(c.Peers) > c.PeerLimit) {
		return ErrCapacity
	}
	seen := map[string]bool{c.Identity.PublicKey(): true}
	tunnels := map[string]bool{c.Identity.TunnelKey(): true}
	for _, p := range c.Peers {
		if !validKey(p.Key) || seen[p.Key] || !validTunnelKey(p.TunnelKey) || tunnels[p.TunnelKey] || !validName(p.Name) || !c.permits(p.Endpoint, true) {
			return ErrIdentity
		}
		seen[p.Key] = true
		tunnels[p.TunnelKey] = true
	}
	return nil
}
func prefixLast(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for bit := p.Bits(); bit < len(b)*8; bit++ {
		b[bit/8] |= 1 << uint(7-bit%8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
func (c Config) permits(ap netip.AddrPort, endpoint bool) bool {
	a := ap.Addr()
	if !ap.IsValid() || ap.Port() == 0 || a.Is4In6() || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || (!a.IsPrivate() && !a.IsLoopback()) {
		return false
	}
	if endpoint && ap.Port() < 1024 {
		return false
	}
	// Never let a configured LAN bind escape via another interface/family.
	if c.Listen.IsValid() && a.Is4() != c.Listen.Addr().Is4() {
		return false
	}
	for _, p := range c.AllowedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// OverlayAddress is presentation/dispatch metadata only. It is never routed by
// the OS and can identify a caller only through Node.PeerKey's live flow table.
func OverlayAddress(key string) (netip.Addr, error) {
	if !validKey(key) {
		return netip.Addr{}, ErrIdentity
	}
	hash := sha256.Sum256([]byte("sobalink directlan overlay v1:" + key))
	var a [16]byte
	copy(a[:], hash[:16])
	copy(a[:6], []byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0})
	return netip.AddrFrom16(a), nil
}

// WireGuard uses a domain-separated X25519 key, never the Ed25519 signing key.
func (i Identity) tunnelPrivate() ([]byte, error) {
	key, e := i.private()
	if e != nil {
		return nil, e
	}
	input := append([]byte("sobalink directlan wireguard v1\x00"), key.Seed()...)
	sum := sha256.Sum256(input)
	return sum[:], nil
}
func (i Identity) TunnelKey() string {
	b, e := i.tunnelPrivate()
	if e != nil {
		return ""
	}
	k, e := ecdh.X25519().NewPrivateKey(b)
	if e != nil {
		return ""
	}
	return hex.EncodeToString(k.PublicKey().Bytes())
}
func validTunnelKey(k string) bool {
	if !validKey(k) {
		return false
	}
	b, _ := hex.DecodeString(k)
	pub, e := ecdh.X25519().NewPublicKey(b)
	if e != nil {
		return false
	}
	var check [32]byte
	check[0] = 1
	priv, _ := ecdh.X25519().NewPrivateKey(check[:])
	_, e = priv.ECDH(pub)
	return e == nil
}

func (c Config) withDefaults() Config {
	if c.FlowLimit == 0 {
		c.FlowLimit = DefaultFlowLimit
	}
	if c.InvitationLimit == 0 {
		c.InvitationLimit = DefaultInvitationLimit
	}
	if c.ControlLimit == 0 {
		c.ControlLimit = c.InvitationLimit
	}
	if c.ListenerLimit == 0 {
		c.ListenerLimit = DefaultListenerLimit
	}
	if c.PacketQueueLimit == 0 {
		c.PacketQueueLimit = DefaultPacketQueueLimit
	}
	return c
}
func (n *Node) peerCapacityLocked() bool {
	limit := n.cfg.PeerLimit
	if n.cfg.PeerLimitCurrent != nil {
		limit = n.cfg.PeerLimitCurrent()
	}
	return limit < 0 || len(n.peers) >= MaxEnginePeers || (limit > 0 && len(n.peers) >= limit)
}
