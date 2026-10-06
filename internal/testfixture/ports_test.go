package testfixture

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"testing"
)

type portCall struct {
	network  string
	endpoint netip.AddrPort
}

// This fake OS reuses the first available ephemeral candidate, and models
// independent TCP/UDP excluded ranges instead of optimistic availability probes.
type portOS struct {
	candidates []uint16
	excluded   map[string][][2]uint16
	active     map[portCall]*portSocket
	sockets    []*portSocket
	calls      []portCall
	maxOpen    int
}

type portSocket struct {
	os         *portOS
	key        portCall
	closeCalls int
	closeErr   error
}

func (s *portSocket) Close() error {
	s.closeCalls++
	delete(s.os.active, s.key)
	return s.closeErr
}

var errPortExcluded = errors.New("test excluded port range")

func (os *portOS) unavailable(network string, endpoint netip.AddrPort) bool {
	if os.active[portCall{network, endpoint}] != nil {
		return true
	}
	for _, ports := range os.excluded[network] {
		if endpoint.Port() >= ports[0] && endpoint.Port() <= ports[1] {
			return true
		}
	}
	return false
}

func (os *portOS) bind(network string, endpoint netip.AddrPort) (netip.AddrPort, io.Closer, error) {
	os.calls = append(os.calls, portCall{network, endpoint})
	if os.active == nil {
		os.active = make(map[portCall]*portSocket)
	}
	if endpoint.Port() == 0 {
		for _, port := range os.candidates {
			candidate := netip.AddrPortFrom(endpoint.Addr(), port)
			if !os.unavailable(network, candidate) {
				endpoint = candidate
				break
			}
		}
		if endpoint.Port() == 0 {
			return netip.AddrPort{}, nil, errors.New("no ephemeral candidates remain")
		}
	}
	if os.unavailable(network, endpoint) {
		return netip.AddrPort{}, nil, errPortExcluded
	}
	key := portCall{network, endpoint}
	socket := &portSocket{os: os, key: key}
	os.active[key] = socket
	os.sockets = append(os.sockets, socket)
	os.maxOpen = max(os.maxOpen, len(os.active))
	return endpoint, socket, nil
}

func (os *portOS) assertClosed(t *testing.T) {
	t.Helper()
	if len(os.active) != 0 {
		t.Errorf("leaked sockets: %v", os.active)
	}
	for _, socket := range os.sockets {
		if socket.closeCalls != 1 {
			t.Errorf("%v closed %d times, want once", socket.key, socket.closeCalls)
		}
	}
}

func TestReservationHoldsBothProtocolsUntilClose(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			os := &portOS{candidates: []uint16{50000}}
			defer os.assertClosed(t)
			address := netip.MustParseAddr(ip)
			r, err := reserveLoopbackTCPUDP(address, 1, os.bind)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			want := netip.AddrPortFrom(address, 50000)
			if r.Endpoint() != want || len(os.active) != 2 || os.maxOpen != 2 {
				t.Fatalf("endpoint=%s, active=%d, peak=%d", r.Endpoint(), len(os.active), os.maxOpen)
			}
			suffix := "6"
			if address.Is4() {
				suffix = "4"
			}
			if os.calls[0] != (portCall{"tcp" + suffix, netip.AddrPortFrom(address, 0)}) || os.calls[1] != (portCall{"udp" + suffix, want}) {
				t.Fatalf("protocols must bind the same exact loopback endpoint: %v", os.calls)
			}
			for range 2 {
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			}
			os.assertClosed(t)
		})
	}
}

func TestReservationAcceptsPortRangeBoundaries(t *testing.T) {
	for _, port := range []uint16{minLoopbackPort, 65535} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			os := &portOS{candidates: []uint16{port}}
			defer os.assertClosed(t)
			r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 1, os.bind)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if r.Endpoint().Port() != port {
				t.Fatalf("endpoint=%s, want port %d", r.Endpoint(), port)
			}
		})
	}
}

