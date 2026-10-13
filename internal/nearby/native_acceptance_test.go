package nearby

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

const nativeCapabilityMarker = "NEARBY_NATIVE_CAPABILITY "

// The only constructor selecting this policy lives in test code. There is no
// production flag, environment switch or public Config field for this seam.
func isolatedLoopbackPolicy() discoveryPolicy { return discoveryPolicy{isolatedLoopbackTest: true} }

func isolatedLoopbackScope() (Scope, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return Scope{}, ErrUnsupported
	}
	var selected Scope
	matches := 0
	for _, ifi := range interfaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			return Scope{}, ErrUnsupported
		}
		for _, raw := range addrs {
			prefix, err := netip.ParsePrefix(raw.String())
			if err == nil && prefix.Addr() == netip.MustParseAddr("127.0.0.1") {
				selected = Scope{InterfaceIndex: ifi.Index, LocalAddress: prefix.Addr(), Prefix: netip.MustParsePrefix("127.0.0.1/32")}
				matches++
			}
		}
	}
	if matches != 1 {
		return Scope{}, ErrUnsupported
	}
	return selected, nil
}

type nativeCapabilityResult struct {
	Status                  string                 `json:"status"`
	Reason                  string                 `json:"reason"`
	Scope                   string                 `json:"scope"`
	BidirectionalCandidates bool                   `json:"bidirectionalCandidates"`
	OwnersJoined            bool                   `json:"ownersJoined"`
	SocketFailureStage      string                 `json:"socketFailureStage"`
	SendFailureKind         string                 `json:"sendFailureKind"`
	WindowsNoAncillaryProbe *nativeSendProbeResult `json:"windowsNoAncillaryProbe"`
	selectedScope           Scope
	probeDeadline           time.Time
}

