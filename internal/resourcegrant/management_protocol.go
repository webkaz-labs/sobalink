package resourcegrant

import "io"

// ManagementProtocolVersion is separate from the unchanged resource wire schema
// and from private persistence formats. Support never establishes authority.
const ManagementProtocolVersion = 2

// ManagementHelloRequest sends no resource or grant selector. Management clients
// must accept ManagementHelloResponse before sending a typed request.
func ManagementHelloRequest() [HelloBytes]byte {
	return [HelloBytes]byte{'S', 'B', 'R', 'I', 1, ManagementProtocolVersion, 0, 0}
}

// ManagementHelloResponse preserves the exact inspection-v1 response for old
// clients. It is protocol support only, not grant or resource discovery. Existing
// inspect-only listeners continue using HelloResponse and reject v2.
func ManagementHelloResponse(requested byte) ([HelloBytes]byte, bool, error) {
	if requested == ProtocolVersion {
		return HelloResponse(requested)
	}
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

// ReadManagementHelloResponse never accepts an inspection-v1 success as write
// support. Unsupported, malformed, EOF and timeout results never permit fallback.
func ReadManagementHelloResponse(reader io.Reader) error {
	var hello [HelloBytes]byte
	if _, err := io.ReadFull(reader, hello[:]); err != nil {
		return ErrTransport
	}
	if !validHello(hello, 2) || hello[5] == 0 {
		return ErrProtocol
	}
	switch hello[6] {
	case 0:
		if hello[5] != ManagementProtocolVersion {
			return ErrProtocol
		}
		return nil
	case 1:
		return ErrUnsupported
	default:
		return ErrProtocol
	}
}