func TestReservationEscapesProtocolExcludedEphemeralClusters(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		for _, blocked := range []string{"tcp", "udp"} {
			t.Run(ip+"/"+blocked, func(t *testing.T) {
				address := netip.MustParseAddr(ip)
				suffix := "6"
				if address.Is4() {
					suffix = "4"
				}
				os := &portOS{excluded: map[string][][2]uint16{blocked + suffix: {{49152, 65535}}}}
				for port := uint16(50000); port < 51000; port++ {
					os.candidates = append(os.candidates, port)
				}
				defer os.assertClosed(t)
				r, err := reserveLoopbackTCPUDP(address, 2, os.bind)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				want := uint16(nextLoopbackPort(50000))
				if blocked == "tcp" {
					want = minLoopbackPort // No OS-selected TCP ephemeral candidate was usable.
				}
				if r.Endpoint().Port() != want || len(os.active) != 2 || os.maxOpen != 2 {
					t.Fatalf("endpoint=%s, active=%d, peak=%d", r.Endpoint(), len(os.active), os.maxOpen)
				}
				if len(os.calls) > 4 || os.calls[len(os.calls)-1].endpoint != r.Endpoint() {
					t.Fatalf("unexpected bind sequence: %v", os.calls)
				}
			})
		}
	}
}

func TestReservationClosesPartialTCPBeforeNextCandidate(t *testing.T) {
	os := &portOS{candidates: []uint16{50000}, excluded: map[string][][2]uint16{
		"udp4": {{50000, 50000}},
		"tcp4": {{uint16(nextLoopbackPort(50000)), uint16(nextLoopbackPort(50000))}},
	}}
	defer os.assertClosed(t)
	r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 3, os.bind)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if os.sockets[0].closeCalls != 1 || len(os.active) != 2 || os.maxOpen != 2 || len(os.calls) != 5 {
		t.Fatalf("rejected TCP socket retained: active=%d, peak=%d, calls=%v", len(os.active), os.maxOpen, os.calls)
	}
}

func TestReservationBoundedExhaustion(t *testing.T) {
	for _, blocked := range []string{"tcp4", "udp4"} {
		t.Run(blocked, func(t *testing.T) {
			os := &portOS{candidates: []uint16{50000}, excluded: map[string][][2]uint16{blocked: {{1024, 65535}}}}
			defer os.assertClosed(t)
			r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), loopbackPortAttempts, os.bind)
			if r != nil || !errors.Is(err, errPortExcluded) || !strings.Contains(err.Error(), "100 candidate attempts") {
				t.Fatalf("reservation=%v, err=%v", r, err)
			}
			wantCalls, wantPeak := loopbackPortAttempts, 0
			if blocked == "udp4" {
				wantCalls, wantPeak = 2*loopbackPortAttempts, 1
			}
			if len(os.calls) != wantCalls || os.maxOpen != wantPeak {
				t.Fatalf("bind calls=%d, peak sockets=%d; want %d and %d", len(os.calls), os.maxOpen, wantCalls, wantPeak)
			}
		})
	}
}

func TestReservationExcludesFixturePortsWithinBudget(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			os := &portOS{candidates: []uint16{54544}}
			defer os.assertClosed(t)
			r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), attempts, os.bind, 54543, 54544, 54545)
			if attempts == 1 {
				if r != nil || err == nil || !strings.Contains(err.Error(), "excluded fixture port 54544") || len(os.calls) != 1 {
					t.Fatalf("reservation=%v, err=%v, calls=%v", r, err, os.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if r.Endpoint().Port() != uint16(nextLoopbackPort(54544)) || os.sockets[0].closeCalls != 1 || len(os.calls) != 3 {
				t.Fatalf("excluded candidate not released: endpoint=%s, calls=%v", r.Endpoint(), os.calls)
			}
		})
	}
}

