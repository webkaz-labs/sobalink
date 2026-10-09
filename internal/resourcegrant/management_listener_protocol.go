package resourcegrant

// ManagementListenerHelloResponse advertises only the protocol accepted by the
// management-only listener. Unlike the pure compatibility helper, it explicitly
// rejects legacy v1 before any selectors are read. Existing inspection listeners
// and ManagementHelloResponse retain their exact previous byte contracts.
// Only the dedicated management listener uses this support response.
func ManagementListenerHelloResponse(requested byte) ([HelloBytes]byte, bool, error) {
	if requested == 0 {
		return [HelloBytes]byte{}, false, ErrProtocol
	}
	reply := [HelloBytes]byte{'S', 'B', 'R', 'I', 2, ManagementProtocolVersion, 0, 0}
	supported := requested == ManagementProtocolVersion
	if !supported {
		reply[6] = 1
	}
	return reply, supported, nil
}