// This result marker, not the enclosing go test exit status or package PASS,
// is the native acceptance evidence. A platform incapable of safe loopback
// delivery is explicitly skipped/unavailable, never called verified.
func TestNativeLoopbackDiscoveryCapability(t *testing.T) {
	result := nativeCapabilityResult{Status: "not_run", Reason: "opt_in_required", Scope: "isolated_loopback", SocketFailureStage: "none", SendFailureKind: "none"}
	if os.Getenv("SOBALINK_NATIVE_NEARBY_LOOPBACK") == "1" {
		result = runNativeLoopbackCapability()
		if result.Status == "unavailable" && result.SocketFailureStage == "socket_write" && result.OwnersJoined {
			recordNativeSendProbe(&result, runPlatformSendProbe(result.selectedScope, result.probeDeadline))
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(nativeCapabilityMarker + string(data))
	switch result.Status {
	case "verified":
	case "not_run", "unavailable":
		t.Skip("native nearby discovery was not verified: " + result.Reason)
	default:
		t.Fatal("native nearby capability failed: " + result.Reason)
	}
}

var errCapabilityScope = errors.New("native capability packet escaped exact loopback metadata")

type observedNativeTransport struct {
	Transport
	scope         Scope
	mu            sync.Mutex
	reads, writes int
	badMetadata   bool
	closeOnce     sync.Once
	closed        bool
	closeErr      error
}

func (t *observedNativeTransport) ReadPacket() (Packet, error) {
	p, err := t.Transport.ReadPacket()
	if err != nil {
		return p, err
	}
	// Fail rather than skip if any native receive exposes foreign/absent scope
	// metadata. Do not print packet contents or interface identifiers.
	if p.Source.Addr() != t.scope.LocalAddress || p.Source.Port() != mdnsPort || p.Destination != multicast4 || p.InterfaceIndex != t.scope.InterfaceIndex || p.HopLimit != 255 {
		t.mu.Lock()
		t.badMetadata = true
		t.mu.Unlock()
		return Packet{}, errCapabilityScope
	}
	t.mu.Lock()
	t.reads++
	t.mu.Unlock()
	return p, nil
}
func (t *observedNativeTransport) WritePacket(data []byte) error {
	err := t.Transport.WritePacket(data)
	if err == nil {
		t.mu.Lock()
		t.writes++
		t.mu.Unlock()
	}
	return err
}
func (t *observedNativeTransport) Close() error {
	t.closeOnce.Do(func() {
		err := normalizedCloseError(t.Transport.Close())
		t.mu.Lock()
		t.closed = true
		t.closeErr = err
		t.mu.Unlock()
	})
	return t.closeErr
}
func (t *observedNativeTransport) snapshot() (reads, writes int, bad, closed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reads, t.writes, t.badMetadata, t.closed && t.closeErr == nil
}

func runNativeLoopbackCapability() (result nativeCapabilityResult) {
	result = nativeCapabilityResult{Status: "unavailable", Reason: "loopback_scope_unavailable", Scope: "isolated_loopback", SocketFailureStage: "none", SendFailureKind: "none"}
	scope, err := isolatedLoopbackScope()
	if err != nil {
		return result
	}
	policy := isolatedLoopbackPolicy()
	result.selectedScope, result.probeDeadline = scope, time.Now().Add(8*time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), result.probeDeadline)
	var opened []*observedNativeTransport
	var sessions []*Session
	// Cleanup is shared by success, capability failure and assertion failure.
	// Close every actual socket and join every Session before emitting a marker.
	defer func() {
		cancel()
		completions := make(chan error, len(sessions)+len(opened))
		for _, session := range sessions {
			go func(s *Session) { completions <- s.Close() }(session)
		}
		for _, transport := range opened {
			go func(tr *observedNativeTransport) { completions <- tr.Close() }(transport)
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		cleanupFailed := false
		for remaining := len(sessions) + len(opened); remaining > 0; remaining-- {
			select {
			case err := <-completions:
				cleanupFailed = cleanupFailed || err != nil
			case <-timer.C:
				result.Status, result.Reason = "failed", "cleanup_not_joined"
				result.OwnersJoined = false
				return
			}
		}
		if cleanupFailed {
			result.Status, result.Reason = "failed", "cleanup_close_uncertain"
			return
		}
		for _, s := range sessions {
			select {
			case <-s.Done():
			default:
				result.Status, result.Reason = "failed", "session_not_joined"
				return
			}
			if len(s.Candidates()) != 0 {
				result.Status, result.Reason = "failed", "candidate_survived_close"
				return
			}
		}
		for _, tr := range opened {
			_, _, bad, closed := tr.snapshot()
			if bad {
				result.Status, result.Reason = "failed", "foreign_or_missing_packet_metadata"
				return
			}
			if !closed {
				result.Status, result.Reason = "failed", "socket_not_closed"
				return
			}
		}
		result.OwnersJoined = len(sessions) == 2
		if result.Status == "verified" && !result.OwnersJoined {
			result.Status, result.Reason = "failed", "owners_missing"
		}
	}()
	// Open both native memberships before starting announcements. The mandatory
	// mDNS port is never replaced with a wildcard/alternate-NIC fallback. No
	// bootstrap listener or active TCP connection is opened by this test.
	for i := 0; i < 2; i++ {
		transport, err := nativeFactory(scope, policy)
		if transport != nil {
			opened = append(opened, &observedNativeTransport{Transport: transport, scope: scope})
		}
		if err != nil {
			result.Reason = "native_socket_unavailable"
			result.SocketFailureStage = socketFailureStage(err)
			result.SendFailureKind = nativeSendFailureKind(err)
			if errors.Is(err, ErrCloseUncertain) || errors.Is(err, ErrCloseUnresolved) {
				result.Status, result.Reason = "failed", "startup_cleanup_uncertain"
			}
			return result
		}
		if transport == nil {
			result.Status, result.Reason = "failed", "native_owner_missing"
			return result
		}
	}
	var advertisements []Advertisement
	for i := 0; i < 2; i++ {
		sid, err := NewSessionID()
		if err != nil {
			result.Status, result.Reason = "failed", "session_entropy_unavailable"
			return result
		}
		ad := Advertisement{SessionID: sid, Alias: []string{"Peer A", "Peer B"}[i], Protocol: Protocol, Capability: Capability, Bootstrap: netip.AddrPortFrom(scope.LocalAddress, uint16(42000+i))}
		advertisements = append(advertisements, ad)
		transport := opened[i]
		factory := func(selected Scope) (Transport, error) {
			if selected != scope {
				return nil, ErrInvalidScope
			}
			return transport, nil
		}
		s, err := start(ctx, Config{Scope: scope, Advertisement: ad}, factory, policy)
		if s != nil {
			sessions = append(sessions, s)
		}
		if err != nil {
			result.Status, result.Reason = "failed", "native_session_start_failed"
			result.SocketFailureStage = socketFailureStage(err)
			result.SendFailureKind = nativeSendFailureKind(err)
			if !errors.Is(err, ErrCloseUncertain) && !errors.Is(err, ErrCloseUnresolved) && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSocketUnavailable) || errors.Is(err, ErrUnsupported)) {
				result.Status, result.Reason = "unavailable", "native_session_unavailable"
			}
			return result
		}
		if s == nil {
			result.Status, result.Reason = "failed", "native_session_missing"
			return result
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		complete := true
		for i, s := range sessions {
			if err := s.Err(); err != nil {
				result.Status, result.Reason = "failed", "native_session_failed"
				result.SocketFailureStage = socketFailureStage(err)
				result.SendFailureKind = nativeSendFailureKind(err)
				if !errors.Is(err, ErrCloseUncertain) && !errors.Is(err, ErrCloseUnresolved) && (errors.Is(err, ErrSocketUnavailable) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrScopeChanged) || errors.Is(err, context.DeadlineExceeded)) {
					result.Status, result.Reason = "unavailable", "native_receive_unavailable"
				}
				if errors.Is(err, errCapabilityScope) {
					result.Status, result.Reason = "failed", "foreign_or_missing_packet_metadata"
				}
				return result
			}
			matched := false
			for _, candidate := range s.Candidates() {
				if candidate.Advertisement != advertisements[1-i] {
					continue
				}
				checked, err := s.Lookup(candidate.ID)
				if err != nil || checked.ID != candidate.ID || checked.Advertisement != candidate.Advertisement || checked.Scope != scope || checked.Source != netip.AddrPortFrom(scope.LocalAddress, mdnsPort) || !time.Now().Before(checked.ExpiresAt) {
					result.Status, result.Reason = "failed", "candidate_binding_or_freshness_mismatch"
					return result
				}
				matched = true
			}
			reads, writes, bad, _ := opened[i].snapshot()
			if bad {
				result.Status, result.Reason = "failed", "foreign_or_missing_packet_metadata"
				return result
			}
			complete = complete && matched && reads > 0 && writes > 0
		}
		if complete {
			result.Status, result.Reason = "verified", "bidirectional_scoped_mdns"
			result.BidirectionalCandidates = true
			return result
		}
		select {
		case <-ctx.Done():
			result.Reason = "native_receive_timeout"
			return result
		case <-ticker.C:
		}
	}
}

