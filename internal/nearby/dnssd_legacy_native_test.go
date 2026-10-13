//go:build darwin && cgo

package nearby

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

func TestDNSServiceLegacySerializationCapability(t *testing.T) {
	result := dnssdLegacyDefault()
	if os.Getenv("SOBALINK_DNSSD_LEGACY_SERIALIZATION") == "1" {
		result = runDNSServiceLegacySerialization()
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("NEARBY_DNSSD_LEGACY_CAPABILITY " + string(data))
	if result.Status == "not_run" || result.Status == "unavailable" {
		t.Skip("legacy DNS serialization not verified: " + result.Reason)
	}
	if result.Status != "verified" {
		t.Fatal("legacy DNS diagnostic failed: " + result.Reason)
	}
}

// This unicast ephemeral socket is a test-only RFC6762 one-shot querier. It is
// never a production discovery fallback or a replacement5353 multicast owner.
type dnssdLegacySocket struct {
	conn     *net.UDPConn
	packet   *ipv4.PacketConn
	local    netip.AddrPort
	once     sync.Once
	closeErr error
}

func (s *dnssdLegacySocket) ReadPacket() (Packet, error) { return dnssdReadDiagnosticPacket(s.conn) }
func (*dnssdLegacySocket) WritePacket([]byte) error      { return ErrUnsupported }
func (s *dnssdLegacySocket) Close() error {
	s.once.Do(func() { s.closeErr = normalizedCloseError(s.packet.Close()) })
	return s.closeErr
}
func openDNSServiceLegacySocket(ctx context.Context, scope Scope) (*dnssdLegacySocket, Transport, error) {
	ifi, err := currentBinding(scope, nil)
	if err != nil {
		return nil, nil, err
	}
	config := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		var optionErr error
		if network != "udp4" || address != net.JoinHostPort(scope.LocalAddress.String(), "0") {
			return ErrInvalidScope
		}
		if err := raw.Control(func(fd uintptr) {
			optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, scope.InterfaceIndex)
		}); err != nil {
			return err
		}
		return optionErr
	}}
	raw, err := config.ListenPacket(ctx, "udp4", net.JoinHostPort(scope.LocalAddress.String(), "0"))
	if err != nil {
		return nil, nil, err
	}
	conn, ok := raw.(*net.UDPConn)
	if !ok {
		retained, e := rollbackTransport(&closeOnlyTransport{close: raw.Close}, ErrUnsupported)
		return nil, retained, e
	}
	socket := &dnssdLegacySocket{conn: conn, packet: ipv4.NewPacketConn(conn)}
	fail := func(cause error) (*dnssdLegacySocket, Transport, error) {
		retained, e := rollbackTransport(socket, cause)
		return nil, retained, e
	}
	bound, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return fail(ErrInvalidScope)
	}
	socket.local = bound.AddrPort()
	if socket.local.Addr() != scope.LocalAddress || socket.local.Port() < 49152 || (socket.local.Port() >= 54543 && socket.local.Port() <= 54545) {
		return fail(ErrInvalidScope)
	}
	for _, apply := range []func() error{
		func() error { return conn.SetReadBuffer(16 * MaxDatagramBytes) },
		func() error {
			return socket.packet.SetControlMessage(ipv4.FlagTTL|ipv4.FlagDst|ipv4.FlagInterface, true)
		},
		func() error { return socket.packet.SetMulticastInterface(&ifi) },
		func() error { return socket.packet.SetMulticastTTL(255) },
		func() error { return socket.packet.SetMulticastLoopback(true) },
	} {
		if err := apply(); err != nil {
			return fail(err)
		}
	}
	return socket, socket, nil
}
func runDNSServiceLegacySerialization() (result dnssdLegacyResult) {
	result = dnssdLegacyDefault()
	result.Status, result.Reason = "unavailable", "readiness_deadline"
	result.ReceiveCounters = &dnssdReceiveCounters{}
	scope, err := dnssdSinglePrivateScope()
	if err != nil {
		result.Reason = "private_scope_unavailable"
		return
	}
	loopScope, err := isolatedLoopbackScope()
	if err != nil {
		result.Reason = "loopback_scope_unavailable"
		return
	}
	original, err := currentBinding(scope, nil)
	if err != nil {
		result.Reason = "selected_binding_changed"
		return
	}
	loopOriginal, err := currentBindingWithPolicy(loopScope, nil, isolatedLoopbackPolicy())
	if err != nil {
		result.Reason = "loopback_scope_unavailable"
		return
	}
	cfgs, err := dnssdSyntheticConfigs(scope)
	if err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	advert := cfgs[0].Advertisement
	var entropy [2]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	id := binary.BigEndian.Uint16(entropy[:]) | 0x8000
	query, err := packLegacyQuery(advert, id)
	if err != nil || len(query) > 512 {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	deadline, _ := ctx.Deadline()
	var ownedSocket Transport
	var api *dnssdCapabilityPeer
	apiErr := make(chan error, 2)
	apiReady := make(chan struct{}, 1)
	readDone, watchDone := make(chan struct{}), make(chan struct{})
	readStarted, watchStarted := false, false
	failure := func(err error) { dnssdLegacyError(&result, err) }
	defer func() {
		cancel()
		closed := make(chan error, 2)
		count := 0
		if ownedSocket != nil {
			count++
			go func() { closed <- ownedSocket.Close() }()
		}
		if api != nil {
			api.close()
			count++
			go func() { <-api.closed; api.mu.Lock(); e := api.closeErr; api.mu.Unlock(); closed <- e }()
			if !api.started {
				close(api.done)
			}
		}
		timer := time.NewTimer(closeJoinTimeout)
		defer timer.Stop()
		badCleanup := func(reason string) {
			result.Status, result.Reason = "failed", reason
			result.LegacySerializationVerified = false
		}
		for i := 0; i < count; i++ {
			select {
			case e := <-closed:
				if e != nil {
					badCleanup("cleanup_uncertain")
					return
				}
			case <-timer.C:
				badCleanup("owners_not_joined")
				return
			}
		}
		joins := []<-chan struct{}{}
		if api != nil {
			joins = append(joins, api.done)
		}
		if readStarted {
			joins = append(joins, readDone)
		}
		if watchStarted {
			joins = append(joins, watchDone)
		}
		for _, done := range joins {
			select {
			case <-done:
			case <-timer.C:
				badCleanup("owners_not_joined")
				return
			}
		}
		for {
			select {
			case e := <-apiErr:
				failure(e)
			default:
				goto drained
			}
		}
	drained:
		result.OwnersJoined = ownedSocket != nil && api != nil
		if result.Status == "verified" && !result.OwnersJoined {
			badCleanup("owners_missing")
		}
	}()
	socket, retained, err := openDNSServiceLegacySocket(ctx, scope)
	ownedSocket = retained
	if err != nil {
		result.Reason = "legacy_socket_unavailable"
		result.FailureStage, result.FailureKind = "legacy_socket", "unavailable"
		return
	}
	if err = socket.conn.SetReadDeadline(deadline); err != nil {
		result.Reason = "legacy_socket_unavailable"
		result.FailureStage, result.FailureKind = "legacy_socket", "unavailable"
		return
	}
	reg, err := dnssdRegistrationFor(cfgs[0], discoveryPolicy{})
	if err != nil {
		failure(err)
		return
	}
	backend, err := newNativeDNSServiceBackend(reg)
	if backend != nil {
		api = &dnssdCapabilityPeer{backend: backend, done: make(chan struct{}), closed: make(chan struct{})}
	}
	if err != nil {
		failure(err)
		return
	}
	binding := func() error {
		if _, err := currentBinding(scope, &original); err != nil {
			return err
		}
		_, err := currentBindingWithPolicy(loopScope, &loopOriginal, isolatedLoopbackPolicy())
		return err
	}
	watchStarted = true
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if e := binding(); e != nil {
					apiErr <- e
					cancel()
					return
				}
			}
		}
	}()
	api.started = true
	go func() {
		defer close(api.done)
		if e := binding(); e != nil {
			apiErr <- e
			return
		}
		if e := backend.Start(); e != nil {
			apiErr <- e
			return
		}
		for _, record := range reg.Records[1:] {
			if ctx.Err() != nil {
				return
			}
			if e := binding(); e != nil {
				apiErr <- e
				return
			}
			if e := backend.Query(record.Name, record.Type); e != nil {
				apiErr <- e
				return
			}
		}
		mask := uint8(0)
		reported := false
		for n := 0; n < MaxReceivedDatagrams; n++ {
			event, e := backend.Next()
			if e != nil {
				if !errors.Is(e, context.Canceled) {
					apiErr <- e
				}
				return
			}
			if e := binding(); e != nil {
				apiErr <- e
				return
			}
			if event.InterfaceIndex != uint32(scope.InterfaceIndex) {
				apiErr <- ErrInvalidPacket
				return
			}
			record, e := dnssdDecodeRecord(event)
			if e != nil {
				apiErr <- e
				return
			}
			sid, ok := recordSession(record)
			if !ok || sid != advert.SessionID {
				continue
			}
			if !dnssdRecordMatchesAdvertisement(record, advert) {
				apiErr <- ErrInvalidPacket
				return
			}
			mask |= 1 << uint(record.Kind-1)
			if mask == 15 && !reported {
				reported = true
				apiReady <- struct{}{}
			}
		}
		apiErr <- ErrReceiveBudget
	}()
	type received struct {
		packet Packet
		err    error
	}
	packets := make(chan received)
	readStarted = true
	go func() {
		defer close(readDone)
		for n := 0; n < MaxReceivedDatagrams; n++ {
			p, e := socket.ReadPacket()
			select {
			case packets <- received{p, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	window := time.Now()
	parsed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-apiErr:
			failure(e)
			return
		case <-apiReady:
			result.APIRecordsReady = true
			result.Reason = "reply_deadline"
			if e := binding(); e != nil {
				failure(e)
				return
			}
			if ctx.Err() != nil {
				return
			}
			// The exact bound source and sticky IP_BOUND_IF/multicast options
			// already select this path. This one comparison query has no
			// ancillary attempt, runtime fallback or resend.
			stage, kind := writeDNSServiceLegacyQuery(socket.conn, query, deadline, time.Now())
			if stage != "none" {
				result.Reason = stage + "_unavailable"
				result.FailureStage, result.FailureKind = stage, kind
				return
			}
			result.OneQuerySent = true
		case got := <-packets:
			if got.err != nil {
				var timeout net.Error
				if errors.As(got.err, &timeout) && timeout.Timeout() {
					return
				}
				result.Reason = "legacy_receive_unavailable"
				result.FailureStage, result.FailureKind = "legacy_receive", "unavailable"
				return
			}
			result.ReceiveCounters.received(got.packet)
			if result.ReceiveCounters.exhausted() {
				result.Status, result.Reason = "failed", "receive_budget"
				return
			}
			now := time.Now()
			if now.Sub(window) >= time.Second {
				window = now
				parsed = 0
			}
			if parsed >= MaxParsedPerSecond {
				result.ReceiveCounters.RateLimitedPackets++
				result.Status, result.Reason = "failed", "receive_budget"
				return
			}
			parsed++
			if e := binding(); e != nil {
				failure(e)
				return
			}
			path := legacyMetadataPath(scope, socket.local, loopScope.InterfaceIndex, got.packet)
			if !result.OneQuerySent || path == "none" || (result.ReceivePath != "none" && result.ReceivePath != path) {
				result.ReceiveCounters.MetadataRejected++
				continue
			}
			result.ReceiveCounters.MetadataAccepted++
			records, e := decodeLegacyReply(got.packet.Data, advert, id)
			if e != nil {
				result.ReceiveCounters.DecodeRejected++
				continue
			}
			result.ReceiveCounters.DecodedPackets++
			result.ReceivePath = path
			result.ReceiveCounters.match(records, advert)
			if result.ReceiveCounters.MatchingRecordKinds == 15 {
				result.Status, result.Reason = "verified", "native_legacy_serialization_observed"
				result.LegacySerializationVerified = true
				return
			}
		}
	}
}
