package core

import (
	"errors"
	"net"
	"net/netip"
	"sort"
)

// LANLocalAddress is a read-only choice for an explicitly selected relay bind.
// Listing choices neither generates an identity nor opens a relay listener.
type LANLocalAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
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
			choice := LANLocalAddress{Interface: item.Name, Address: ip.String()}
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