func TestReservationRejectsUnsafeAddressesAndInvalidBudget(t *testing.T) {
	for _, ip := range []string{"", "0.0.0.0", "::", "127.0.0.2", "::ffff:127.0.0.1", "192.0.2.1"} {
		t.Run(ip, func(t *testing.T) {
			address, _ := netip.ParseAddr(ip)
			os := &portOS{}
			if r, err := reserveLoopbackTCPUDP(address, 1, os.bind); r != nil || err == nil || len(os.calls) != 0 {
				t.Fatalf("unsafe address reached binder: reservation=%v, err=%v, calls=%v", r, err, os.calls)
			}
		})
	}
	for _, attempts := range []int{0, -1} {
		os := &portOS{}
		if r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), attempts, os.bind); r != nil || err == nil || len(os.calls) != 0 {
			t.Fatalf("invalid attempt budget: reservation=%v, err=%v, calls=%v", r, err, os.calls)
		}
	}
}

func TestReservationRejectsMalformedBindingsAndClosesSockets(t *testing.T) {
	for _, network := range []string{"tcp4", "udp4"} {
		for _, malformed := range []string{"nil socket", "invalid endpoint", "zero port", "privileged port", "other loopback", "wrong port"} {
			if network == "tcp4" && malformed == "wrong port" {
				continue // The OS is allowed to choose the first TCP port.
			}
			t.Run(network+"/"+malformed, func(t *testing.T) {
				os := &portOS{candidates: []uint16{50000}}
				defer os.assertClosed(t)
				bind := func(n string, ap netip.AddrPort) (netip.AddrPort, io.Closer, error) {
					if n == network && malformed == "nil socket" {
						return netip.AddrPortFrom(ap.Addr(), 50000), nil, nil
					}
					bound, socket, err := os.bind(n, ap)
					if n == network {
						switch malformed {
						case "invalid endpoint":
							bound = netip.AddrPort{}
						case "zero port":
							bound = netip.AddrPortFrom(ap.Addr(), 0)
						case "privileged port":
							bound = netip.AddrPortFrom(ap.Addr(), 1023)
						case "other loopback":
							bound = netip.AddrPortFrom(netip.IPv6Loopback(), 50000)
						case "wrong port":
							bound = netip.AddrPortFrom(ap.Addr(), 50001)
						}
					}
					return bound, socket, err
				}
				if r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 2, bind); r != nil || err == nil || !strings.Contains(err.Error(), "invalid fixture") {
					t.Fatalf("malformed binding accepted: reservation=%v, err=%v", r, err)
				}
			})
		}
	}
}

func TestReservationRejectsWrongExplicitTCPPort(t *testing.T) {
	os := &portOS{candidates: []uint16{50000}, excluded: map[string][][2]uint16{"udp4": {{50000, 50000}}}}
	defer os.assertClosed(t)
	bind := func(network string, ap netip.AddrPort) (netip.AddrPort, io.Closer, error) {
		bound, socket, err := os.bind(network, ap)
		if network == "tcp4" && ap.Port() != 0 {
			bound = netip.AddrPortFrom(ap.Addr(), ap.Port()+1)
		}
		return bound, socket, err
	}
	if r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 2, bind); r != nil || err == nil || !strings.Contains(err.Error(), "invalid fixture tcp4") {
		t.Fatalf("wrong explicit TCP port accepted: reservation=%v, err=%v", r, err)
	}
}

func TestReservationCleansPartialBindErrors(t *testing.T) {
	bindErr := errors.New("test bind error with partial socket")
	closeErr := errors.New("test close failure")
	for _, network := range []string{"tcp4", "udp4"} {
		for _, failClose := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/close-error-%t", network, failClose), func(t *testing.T) {
				os := &portOS{candidates: []uint16{50000}}
				defer os.assertClosed(t)
				bind := func(n string, ap netip.AddrPort) (netip.AddrPort, io.Closer, error) {
					bound, socket, err := os.bind(n, ap)
					if n == network && err == nil {
						if failClose {
							socket.(*portSocket).closeErr = closeErr
						}
						return bound, socket, bindErr
					}
					return bound, socket, err
				}
				r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 2, bind)
				if r != nil || !errors.Is(err, bindErr) || errors.Is(err, closeErr) != failClose {
					t.Fatalf("reservation=%v, err=%v", r, err)
				}
				attempts := 2
				if failClose {
					attempts = 1 // A cleanup failure must stop candidate probing.
				}
				if network == "udp4" {
					attempts *= 2
				}
				if len(os.calls) != attempts {
					t.Fatalf("bind calls=%d, want %d", len(os.calls), attempts)
				}
			})
		}
	}
}

