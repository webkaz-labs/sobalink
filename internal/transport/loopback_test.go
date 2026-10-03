package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestExplicitIPv6LoopbackListeners(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }
	for _, network := range []string{"tcp", "udp", "socks"} {
		t.Run(network, func(t *testing.T) {
			var server *Server
			var err error
			switch network {
			case "tcp":
				server, err = StartTCP(t.Context(), TCPConfig{ListenAddress: "[::1]:0", Target: "example.test:8080"}, dial)
			case "udp":
				server, err = StartUDP(t.Context(), UDPConfig{ListenAddress: "[::1]:0", Target: "example.test:8080"}, dial)
			case "socks":
				server, err = StartSOCKS(t.Context(), SOCKSConfig{ListenAddress: "[::1]:0", Username: "user", Password: "password"}, dial)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer closeServer(t, server)
			address, err := netip.ParseAddrPort(server.Addr().String())
			if err != nil || address.Addr() != netip.IPv6Loopback() {
				t.Fatalf("wrong loopback listener: %v, %v", server.Addr(), err)
			}
		})
	}
}