func TestLoopbackCapabilitySeamNeverBroadensPublicAPI(t *testing.T) {
	p := isolatedLoopbackPolicy()
	scope := Scope{7, netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.1/32")}
	ad := testAd(1)
	ad.Bootstrap = netip.MustParseAddrPort("127.0.0.1:42000")
	if scope.Validate() == nil || ad.Validate() == nil {
		t.Fatal("exported validation accepts test scope")
	}
	opened := false
	_, err := Start(context.Background(), Config{Scope: scope, Advertisement: ad}, func(Scope) (Transport, error) { opened = true; return newFakeTransport(), nil })
	if err == nil || opened {
		t.Fatal("public Start opened loopback transport")
	}
	if _, err := NativeFactory(scope); !errors.Is(err, ErrInvalidScope) {
		t.Fatal("public native factory accepts loopback")
	}
	if p.validateScope(scope) != nil || ad.validate(p) != nil {
		t.Fatal("exact test scope unavailable")
	}
	for _, ip := range []string{"127.0.0.2", "192.168.40.1", "0.0.0.0", "::1", "::ffff:127.0.0.1"} {
		changed := scope
		changed.LocalAddress = netip.MustParseAddr(ip)
		if p.validateScope(changed) == nil || p.validAddress(changed.LocalAddress) {
			t.Fatal("test policy admits another address")
		}
	}
	changed := scope
	changed.Prefix = netip.MustParsePrefix("127.0.0.0/8")
	if p.validateScope(changed) == nil {
		t.Fatal("test prefix widened")
	}
	ifi := net.Interface{Index: 7, Name: "synthetic-loopback", Flags: net.FlagUp | net.FlagLoopback}
	addrs := []net.Addr{&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)}}
	if !bindingMatchesWithPolicy(scope, ifi, addrs, &ifi, p) {
		t.Fatal("valid test binding rejected")
	}
	nonloopback := ifi
	nonloopback.Flags = net.FlagUp | net.FlagMulticast
	if bindingMatchesWithPolicy(scope, nonloopback, addrs, &nonloopback, p) {
		t.Fatal("test scope accepted another interface class")
	}
	renamed := ifi
	renamed.Name = "replacement-loopback"
	if bindingMatchesWithPolicy(scope, renamed, addrs, &ifi, p) {
		t.Fatal("test scope accepted replaced interface")
	}
	data, err := packAdvertisementWithPolicy(ad, p)
	if err != nil {
		t.Fatal(err)
	}
	packet := Packet{Data: data, Source: netip.MustParseAddrPort("127.0.0.1:5353"), Destination: multicast4, InterfaceIndex: 7, HopLimit: 255}
	if validatePacket(scope, packet) {
		t.Fatal("public wire validation accepts loopback")
	}
	if _, _, _, err := unpackPacket(packet); err == nil {
		t.Fatal("public parser accepts loopback bootstrap")
	}
	got, _, _, err := unpackPacketWithPolicy(packet, p)
	if err != nil || got != ad || !validatePacketWithPolicy(scope, packet, p) {
		t.Fatal("exact test packet rejected")
	}
	packet.InterfaceIndex++
	if validatePacketWithPolicy(scope, packet, p) {
		t.Fatal("test scope accepted foreign interface")
	}
}

