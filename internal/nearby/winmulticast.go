package nearby

// Winsock accepts a network-order 0.x.x.x interface index for both
// IP_MULTICAST_IF and IP_MREQ.imr_interface. Address-based selection is ambiguous
// when two interfaces have the same local IPv4 address. Zero means default and
// is never allowed; values outside the documented 24-bit range fail closed.
func windowsMulticastIndex(index int) ([4]byte, error) {
	if index < 1 || index > 0xffffff {
		return [4]byte{}, ErrUnsupported
	}
	return [4]byte{0, byte(index >> 16), byte(index >> 8), byte(index)}, nil
}

type windowsMulticastOps struct {
	setInterface func([4]byte) error
	joinGroup    func([4]byte, [4]byte) error
}

func configureWindowsMulticast(scope Scope, ops windowsMulticastOps) error {
	selected, err := windowsMulticastIndex(scope.InterfaceIndex)
	if err != nil {
		return err
	}
	if err := ops.setInterface(selected); err != nil {
		return err
	}
	return ops.joinGroup(multicast4.As4(), selected)
}
