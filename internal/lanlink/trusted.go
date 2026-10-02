package lanlink

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"reflect"
	"strings"

	"github.com/tailscale/tailcat"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// TrustedRelay selects exactly one numeric endpoint and its TLS certificate.
// This mode allows peer direct traffic and diagnostics to this relay; it is not
// a promise of zero external traffic or a LAN-only egress sandbox.
type TrustedRelay struct {
	Address           netip.AddrPort `json:"address"`
	CertificateSHA256 string         `json:"certificate_sha256"`
}

func (r TrustedRelay) Validate() error {
	a := r.Address.Addr()
	if !r.Address.IsValid() || r.Address.Port() == 0 || a.IsUnspecified() || a.IsMulticast() || a.Zone() != "" || a.Is4In6() {
		return errors.New("explicit numeric unicast relay endpoint required")
	}
	if !validKey(r.CertificateSHA256) {
		return errors.New("relay certificate SHA-256 pin required")
	}
	return nil
}
func (r TrustedRelay) region() *tailcfg.DERPRegion {
	n := &tailcfg.DERPNode{Name: r.Address.Addr().String(), RegionID: 1, HostName: r.Address.Addr().String(), CertName: "sha256-raw:" + r.CertificateSHA256, IPv4: "none", IPv6: "none", DERPPort: int(r.Address.Port()), STUNPort: -1}
	if r.Address.Addr().Is4() {
		n.IPv4 = r.Address.Addr().String()
	} else {
		n.IPv6 = r.Address.Addr().String()
	}
	return &tailcfg.DERPRegion{RegionID: 1, RegionCode: "1", Nodes: []*tailcfg.DERPNode{n}}
}

// Identity is secret state. Generate only during an explicit setup action and
// persist using the application's protected state store. Never log this value.
type Identity struct {
	Key key.NodePrivate      `json:"key"`
	PSK tailcat.PresharedKey `json:"psk"`
}

func GenerateIdentity() Identity { return Identity{key.NewNode(), tailcat.NewPresharedKey()} }
func (i Identity) Validate() error {
	if i.Key.IsZero() || i.PSK.IsZero() {
		return errors.New("saved node key and pre-shared key required")
	}
	return nil
}
func (i Identity) PublicKey() string    { return keyString(i.Key.Public()) }
func keyString(k key.NodePublic) string { b := k.Raw32(); return hex.EncodeToString(b[:]) }

// overlayAddress is the pinned Tailcat address derivation at b4dc28e8aa89.
// Keeping it here avoids Client.DialTCPPort's UserDial OS-fallback boundary.
// Upstream upgrades must reverify this against tailcat.tcAddrForKey.
func overlayAddress(k key.NodePublic) netip.Addr {
	var a [16]byte
	copy(a[:6], []byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0})
	b := k.Raw32()
	copy(a[6:], b[:10])
	return netip.AddrFrom16(a)
}

// ValidateBuild prevents accidental enablement of discovery/proxy modules that
// can contact destinations outside the selected relay. It does not disable
// ordinary peer direct traffic or relay HTTPS/ICMP latency measurement.
func ValidateBuild() error {
	if buildfeatures.HasPortMapper || buildfeatures.HasCaptivePortal || buildfeatures.HasUseProxy {
		return errors.New("trusted relay mode requires build tags ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy")
	}
	return nil
}
func validateEnvironment(env []string) error {
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		if v == "" {
			continue
		}
		u := strings.ToUpper(k)
		if u == "HTTP_PROXY" || u == "HTTPS_PROXY" || u == "ALL_PROXY" || u == "TS_PROXY" {
			return errors.New("proxy environment is unsupported in trusted relay mode")
		}
		if strings.HasPrefix(u, "TS_") && u != "TS_NO_LOGS_NO_SUPPORT" {
			return errors.New("Tailscale environment overrides are unsupported in trusted relay mode")
		}
	}
	return nil
}

// RemotePeer contains a secret Tailcat capability. Keep it in protected state,
// separate from public status, logs and the Book's public Snapshot.
// PeerOffer is safe to exchange only in the chosen private pairing channel:
// Address includes a PSK. It contains no local private engine key.
type PeerOffer struct {
	Peer    Peer         `json:"peer"`
	Address tailcat.Addr `json:"address"`
}
type RemotePeer struct {
	Peer              Peer            `json:"peer"`
	Address           tailcat.Addr    `json:"address"`
	ClientPrivate     key.NodePrivate `json:"client_private"`
	IncomingClientKey string          `json:"incoming_client_key"`
}

func (r RemotePeer) Offer() PeerOffer { return PeerOffer{Peer: r.Peer, Address: r.Address} }

func validateRemote(r PeerOffer, relay TrustedRelay) (netip.Addr, error) {
	if !validPeer(r.Peer) || len(r.Address) > 16384 {
		return netip.Addr{}, ErrUntrusted
	}
	ci, e := tailcat.ParseAddr(r.Address)
	if e != nil {
		return netip.Addr{}, errors.New("invalid peer capability")
	}
	if ci.ServerPublic.IsZero() || ci.ServerDiscoPublic.IsZero() || ci.PresharedKey.IsZero() || keyString(ci.ServerPublic.NodePublic) != r.Peer.Key {
		return netip.Addr{}, ErrUntrusted
	}
	if ci.RegionID != 0 || len(ci.Region) != 1 || ci.Region[0] == nil {
		return netip.Addr{}, errors.New("peer capability must embed the selected relay")
	}
	got := ci.Region[0]
	want := relay.region()
	if got.RegionID != 1 || len(got.Nodes) != 1 || got.Nodes[0] == nil {
		return netip.Addr{}, errors.New("unexpected relay region")
	}
	if !reflect.DeepEqual(got, want) {
		return netip.Addr{}, errors.New("peer relay does not match the exact trusted region shape")
	}

	return overlayAddress(ci.ServerPublic.NodePublic), nil
}

func distinctRoleKeys(remote RemotePeer, used map[string]bool) error {
	if remote.ClientPrivate.IsZero() || !validKey(remote.IncomingClientKey) {
		return errors.New("separate paired role keys required")
	}
	keys := []string{remote.Peer.Key, keyString(remote.ClientPrivate.Public()), remote.IncomingClientKey}
	for _, k := range keys {
		if !validKey(k) || used[k] {
			return errors.New("server and role keys must be distinct across peers")
		}
		used[k] = true
	}
	return nil
}
