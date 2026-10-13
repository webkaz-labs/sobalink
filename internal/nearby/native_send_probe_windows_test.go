//go:build windows

package nearby

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"time"
)

// This is a test-only discriminator, never a production send fallback. It uses
// the original observation deadline and exact production socket restrictions.
func runPlatformSendProbe(scope Scope, deadline time.Time) (result *nativeSendProbeResult) {
	result = &nativeSendProbeResult{Status: "unavailable", Reason: "native_receive_timeout", SocketFailureStage: "none", SendFailureKind: "none"}
	if !time.Now().Before(deadline) {
		return result
	}
	var opened []*observedNativeTransport
	defer func() {
		done := make(chan error, len(opened))
		for _, owner := range opened {
			go func(tr *observedNativeTransport) { done <- tr.Close() }(owner)
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for remaining := len(opened); remaining > 0; remaining-- {
			select {
			case err := <-done:
				if err != nil {
					result.Status, result.Reason = "failed", "cleanup_close_uncertain"
				}
			case <-timer.C:
				result.Status, result.Reason = "failed", "cleanup_not_joined"
				return
			}
		}
		joined := len(opened) == 2
		for _, owner := range opened {
			_, _, bad, closed := owner.snapshot()
			if bad {
				result.Status, result.Reason = "failed", "foreign_or_missing_packet_metadata"
			}
			joined = joined && closed
		}
		result.OwnersJoined = joined
		if result.Status == "verified" && !joined {
			result.Status, result.Reason = "failed", "owners_missing"
		}
	}()
	policy := isolatedLoopbackPolicy()
	for i := 0; i < 2; i++ {
		tr, err := nativeFactory(scope, policy)
		if tr != nil {
			opened = append(opened, &observedNativeTransport{Transport: tr, scope: scope})
		}
		if err != nil {
			result.Reason, result.SocketFailureStage = "native_socket_unavailable", socketFailureStage(err)
			if errors.Is(err, ErrCloseUncertain) || errors.Is(err, ErrCloseUnresolved) {
				result.Status, result.Reason = "failed", "startup_cleanup_uncertain"
			}
			return result
		}
		if tr == nil {
			result.Status, result.Reason = "failed", "native_owner_missing"
			return result
		}
	}
	sender, senderOK := opened[0].Transport.(*nativeTransport)
	receiver, receiverOK := opened[1].Transport.(*nativeTransport)
	if !senderOK || !receiverOK {
		result.Status, result.Reason = "failed", "native_owner_missing"
		return result
	}

	sid, err := NewSessionID()
	if err != nil {
		result.Status, result.Reason = "failed", "session_entropy_unavailable"
		return result
	}
	ad := Advertisement{SessionID: sid, Alias: "Probe", Protocol: Protocol, Capability: Capability, Bootstrap: netip.AddrPortFrom(scope.LocalAddress, 42002)}
	data, err := packAdvertisementWithPolicy(ad, policy)
	if err != nil {
		result.Status, result.Reason = "failed", "native_session_start_failed"
		return result
	}
	writeDeadline := time.Now().Add(time.Second)
	if deadline.Before(writeDeadline) {
		writeDeadline = deadline
	}
	if sender.conn.SetWriteDeadline(writeDeadline) != nil {
		result.Reason, result.SocketFailureStage = "native_socket_unavailable", "socket_write_deadline"
		return result
	}
	if receiver.conn.SetReadDeadline(deadline) != nil {
		result.Reason, result.SocketFailureStage = "native_socket_unavailable", "socket_read"
		return result
	}
	// The sender remains bound to the exact selected local source on port 5353. Its
	// IP_MULTICAST_IF, TTL and loopback options remain the factory's exact values.
	if _, err := currentBindingWithPolicy(scope, &sender.ifi, policy); err != nil {
		result.Reason = "loopback_scope_unavailable"
		return result
	}
	n, err := sender.conn.WriteToUDP(data, &net.UDPAddr{IP: net.IP(multicast4.AsSlice()), Port: mdnsPort})
	if err = classifyNativeSend(n, len(data), 0, 0, err); err != nil {
		result.Reason, result.SocketFailureStage, result.SendFailureKind = "native_receive_unavailable", socketFailureStage(err), nativeSendFailureKind(err)
		return result
	}
	for read := 0; read < MaxReceivedDatagrams && time.Now().Before(deadline); read++ {
		packet, err := opened[1].ReadPacket()
		if err != nil {
			result.SocketFailureStage = socketFailureStage(err)
			if errors.Is(err, errCapabilityScope) {
				result.Status, result.Reason = "failed", "foreign_or_missing_packet_metadata"
			} else if time.Now().Before(deadline) {
				result.Reason = "native_receive_unavailable"
			}
			return result
		}
		if bytes.Equal(packet.Data, data) {
			result.Status, result.Reason, result.MetadataVerified = "verified", "scoped_no_ancillary_delivery", true
			return result
		}
	}
	return result
}
