//go:build darwin && cgo

package nearby

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

// This is a receive-only test owner. It cannot announce/query or exercise the
// known-unavailable raw Darwin local sender. The production bind is unchanged.
type dnssdReceiveOnly struct{ *nativeTransport }

func (*dnssdReceiveOnly) WritePacket([]byte) error { return ErrUnsupported }
func newDNSServiceReceiveOnly(scope Scope) (Transport, error) {
	ifi, err := currentBinding(scope, nil)
	if err != nil {
		return nil, err
	}
	raw, retained, err := openNativeGroupSocket(scope.InterfaceIndex, systemNativeSocketOps())
	if err != nil {
		return retained, err
	}
	conn, ok := raw.(*net.UDPConn)
	if !ok {
		return rollbackTransport(&closeOnlyTransport{close: raw.Close}, ErrUnsupported)
	}
	packet := ipv4.NewPacketConn(conn)
	owner := &dnssdReceiveOnly{&nativeTransport{conn: conn, packet: packet, scope: scope, ifi: ifi, closeOwner: &closeOnlyTransport{close: packet.Close}}}
	if conn.LocalAddr().String() != "224.0.0.251:5353" {
		return rollbackTransport(owner, ErrInvalidScope)
	}
	for _, step := range []func() error{
		func() error { return conn.SetReadBuffer(16 * MaxDatagramBytes) },
		func() error { return packet.SetControlMessage(ipv4.FlagTTL|ipv4.FlagDst|ipv4.FlagInterface, true) },
		func() error { return packet.JoinGroup(&ifi, &net.UDPAddr{IP: net.IP(multicast4.AsSlice())}) },
	} {
		if step() != nil {
			return rollbackTransport(owner, ErrSocketUnavailable)
		}
	}
	return owner, nil
}
func TestDNSServiceRawReceiveCapability(t *testing.T) {
	result := dnssdCapability("selected_private_interface_raw_receive")
	if os.Getenv("SOBALINK_DNSSD_RAW_RECEIVE") != "1" {
		result.Status, result.Reason = "not_run", "opt_in_required"
	} else {
		result = runDNSServiceRawReceiveCapability()
	}
	emitDNSServiceCapability(t, result)
}
func runDNSServiceRawReceiveCapability() (result dnssdCapabilityResult) {
	result = dnssdCapability("selected_private_interface_raw_receive")
	scope, err := dnssdSinglePrivateScope()
	if err != nil {
		result.Reason = "private_scope_unavailable"
		return
	}
	cfgs, err := dnssdSyntheticConfigs(scope)
	if err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	deadline, _ := ctx.Deadline()
	apiErr := make(chan error, 2)
	watchDone := make(chan struct{})
	watchStarted := false
	var receiver Transport
	var api *dnssdCapabilityPeer
	readDone := make(chan struct{})
	readStarted := false
	defer func() {
		cancel()
		closed := make(chan error, 2)
		count := 0
		if receiver != nil {
			count++
			go func() { closed <- receiver.Close() }()
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
		for i := 0; i < count; i++ {
			select {
			case e := <-closed:
				if e != nil {
					result.Status, result.Reason = "failed", "cleanup_uncertain"
					return
				}
			case <-timer.C:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
		}
		if api != nil {
			select {
			case <-api.done:
			case <-timer.C:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
		}
		if watchStarted {
			select {
			case <-watchDone:
			case <-timer.C:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
		}
		if readStarted {
			select {
			case <-readDone:
			case <-timer.C:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
		}
		for {
			select {
			case e := <-apiErr:
				dnssdCapabilityError(&result, e)
			default:
				goto drained
			}
		}
	drained:
		result.OwnersJoined = receiver != nil && api != nil
		if result.Status == "verified" && !result.OwnersJoined {
			result.Status, result.Reason = "failed", "owners_missing"
		}
	}()
	receiver, err = newDNSServiceReceiveOnly(scope)
	if err != nil {
		result.Reason = "raw_receiver_unavailable"
		return
	}
	reg, err := dnssdRegistrationFor(cfgs[0], discoveryPolicy{})
	if err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	backend, err := newNativeDNSServiceBackend(reg)
	if backend != nil {
		api = &dnssdCapabilityPeer{backend: backend, done: make(chan struct{}), closed: make(chan struct{})}
	}
	if err != nil {
		dnssdCapabilityError(&result, err)
		return
	}
	type received struct {
		packet Packet
		err    error
	}
	packets := make(chan received)
	readStarted = true
	go func() {
		defer close(readDone)
		for n := 0; n < MaxReceivedDatagrams; n++ {
			p, e := receiver.ReadPacket()
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
	original, e := currentBinding(scope, nil)
	if e != nil {
		dnssdCapabilityError(&result, e)
		return
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
				if _, e := currentBinding(scope, &original); e != nil {
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
		if _, e := currentBinding(scope, &original); e != nil {
			apiErr <- e
			return
		}
		if e := backend.Start(); e != nil {
			apiErr <- e
			return
		}
		for {
			_, e := backend.Next()
			if e != nil {
				if !errors.Is(e, context.Canceled) {
					apiErr <- e
				}
				return
			}
		}
	}()
	assembler := newRecordAssembler(scope, discoveryPolicy{}, deadline, cfgs[1].Advertisement.SessionID)
	store := newStore(scope)
	store.generation = cfgs[1].Advertisement.SessionID
	window := time.Now()
	parsed, receivedCount := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-apiErr:
			dnssdCapabilityError(&result, e)
			return
		case got := <-packets:
			if got.err != nil {
				result.Reason = "raw_receive_unavailable"
				return
			}
			receivedCount++
			if receivedCount >= MaxReceivedDatagrams {
				result.Status, result.Reason = "failed", "provider_invariant"
				return
			}
			now := time.Now()
			if now.Sub(window) >= time.Second {
				window = now
				parsed = 0
			}
			if parsed >= MaxParsedPerSecond {
				continue
			}
			parsed++
			if !validatePacket(scope, got.packet) {
				continue
			}
			records, _, e := decodeRecordPacket(got.packet.Data, cfgs[1].Advertisement)
			if e != nil {
				continue
			}
			observation := recordObservation{Kind: rawPacketObservation, InterfaceIndex: got.packet.InterfaceIndex, Source: got.packet.Source, Records: records}
			if e = assembler.observe(observation, now, &store); e != nil {
				result.Status, result.Reason = "failed", "provider_invariant"
				return
			}
			for _, candidate := range store.list(now) {
				if candidate.Advertisement == cfgs[0].Advertisement && candidate.Scope == scope && candidate.Provenance == RawPacketProvenance && candidate.Source.IsValid() {
					result.RawPacketVerified = true
					result.Status, result.Reason = "verified", "native_raw_receive_observed"
					return
				}
			}
		}
	}
}
