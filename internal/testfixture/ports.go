// Package testfixture provides helpers used only by test fixtures.
package testfixture

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync"
)

const (
	loopbackPortAttempts = 100
	minLoopbackPort      = 1024
	loopbackPortCount    = 65535 - minLoopbackPort + 1
)

// PortReservation holds TCP and UDP sockets on one exact loopback endpoint.
// Close releases both sockets; a caller must check its result just before
// starting the fixture. Another process can still claim the port after release.
type PortReservation struct {
	endpoint netip.AddrPort
	tcp      io.Closer
	udp      io.Closer
	once     sync.Once
	closeErr error
}

func (r *PortReservation) Endpoint() netip.AddrPort { return r.endpoint }

// Close is idempotent, including when either socket reports a close error.
func (r *PortReservation) Close() error {
	r.once.Do(func() { r.closeErr = closePortSockets(r.tcp, r.udp) })
	return r.closeErr
}

type portBinder func(string, netip.AddrPort) (netip.AddrPort, io.Closer, error)

// ReserveLoopbackTCPUDP finds a port that can bind both protocols on exactly
// 127.0.0.1 or ::1. It first asks the OS for a TCP port, then probes spread-out
// candidates: the ephemeral range may be excluded for the other protocol.
// Every candidate is checked using real binds, without changing OS policy.
// Excluded ports are never returned and count toward the same attempt limit.
func ReserveLoopbackTCPUDP(ip netip.Addr, excluded ...uint16) (*PortReservation, error) {
	return reserveLoopbackTCPUDP(ip, loopbackPortAttempts, bindLoopbackPort, excluded...)
}

func bindLoopbackPort(network string, endpoint netip.AddrPort) (netip.AddrPort, io.Closer, error) {
	if network == "udp4" || network == "udp6" {
		conn, err := net.ListenPacket(network, endpoint.String())
		if err != nil {
			return netip.AddrPort{}, nil, err
		}
		return conn.LocalAddr().(*net.UDPAddr).AddrPort(), conn, nil
	}
	listener, err := net.Listen(network, endpoint.String())
	if err != nil {
		return netip.AddrPort{}, nil, err
	}
	return listener.Addr().(*net.TCPAddr).AddrPort(), listener, nil
}

func nextLoopbackPort(port int) int {
	// The stride is coprime to 64512, so all unprivileged ports are visited
	// before any repeats, rather than probing adjacent excluded ports.
	return minLoopbackPort + (port-minLoopbackPort+39869)%loopbackPortCount
}

func closePortSockets(tcp, udp io.Closer) error {
	var errs []error
	if tcp != nil {
		if err := tcp.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close fixture TCP socket: %w", err))
		}
	}
	if udp != nil {
		if err := udp.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close fixture UDP socket: %w", err))
		}
	}
	return errors.Join(errs...)
}

func validatePortBinding(network string, requested, bound netip.AddrPort, socket io.Closer) error {
	if socket == nil || bound.Addr() != requested.Addr() || bound.Port() < minLoopbackPort || (requested.Port() != 0 && bound != requested) {
		return fmt.Errorf("invalid fixture %s binding for %s: endpoint %s, socket present %t", network, requested, bound, socket != nil)
	}
	return nil
}

func reserveLoopbackTCPUDP(ip netip.Addr, maxAttempts int, bind portBinder, excluded ...uint16) (*PortReservation, error) {
	if ip != netip.MustParseAddr("127.0.0.1") && ip != netip.IPv6Loopback() {
		return nil, fmt.Errorf("fixture address must be exactly 127.0.0.1 or ::1, got %s", ip)
	}
	if maxAttempts <= 0 {
		return nil, fmt.Errorf("fixture port search requires a positive attempt limit, got %d", maxAttempts)
	}
	tcpNetwork, udpNetwork := "tcp6", "udp6"
	if ip.Is4() {
		tcpNetwork, udpNetwork = "tcp4", "udp4"
	}
	var lastErr error
	nextPort := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		requested := netip.AddrPortFrom(ip, uint16(nextPort))
		if nextPort == 0 {
			nextPort = minLoopbackPort
		} else {
			nextPort = nextLoopbackPort(nextPort)
		}
		endpoint, tcp, err := bind(tcpNetwork, requested)
		if err != nil {
			lastErr = fmt.Errorf("candidate %d/%d, %s %s: %w", attempt, maxAttempts, tcpNetwork, requested, err)
			if closeErr := closePortSockets(tcp, nil); closeErr != nil {
				return nil, errors.Join(lastErr, closeErr)
			}
			continue
		}
		if err = validatePortBinding(tcpNetwork, requested, endpoint, tcp); err != nil {
			return nil, errors.Join(err, closePortSockets(tcp, nil))
		}
		if requested.Port() == 0 {
			nextPort = nextLoopbackPort(int(endpoint.Port()))
		}
		if slices.Contains(excluded, endpoint.Port()) {
			lastErr = fmt.Errorf("candidate %d/%d uses excluded fixture port %d", attempt, maxAttempts, endpoint.Port())
			if closeErr := closePortSockets(tcp, nil); closeErr != nil {
				return nil, errors.Join(lastErr, closeErr)
			}
			continue
		}
		udpEndpoint, udp, err := bind(udpNetwork, endpoint)
		if err != nil {
			lastErr = fmt.Errorf("candidate %d/%d, %s %s: %w", attempt, maxAttempts, udpNetwork, endpoint, err)
			if closeErr := closePortSockets(tcp, udp); closeErr != nil {
				return nil, errors.Join(lastErr, closeErr)
			}
			continue
		}
		if err = validatePortBinding(udpNetwork, endpoint, udpEndpoint, udp); err != nil {
			return nil, errors.Join(err, closePortSockets(tcp, udp))
		}
		return &PortReservation{endpoint: endpoint, tcp: tcp, udp: udp}, nil
	}
	return nil, fmt.Errorf("no shared loopback TCP/UDP port after %d candidate attempts: %w", maxAttempts, lastErr)
}