func TestNativeCapabilityAuditRejectsForeignMetadata(t *testing.T) {
	scope := Scope{7, netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.1/32")}
	for _, mutate := range []func(*Packet){
		func(p *Packet) { p.Source = netip.MustParseAddrPort("127.0.0.2:5353") },
		func(p *Packet) { p.Source = netip.MustParseAddrPort("127.0.0.1:50000") },
		func(p *Packet) { p.InterfaceIndex++ }, func(p *Packet) { p.HopLimit = 0 },
		func(p *Packet) { p.Destination = scope.LocalAddress },
	} {
		transport := newFakeTransport()
		audit := &observedNativeTransport{Transport: transport, scope: scope}
		packet := Packet{Data: packQuery(), Source: netip.MustParseAddrPort("127.0.0.1:5353"), Destination: multicast4, InterfaceIndex: 7, HopLimit: 255}
		mutate(&packet)
		transport.incoming <- packet
		if _, err := audit.ReadPacket(); !errors.Is(err, errCapabilityScope) {
			t.Fatal("foreign or missing metadata accepted")
		}
		_, _, bad, _ := audit.snapshot()
		if !bad {
			t.Fatal("scope failure not recorded")
		}
		_ = audit.Close()
	}
}

// This supplemental probe can never verify production discovery. Its input
// socket and destination scope remain identical; only test send ancillary data
// is omitted to distinguish control rejection from loopback path failure.
type nativeSendProbeResult struct {
	Status                      string `json:"status"`
	Reason                      string `json:"reason"`
	SocketFailureStage          string `json:"socketFailureStage"`
	SendFailureKind             string `json:"sendFailureKind"`
	MetadataVerified            bool   `json:"metadataVerified"`
	OwnersJoined                bool   `json:"ownersJoined"`
	ProductionDiscoveryVerified bool   `json:"productionDiscoveryVerified"`
}

func recordNativeSendProbe(result *nativeCapabilityResult, probe *nativeSendProbeResult) {
	result.WindowsNoAncillaryProbe = probe
	// A diagnostic alternative never upgrades production evidence. A safety
	// invariant failure is fatal even when production merely was unavailable.
	if probe != nil && probe.Status == "failed" {
		result.Status, result.Reason = "failed", probe.Reason
	}
}
