package core

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"net"
	"net/netip"
	"sort"
)

// LANLocalAddress is a read-only choice for an explicitly selected relay bind.
// Listing choices neither generates an identity nor opens a relay listener.
type LANLocalAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	Prefix    string `json:"prefix"`
}

const maxLANAddressChoices = 128

func localLANAddresses() ([]LANLocalAddress, error) {
	return readLANAddresses(net.Interfaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() })
}

func readLANAddresses(interfaces func() ([]net.Interface, error), addresses func(net.Interface) ([]net.Addr, error)) ([]LANLocalAddress, error) {
	items, err := interfaces()
	if err != nil {
		return nil, errors.New("local interface addresses are unavailable; retry after checking the selected network")
	}
	choices := []LANLocalAddress{}
	seen := map[LANLocalAddress]bool{}
	for _, item := range items {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := addresses(item)
		if err != nil {
			return nil, errors.New("local interface addresses changed; refresh the address choices")
		}
		for _, raw := range addrs {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if !ip.IsPrivate() || ip.IsLoopback() || ip.Zone() != "" {
				continue
			}
			bits := prefix.Bits()
			if prefix.Addr().Is4In6() {
				bits -= 96
			}
			if bits < 0 {
				continue
			}
			choice := LANLocalAddress{Interface: item.Name, Address: ip.String(), Prefix: netip.PrefixFrom(ip, bits).Masked().String()}
			if !seen[choice] {
				if len(choices) == maxLANAddressChoices {
					return nil, errors.New("too many local addresses; select an exact private relay address manually")
				}
				seen[choice] = true
				choices = append(choices, choice)
			}
		}
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].Interface != choices[j].Interface {
			return choices[i].Interface < choices[j].Interface
		}
		return choices[i].Address < choices[j].Address
	})
	return choices, nil
}

// Certificate status is public metadata only. Reading it never renews an
// identity, opens a socket, or replaces a peer's pinned certificate.
func localRelayCertificateStatus(identity *lanlink.RelayIdentity, now time.Time) map[string]any {
	if identity == nil {
		return nil
	}
	block, _ := pem.Decode(identity.CertificatePEM)
	if block == nil {
		return nil
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	state := "valid"
	switch {
	case now.Before(certificate.NotBefore):
		state = "not-yet-valid"
	case !now.Before(certificate.NotAfter):
		state = "expired"
	case certificate.NotAfter.Sub(now) <= 30*24*time.Hour:
		state = "expiring"
	}
	return map[string]any{"state": state, "notBefore": certificate.NotBefore, "notAfter": certificate.NotAfter}
}

func relayRotationRequired() error {
	return &lanCommandError{"lan_certificate_rotation_required", "saved relay certificate cannot be reused at this address or time; check the clock, revoke saved LAN pairs, stop soba and start --offline, then repeat host setup with --rotate-certificate; verify the new pin and pair again"}
}
