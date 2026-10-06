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

const (
	fixtureMinIDPort   = 1025
	fixtureIDPortCount = 65535 - fixtureMinIDPort + 1
)

func nextFixtureIDPort(port int) int {
	// This stride spreads consecutive probes across the port range. It is
	// coprime to 64511, so every valid ID is visited once before repeating.
	return fixtureMinIDPort + (port-fixtureMinIDPort+39869)%fixtureIDPortCount
}

// allocateFixturePorts starts with an OS-selected UDP ID anchor, then spreads
// explicit probes across the legal ID range. TCP and UDP exclusions can differ,
// and either :0 allocator can remain in a cluster forbidden to the other. Each
// candidate must still bind UDP, both TCP ports, and an independent TCP relay.
// Rejected UDP anchors remain held; partial TCP claims are released immediately.
// At most maxAttempts+3 sockets are held, and every exit closes them all.
func allocateFixturePorts(maxAttempts int, bind fixturePortBinder) (int, int, error) {
	anchors := make(map[int]io.Closer)
	defer func() {
		for _, l := range anchors {
			_ = l.Close()
		}
	}()
	var lastErr error
	nextID := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		requestedID := nextID
		id, anchor, err := bind("udp4", requestedID)
		if requestedID != 0 {
			nextID = nextFixtureIDPort(requestedID)
		}
		if err != nil {
			if requestedID == 0 {
				return 0, 0, fmt.Errorf("fixture UDP candidate %d/%d: %w", attempt, maxAttempts, err)
			}
			lastErr = fmt.Errorf("ID candidate %d, UDP %d: %w", requestedID, requestedID, err)
			continue
		}
		if requestedID == 0 {
			nextID = fixtureMinIDPort
			if id >= fixtureMinIDPort && id <= 65535 {
				nextID = nextFixtureIDPort(id)
			}
		}
		if _, exists := anchors[id]; exists {
			_ = anchor.Close()
			return 0, 0, fmt.Errorf("fixture UDP candidate %d/%d reused reserved port %d", attempt, maxAttempts, id)
		}
		anchors[id] = anchor
		if id < fixtureMinIDPort || id > 65535 {
			lastErr = fmt.Errorf("ID candidate %d is outside 1025..65535", id)
			continue
		}
		_, tcp, err := bind("tcp4", id)
		if err != nil {
			lastErr = fmt.Errorf("ID candidate %d, TCP %d: %w", id, id, err)
			continue
		}
		_, neighbor, err := bind("tcp4", id-1)
		if err != nil {
			_ = tcp.Close()
			lastErr = fmt.Errorf("ID candidate %d, TCP %d: %w", id, id-1, err)
			continue
		}
		relay, listener, err := bind("tcp4", 0)
		if err != nil {
			_ = tcp.Close()
			_ = neighbor.Close()
			return 0, 0, fmt.Errorf("fixture relay TCP after ID candidate %d/%d (port %d): %w", attempt, maxAttempts, id, err)
		}
		defer tcp.Close()
		defer neighbor.Close()
		defer listener.Close()
		if relay < 1024 || relay > 65535 || relay == id || relay == id-1 {
			return 0, 0, fmt.Errorf("fixture relay TCP port %d is invalid for ID candidate %d/%d (port %d)", relay, attempt, maxAttempts, id)
		}
		return id, relay, nil
	}
	if lastErr == nil {
		return 0, 0, fmt.Errorf("fixture port search requires a positive attempt limit, got %d", maxAttempts)
	}
	return 0, 0, fmt.Errorf("no fixture ports after %d candidate attempts: %w", maxAttempts, lastErr)
}

