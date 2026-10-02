package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
)

type fixturePortBinder func(network string, port int) (int, io.Closer, error)

func bindFixturePort(network string, port int) (int, io.Closer, error) {
	if network == "udp4" {
		l, err := net.ListenPacket(network, config.Loopback(port))
		if err != nil {
			return 0, nil, err
		}
		return l.LocalAddr().(*net.UDPAddr).Port, l, nil
	}
	l, err := net.Listen(network, config.Loopback(port))
	if err != nil {
		return 0, nil, err
	}
	return l.Addr().(*net.TCPAddr).Port, l, nil
}

// allocateFixturePorts claims the complete listener set at once, then releases
// it for the service. Keep rejected TCP anchors open so :0 cannot repeatedly
// select the same unusable ID port. A prior anchor can supply the next ID's
// lower neighbor; otherwise sequential OS allocation would block itself.
// The relay is independent: only the two ID TCP ports must be consecutive.
// At most maxAttempts+3 sockets are held, and all are closed on every exit.
func allocateFixturePorts(maxAttempts int, bind fixturePortBinder) (int, int, error) {
	anchors := make(map[int]io.Closer)
	defer func() {
		for _, l := range anchors {
			_ = l.Close()
		}
	}()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		id, anchor, err := bind("tcp4", 0)
		if err != nil {
			return 0, 0, fmt.Errorf("fixture TCP candidate %d/%d: %w", attempt, maxAttempts, err)
		}
		if _, exists := anchors[id]; exists {
			_ = anchor.Close()
			return 0, 0, fmt.Errorf("fixture TCP candidate %d/%d reused reserved port %d", attempt, maxAttempts, id)
		}
		anchors[id] = anchor
		if id < 1025 || id > 65535 {
			lastErr = fmt.Errorf("ID candidate %d is outside 1025..65535", id)
			continue
		}
		neighbor, held := anchors[id-1]
		if !held {
			_, neighbor, err = bind("tcp4", id-1)
			if err != nil {
				lastErr = fmt.Errorf("ID candidate %d, TCP %d: %w", id, id-1, err)
				continue
			}
		}
		_, udp, err := bind("udp4", id)
		if err != nil {
			if !held {
				_ = neighbor.Close()
			}
			lastErr = fmt.Errorf("ID candidate %d, UDP %d: %w", id, id, err)
			continue
		}
		defer udp.Close()
		if !held {
			defer neighbor.Close()
		}
		relay, listener, err := bind("tcp4", 0)
		if err != nil {
			return 0, 0, fmt.Errorf("fixture relay TCP after ID candidate %d/%d (port %d): %w", attempt, maxAttempts, id, err)
		}
		defer listener.Close()
		if relay < 1024 || relay > 65535 || relay == id || relay == id-1 {
			return 0, 0, fmt.Errorf("fixture relay TCP port %d is invalid for ID candidate %d/%d (port %d)", relay, attempt, maxAttempts, id)
		}
		return id, relay, nil
	}
	if lastErr == nil {
		return 0, 0, fmt.Errorf("fixture port search requires a positive attempt limit, got %d", maxAttempts)
	}
	return 0, 0, fmt.Errorf("no fixture ports after %d distinct TCP candidates: %w", maxAttempts, lastErr)
}

// The fake OS always returns the first available ephemeral candidate. Releasing
// a rejected anchor too soon therefore reproduces repeated allocation exactly.
type fixturePortOS struct {
	candidates []int
	failures   map[string]error
	active     map[string]*fixturePortSocket
	sockets    []*fixturePortSocket
	maxOpen    int
}

type fixturePortSocket struct {
	os         *fixturePortOS
	address    string
	closeCalls int
}

func (s *fixturePortSocket) Close() error {
	s.closeCalls++
	delete(s.os.active, s.address)
	return nil
}

var errFixturePortExhausted = errors.New("no ephemeral candidates remain")

func (os *fixturePortOS) bind(network string, port int) (int, io.Closer, error) {
	if os.active == nil {
		os.active = make(map[string]*fixturePortSocket)
	}
	if port == 0 {
		for _, candidate := range os.candidates {
			if os.active[fmt.Sprintf("%s:%d", network, candidate)] == nil {
				port = candidate
				break
			}
		}
		if port == 0 {
			return 0, nil, errFixturePortExhausted
		}
	}
	address := fmt.Sprintf("%s:%d", network, port)
	if err := os.failures[address]; err != nil {
		return 0, nil, err
	}
	if os.active[address] != nil {
		return 0, nil, fmt.Errorf("%s is already bound", address)
	}
	socket := &fixturePortSocket{os: os, address: address}
	os.active[address] = socket
	os.sockets = append(os.sockets, socket)
	os.maxOpen = max(os.maxOpen, len(os.active))
	return port, socket, nil
}

