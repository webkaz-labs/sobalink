package resourcegrant

import (
	"errors"
	"io"
)

const ProtocolVersion = 1

// ProtocolPort remains an ordinary service port while inspection is inactive.
// An explicitly enabled inspection listener must acquire it exclusively and
// report conflicts; it never displaces another service or reserves it globally.
const ProtocolPort uint16 = 54546
const HelloBytes = 8

var (
	ErrProtocol    = errors.New("invalid resource inspection protocol")
	ErrTransport   = errors.New("resource inspection transport failed")
	ErrUnsupported = errors.New("resource inspection protocol version is unsupported")
)

// HelloRequest contains only fixed protocol identity and version. A client
// sends no resource/grant selectors before accepting a supported response.
// This negotiates peer-authenticated compatibility, not process attestation.
func HelloRequest() [HelloBytes]byte {
	return [HelloBytes]byte{'S', 'B', 'R', 'I', 1, ProtocolVersion, 0, 0}
}

func ReadHelloRequest(reader io.Reader) (byte, error) {
	var hello [HelloBytes]byte
	if _, err := io.ReadFull(reader, hello[:]); err != nil {
		return 0, ErrTransport
	}
	if !validHello(hello, 1) || hello[5] == 0 || hello[6] != 0 {
		return 0, ErrProtocol
	}
	return hello[5], nil
}

// HelloResponse is fixed-size protocol support only. It must not vary with
// grant/resource existence, IDs, settings, expiry or revocation. The transport
// owns current managed-flow authentication and bounded I/O admission.
func HelloResponse(requested byte) ([HelloBytes]byte, bool, error) {
	if requested == 0 {
		return [HelloBytes]byte{}, false, ErrProtocol
	}
	reply := [HelloBytes]byte{'S', 'B', 'R', 'I', 2, ProtocolVersion, 0, 0}
	supported := requested == ProtocolVersion
	if !supported {
		reply[6] = 1
	}
	return reply, supported, nil
}

// ReadHelloResponse is meaningful only on the caller's current authenticated
// managed connection, with its exclusive deadline. EOF, timeout and ordinary
// service bytes never mean unsupported and never authorize a fallback.
func ReadHelloResponse(reader io.Reader) error {
	var hello [HelloBytes]byte
	if _, err := io.ReadFull(reader, hello[:]); err != nil {
		return ErrTransport
	}
	if !validHello(hello, 2) || hello[5] == 0 {
		return ErrProtocol
	}
	switch hello[6] {
	case 0:
		if hello[5] != ProtocolVersion {
			return ErrProtocol
		}
		return nil
	case 1:
		return ErrUnsupported
	default:
		return ErrProtocol
	}
}
func validHello(hello [HelloBytes]byte, direction byte) bool {
	return hello[0] == 'S' && hello[1] == 'B' && hello[2] == 'R' && hello[3] == 'I' && hello[4] == direction && hello[7] == 0
}