// The fake OS always returns the first available ephemeral candidate, modeling
// clustered allocations and reuse after sockets are released.
type fixturePortOS struct {
	candidates []int
	failures   map[string]error
	excluded   map[string][][2]int
	active     map[string]*fixturePortSocket
	sockets    []*fixturePortSocket
	maxOpen    int
	bindCalls  int
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
var errFixturePortExcluded = errors.New("test excluded port range")

func (os *fixturePortOS) unavailable(network string, port int) error {
	if err := os.failures[fmt.Sprintf("%s:%d", network, port)]; err != nil {
		return err
	}
	for _, ports := range os.excluded[network] {
		if port >= ports[0] && port <= ports[1] {
			return errFixturePortExcluded
		}
	}
	return nil
}

func (os *fixturePortOS) bind(network string, port int) (int, io.Closer, error) {
	os.bindCalls++
	if os.active == nil {
		os.active = make(map[string]*fixturePortSocket)
	}
	if port == 0 {
		for _, candidate := range os.candidates {
			if os.active[fmt.Sprintf("%s:%d", network, candidate)] == nil && os.unavailable(network, candidate) == nil {
				port = candidate
				break
			}
		}
		if port == 0 {
			return 0, nil, errFixturePortExhausted
		}
	}
	address := fmt.Sprintf("%s:%d", network, port)
	if err := os.unavailable(network, port); err != nil {
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

func TestFixturePortsRetainUDPAnchorsAndReleasePartialTCP(t *testing.T) {
	os := &fixturePortOS{candidates: []int{40000, 40001, 53000}, failures: map[string]error{"tcp4:39999": errors.New("test bind failure")}}
	id, relay, err := allocateFixturePorts(2, os.bind)
	if err != nil || id != 15358 || relay != 40000 {
		t.Fatalf("ports=(%d,%d), err=%v", id, relay, err)
	}
	os.assertClosed(t)
	if os.maxOpen != 5 {
		t.Fatalf("peak sockets=%d, want five bounded claims", os.maxOpen)
	}
}

func TestFixturePortProbeSequenceCoversLegalRange(t *testing.T) {
	seen := make(map[int]bool)
	port := fixtureMinIDPort
	for range fixtureIDPortCount {
		if port < fixtureMinIDPort || port > 65535 || seen[port] {
			t.Fatalf("invalid or repeated candidate %d after %d distinct ports", port, len(seen))
		}
		seen[port] = true
		port = nextFixtureIDPort(port)
	}
	if port != fixtureMinIDPort {
		t.Fatalf("sequence ended at %d, want start of next cycle %d", port, fixtureMinIDPort)
	}
}

func TestFixturePortsBoundedExhaustion(t *testing.T) {
	const attempts = 100
	os := &fixturePortOS{candidates: []int{49152}, excluded: map[string][][2]int{"tcp4": {{1024, 65535}}}}
	defer os.assertClosed(t)
	id, relay, err := allocateFixturePorts(attempts, os.bind)
	if id != 0 || relay != 0 || !errors.Is(err, errFixturePortExcluded) {
		t.Fatalf("ports=(%d,%d), error=%v; want excluded-port failure", id, relay, err)
	}
	if os.bindCalls != 2*attempts || os.maxOpen != attempts {
		t.Fatalf("search used %d binds and %d simultaneous sockets; want %d and %d", os.bindCalls, os.maxOpen, 2*attempts, attempts)
	}
}

func TestFixturePortsPreferUsableUDPRange(t *testing.T) {
	os := &fixturePortOS{candidates: []int{40000, 65000}, failures: map[string]error{"udp4:65000": errors.New("excluded UDP range")}}
	binder := func(network string, port int) (int, io.Closer, error) {
		if network == "tcp4" && port == 0 {
			return os.bind(network, 65000)
		}
		return os.bind(network, port)
	}
	id, relay, err := allocateFixturePorts(2, binder)
	if err != nil || id != 40000 || relay != 65000 {
		t.Fatalf("UDP allocator was not authoritative: %d %d %v", id, relay, err)
	}
	os.assertClosed(t)
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

func TestFixturePortsProbeAcrossExcludedRanges(t *testing.T) {
	// Both OS allocators choose adjacent candidates. Each first cluster is
	// wider than the entire search budget and forbidden to the other protocol.
	var candidates []int
	for _, start := range []int{49152, 54000} {
		for port := start; port < start+200; port++ {
			candidates = append(candidates, port)
		}
	}
	for _, tc := range []struct {
		name      string
		excluded  map[string][][2]int
		wantID    int
		wantRelay int
	}{
		{"opposite wide clusters", map[string][][2]int{"tcp4": {{49000, 52000}}, "udp4": {{53000, 55000}}}, 24510, 54000},
		{"reversed wide clusters", map[string][][2]int{"tcp4": {{53000, 55000}}, "udp4": {{49000, 52000}}}, 29358, 49152},
		{"TCP neighbor exclusion", map[string][][2]int{"tcp4": {{49000, 52000}, {24509, 24509}}, "udp4": {{53000, 55000}}}, 64379, 54000},
		{"UDP explicit probe exclusion", map[string][][2]int{"tcp4": {{49000, 52000}}, "udp4": {{24000, 26000}, {53000, 55000}}}, 64379, 54000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os := &fixturePortOS{candidates: candidates, excluded: tc.excluded}
			defer os.assertClosed(t)
			id, relay, err := allocateFixturePorts(100, os.bind)
			if err != nil || id != tc.wantID || relay != tc.wantRelay {
				t.Fatalf("ports=(%d,%d), error=%v; want (%d,%d)", id, relay, err, tc.wantID, tc.wantRelay)
			}
			if os.bindCalls > 10 || os.maxOpen > 6 {
				t.Fatalf("search used %d binds and %d simultaneous sockets; want at most 10 and 6", os.bindCalls, os.maxOpen)
			}
		})
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
		{"neighbor exhaustion", 2, []int{40000, 41000}, map[string]error{"tcp4:39999": bindErr, "tcp4:15357": bindErr}, bindErr, []string{"2 candidate attempts", "ID candidate 15358", "TCP 15357"}},
		{"TCP ID exhaustion", 2, []int{40000, 41000}, map[string]error{"tcp4:40000": bindErr, "tcp4:15358": bindErr}, bindErr, []string{"2 candidate attempts", "ID candidate 15358", "TCP 15358"}},
		{"UDP probe exhaustion", 2, []int{40000}, map[string]error{"tcp4:40000": bindErr, "udp4:15358": bindErr}, bindErr, []string{"2 candidate attempts", "ID candidate 15358", "UDP 15358"}},
		{"invalid lower candidate", 1, []int{1024}, nil, nil, []string{"1 candidate attempts", "ID candidate 1024", "outside 1025..65535"}},
		{"invalid upper candidate", 1, []int{65536}, nil, nil, []string{"1 candidate attempts", "ID candidate 65536", "outside 1025..65535"}},
		{"candidate allocation", 2, nil, nil, errFixturePortExhausted, []string{"UDP candidate 1/2"}},
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