func (os *fixturePortOS) assertClosed(t *testing.T) {
	t.Helper()
	if len(os.active) != 0 {
		t.Errorf("leaked sockets: %v", os.active)
	}
	for _, socket := range os.sockets {
		if socket.closeCalls != 1 {
			t.Errorf("%s closed %d times, want once", socket.address, socket.closeCalls)
		}
	}
}

func TestFixturePortsRetainAndReuseRejectedAnchors(t *testing.T) {
	for _, failure := range []string{"tcp4:39999", "udp4:40000"} {
		t.Run(failure, func(t *testing.T) {
			os := &fixturePortOS{
				candidates: []int{40000, 40001, 53000},
				failures:   map[string]error{failure: errors.New("test bind failure")},
			}
			id, relay, err := allocateFixturePorts(2, os.bind)
			if err != nil || id != 40001 || relay != 53000 {
				t.Fatalf("ports = (%d, %d), error = %v", id, relay, err)
			}
			os.assertClosed(t)
			if os.maxOpen != 4 {
				t.Errorf("peak sockets = %d, want 4 (reuse anchor; release failed UDP neighbor)", os.maxOpen)
			}
		})
	}
}

func TestFixturePortsAcceptUpperIDBoundary(t *testing.T) {
	os := &fixturePortOS{candidates: []int{65535, 53000}}
	id, relay, err := allocateFixturePorts(1, os.bind)
	if err != nil || id != 65535 || relay != 53000 {
		t.Fatalf("ports = (%d, %d), error = %v", id, relay, err)
	}
	os.assertClosed(t)
}

func TestFixturePortsNativeRepeated(t *testing.T) {
	for iteration := 1; iteration <= 25; iteration++ {
		id, relay, err := allocateFixturePorts(100, bindFixturePort)
		if err != nil {
			t.Fatalf("iteration %d/25: %v", iteration, err)
		}
		c := config.Config{Version: 1, Mode: "forward", LocalIDPort: id, LocalRelayPort: relay}
		if err := Preflight(c); err != nil {
			t.Fatalf("iteration %d/25, ID=%d relay=%d: %v", iteration, id, relay, err)
		}
	}
}

func TestFixturePortsFailureDiagnosticsAndCleanup(t *testing.T) {
	bindErr := errors.New("test socket permission denied")
	for _, tc := range []struct {
		name       string
		attempts   int
		candidates []int
		failures   map[string]error
		wantCause  error
		wantText   []string
	}{
		{"neighbor exhaustion", 2, []int{40000, 41000}, map[string]error{"tcp4:39999": bindErr, "tcp4:40999": bindErr}, bindErr, []string{"2 distinct TCP candidates", "ID candidate 41000", "TCP 40999"}},
		{"UDP exhaustion", 2, []int{40000, 40001}, map[string]error{"udp4:40000": bindErr, "udp4:40001": bindErr}, bindErr, []string{"2 distinct TCP candidates", "ID candidate 40001", "UDP 40001"}},
		{"invalid candidates", 2, []int{1024, 1023}, nil, nil, []string{"2 distinct TCP candidates", "ID candidate 1023", "outside 1025..65535"}},
		{"candidate allocation", 2, nil, nil, errFixturePortExhausted, []string{"TCP candidate 1/2"}},
		{"relay allocation", 2, []int{40000}, nil, errFixturePortExhausted, []string{"relay TCP", "ID candidate 1/2", "port 40000"}},
		{"invalid relay", 2, []int{40000, 1023}, nil, nil, []string{"relay TCP port 1023", "ID candidate 1/2"}},
		{"invalid limit", 0, nil, nil, nil, []string{"positive attempt limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os := &fixturePortOS{candidates: tc.candidates, failures: tc.failures}
			id, relay, err := allocateFixturePorts(tc.attempts, os.bind)
			if err == nil || id != 0 || relay != 0 {
				t.Fatalf("ports = (%d, %d), error = %v; want no ports and an error", id, relay, err)
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Errorf("error = %v; want cause %v", err, tc.wantCause)
			}
			for _, text := range tc.wantText {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("error = %q; missing %q", err, text)
				}
			}
			os.assertClosed(t)
			if os.maxOpen > tc.attempts+3 {
				t.Errorf("peak sockets = %d exceeds attempt budget + 3", os.maxOpen)
			}
		})
	}
}