func TestReservationReportsBothCloseErrorsOnce(t *testing.T) {
	os := &portOS{candidates: []uint16{50000}}
	defer os.assertClosed(t)
	r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 1, os.bind)
	if err != nil {
		t.Fatal(err)
	}
	tcpErr, udpErr := errors.New("test TCP close error"), errors.New("test UDP close error")
	os.sockets[0].closeErr, os.sockets[1].closeErr = tcpErr, udpErr
	first := r.Close()
	if !errors.Is(first, tcpErr) || !errors.Is(first, udpErr) || r.Close() != first {
		t.Fatalf("close did not retain both errors: %v", first)
	}
}

func TestReservationReportsRejectedCandidateCloseError(t *testing.T) {
	for _, rejection := range []string{"excluded", "malformed", "UDP bind failure"} {
		t.Run(rejection, func(t *testing.T) {
			os := &portOS{candidates: []uint16{50000}}
			defer os.assertClosed(t)
			closeErr := errors.New("test TCP close error")
			var excluded []uint16
			if rejection == "excluded" {
				excluded = []uint16{50000}
			}
			bind := func(network string, ap netip.AddrPort) (netip.AddrPort, io.Closer, error) {
				if network == "udp4" {
					return netip.AddrPort{}, nil, errPortExcluded
				}
				bound, socket, err := os.bind(network, ap)
				socket.(*portSocket).closeErr = closeErr
				if rejection == "malformed" {
					bound = netip.AddrPort{}
				}
				return bound, socket, err
			}
			if r, err := reserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 2, bind, excluded...); r != nil || !errors.Is(err, closeErr) {
				t.Fatalf("cleanup error lost: reservation=%v, err=%v", r, err)
			}
			if len(os.sockets) != 1 {
				t.Fatalf("continued probing after cleanup failed: %d sockets", len(os.sockets))
			}
		})
	}
}

func TestLoopbackPortSequenceCoversUnprivilegedRange(t *testing.T) {
	seen := make(map[int]bool)
	port := minLoopbackPort
	for range loopbackPortCount {
		if port < minLoopbackPort || port > 65535 || seen[port] {
			t.Fatalf("invalid or repeated candidate %d after %d ports", port, len(seen))
		}
		seen[port] = true
		port = nextLoopbackPort(port)
	}
	if port != minLoopbackPort {
		t.Fatalf("sequence did not return to its start: %d", port)
	}
}

func TestReservationUsesRealLoopbackSockets(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			r, err := ReserveLoopbackTCPUDP(netip.MustParseAddr(ip))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			}()
			suffix := "6"
			if r.Endpoint().Addr().Is4() {
				suffix = "4"
			}
			for _, network := range []string{"tcp" + suffix, "udp" + suffix} {
				endpoint, socket, err := bindLoopbackPort(network, r.Endpoint())
				if socket != nil {
					socket.Close()
				}
				if err == nil || endpoint.IsValid() || socket != nil {
					t.Fatalf("%s reservation was not held: endpoint=%s, socket present=%t, err=%v", network, endpoint, socket != nil, err)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			// Rebinding verifies release; fixtures still fail if the real product
			// cannot claim the endpoint after this unavoidable handoff window.
			_, listener, err := bindLoopbackPort("tcp"+suffix, r.Endpoint())
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			_, socket, err := bindLoopbackPort("udp"+suffix, r.Endpoint())
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
		})
	}
}
