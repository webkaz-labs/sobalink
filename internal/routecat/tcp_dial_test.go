package routecat

import (
	"context"
	"errors"
	"net"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

func TestTCPDialErrorsNeverExposeTypedNilConnection(t *testing.T) {
	var concrete *gonet.TCPConn
	var unsafeInterface net.Conn = concrete
	if unsafeInterface == nil {
		t.Fatal("fixture does not represent typed nil")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("synthetic refusal")} {
		conn, err := normalizeTCPDial(concrete, cause)
		if conn != nil || !errors.Is(err, cause) {
			t.Fatal("dial error exposed typed nil or lost its cause")
		}
		// This is the ordinary cleanup pattern that previously panicked in native
		// Core when a route transition made a TCP connection attempt fail.
		if conn != nil {
			conn.Close()
		}
	}
	conn, err := normalizeTCPDial(nil, nil)
	if conn != nil || err == nil {
		t.Fatal("empty dial result was accepted")
	}
}

func TestTCPDialPreservesConcreteResultWithoutTouchingIt(t *testing.T) {
	// A zero object is used only for identity comparison; no sockets or methods.
	concrete := new(gonet.TCPConn)
	conn, err := normalizeTCPDial(concrete, nil)
	if conn != concrete || err != nil {
		t.Fatal("successful concrete dial was altered")
	}
	cause := errors.New("synthetic partial result")
	conn, err = normalizeTCPDial(concrete, cause)
	if conn != concrete || !errors.Is(err, cause) {
		t.Fatal("concrete partial result was lost")
	}
}
