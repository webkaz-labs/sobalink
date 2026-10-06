package directlan

import (
	"errors"
	"net"
	"net/netip"
)

var ErrLocalAddressUnavailable = errors.New("selected direct LAN address is no longer assigned to an up local interface")
var ErrLocalAddressUnknown = errors.New("selected direct LAN interface availability could not be established")

type interfaceObservation struct {
	up        bool
	addresses []netip.Addr
}

func observedAddressAvailable(address netip.Addr, observations []interfaceObservation) bool {
	for _, o := range observations {
		if !o.up {
			continue
		}
		for _, ip := range o.addresses {
			if ip == address {
				return true
			}
		}
	}
	return false
}
func localAddressReady(address netip.Addr) error {
	// A configured loopback listener remains local and does not depend on which
	// physical LAN interface is currently up. No address enumeration is needed.
	if address.IsLoopback() {
		return nil
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		return ErrLocalAddressUnknown
	}
	var observations []interfaceObservation
	for _, nic := range interfaces {
		if nic.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, e := nic.Addrs()
		if e != nil {
			return ErrLocalAddressUnknown
		}
		item := interfaceObservation{up: true}
		for _, a := range addresses {
			p, e := netip.ParsePrefix(a.String())
			if e == nil {
				item.addresses = append(item.addresses, p.Addr().Unmap())
			}
		}
		observations = append(observations, item)
	}
	if !observedAddressAvailable(address, observations) {
		return ErrLocalAddressUnavailable
	}
	return nil
}
