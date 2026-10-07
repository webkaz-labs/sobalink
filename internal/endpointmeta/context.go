package endpointmeta

import (
	"encoding/base64"
	"net/netip"
	"time"
)

const InvitationPrefix = "soba-directlan2."
const ALPN = "sobalink-directlan/2"

type Scope struct {
	Family   string   `json:"family"`
	Prefixes []string `json:"prefixes"`
}

func (s Scope) validate() error {
	if len(s.Prefixes) > maxScopePrefixes {
		return ErrCapacity
	}
	if (s.Family != "ipv4" && s.Family != "ipv6") || len(s.Prefixes) == 0 {
		return ErrPolicy
	}
	for i, text := range s.Prefixes {
		if len(text) > 43 {
			return ErrPolicy
		}
		p, err := netip.ParsePrefix(text)
		if err != nil || p.String() != text || p != p.Masked() || p.Bits() == 0 || p.Addr().Is4In6() || p.Addr().Zone() != "" || p.Addr().Is4() != (s.Family == "ipv4") || i > 0 && s.Prefixes[i-1] >= text {
			return ErrPolicy
		}
		last := p.Addr().AsSlice()
		for bit := p.Bits(); bit < len(last)*8; bit++ {
			last[bit/8] |= 1 << uint(7-bit%8)
		}
		end, _ := netip.AddrFromSlice(last)
		if !(p.Addr().IsPrivate() && end.IsPrivate()) && !(p.Addr().IsLoopback() && end.IsLoopback()) {
			return ErrPolicy
		}
	}
	return nil
}

func (s Scope) Contains(text string) bool {
	if s.validate() != nil {
		return false
	}
	a, err := endpoint(text)
	if err != nil || a.Addr().Is4() != (s.Family == "ipv4") {
		return false
	}
	for _, text := range s.Prefixes {
		p, _ := netip.ParsePrefix(text)
		if p.Contains(a.Addr()) {
			return true
		}
	}
	return false
}

func (s Scope) Digest() (string, error) {
	b, err := Encode(s)
	if err != nil {
		return "", err
	}
	return digest("sobalink directlan endpoint scope v1", b), nil
}

type PeerWire struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	TunnelKey string `json:"tunnel_key"`
}

func (p PeerWire) validate() error {
	if !validHex(p.Key) || !validTunnel(p.TunnelKey) || !validName(p.Name) {
		return ErrIdentity
	}
	_, err := endpoint(p.Endpoint)
	return err
}

type PairContext struct {
	Version         int    `json:"version"`
	HostKey         string `json:"host_key"`
	JoinerKey       string `json:"joiner_key"`
	HostTunnelKey   string `json:"host_tunnel_key"`
	JoinerTunnelKey string `json:"joiner_tunnel_key"`
	HostNonce       string `json:"host_nonce"`
	JoinerNonce     string `json:"joiner_nonce"`
	HostEndpoint    string `json:"host_endpoint"`
	JoinerEndpoint  string `json:"joiner_endpoint"`
	HostScope       Scope  `json:"host_scope"`
	JoinerScope     Scope  `json:"joiner_scope"`
}

func (p PairContext) validate() error {
	if p.Version != 1 || !validHex(p.HostKey) || !validHex(p.JoinerKey) || p.HostKey == p.JoinerKey || !validTunnel(p.HostTunnelKey) || !validTunnel(p.JoinerTunnelKey) || p.HostTunnelKey == p.JoinerTunnelKey {
		return ErrIdentity
	}
	if _, err := rawBytes(p.HostNonce, 32); err != nil {
		return err
	}
	if _, err := rawBytes(p.JoinerNonce, 32); err != nil {
		return err
	}
	if !p.HostScope.Contains(p.HostEndpoint) || !p.HostScope.Contains(p.JoinerEndpoint) || !p.JoinerScope.Contains(p.HostEndpoint) || !p.JoinerScope.Contains(p.JoinerEndpoint) {
		return ErrPolicy
	}
	return nil
}

func (p PairContext) Binding() (string, error) {
	b, err := Encode(p)
	if err != nil {
		return "", err
	}
	return digest("sobalink directlan pair context v1", b), nil
}

func ParsePairContext(b []byte) (PairContext, error) {
	var p PairContext
	err := decode(b, &p)
	return p, err
}

type Invitation struct {
	Version      int      `json:"version"`
	Host         PeerWire `json:"host"`
	RecipientKey string   `json:"recipient_key"`
	Token        string   `json:"token"`
	Expires      string   `json:"expires"`
	HostNonce    string   `json:"host_nonce"`
	HostScope    Scope    `json:"host_scope"`
}

func (i Invitation) validate() error {
	if i.Version != 2 || i.Host.validate() != nil || !validHex(i.RecipientKey) || i.RecipientKey == i.Host.Key || !i.HostScope.Contains(i.Host.Endpoint) {
		return ErrInvalid
	}
	if _, err := rawBytes(i.Token, 32); err != nil {
		return err
	}
	if _, err := rawBytes(i.HostNonce, 32); err != nil {
		return err
	}
	_, err := instant(i.Expires)
	return err
}

func (i Invitation) ValidateFor(recipient string, now time.Time) error {
	if err := i.validate(); err != nil {
		return err
	}
	expires, _ := instant(i.Expires)
	if recipient != i.RecipientKey {
		return ErrIdentity
	}
	if now.IsZero() || !now.Before(expires) || expires.Sub(now) > 10*time.Minute {
		return ErrExpired
	}
	return nil
}

func (i Invitation) Text() (string, error) {
	b, err := Encode(i)
	if err != nil {
		return "", err
	}
	return InvitationPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func ParseInvitation(s string) (Invitation, error) {
	var i Invitation
	b, err := decodeText(s, InvitationPrefix)
	if err != nil {
		return i, err
	}
	err = decode(b, &i)
	return i, err
}
