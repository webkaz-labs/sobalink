package nearby

import (
	"encoding/binary"
	"golang.org/x/net/dns/dnsmessage"
	"net/netip"
	"strings"
	"time"
)

// This test-only decoder verifies that the emitted raw profile remains backward
// compatible. Production admission uses decodeRecordPacket and recordAssembler.
// unpackPacket accepts only the one bounded DNS-SD profile emitted here. It
// does not resolve DNS names or follow records into another packet/network.
func unpackPacket(p Packet) (Advertisement, time.Duration, bool, error) {
	return unpackPacketWithPolicy(p, discoveryPolicy{})
}

func unpackPacketWithPolicy(p Packet, policy discoveryPolicy) (Advertisement, time.Duration, bool, error) {
	data := p.Data
	if len(data) < 12 || len(data) > MaxDatagramBytes {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	response := binary.BigEndian.Uint16(data[2:4])&0x8000 != 0
	// Check counts before the DNS library can allocate lists from hostile counts.
	q, a, ns, ar := binary.BigEndian.Uint16(data[4:6]), binary.BigEndian.Uint16(data[6:8]), binary.BigEndian.Uint16(data[8:10]), binary.BigEndian.Uint16(data[10:12])
	if ns != 0 || ar != 0 || (response && (q != 0 || a != 4)) || (!response && (q != 1 || a != 0)) {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	var m dnsmessage.Message
	if err := m.Unpack(data); err != nil {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	h := m.Header
	if h.ID != 0 || h.OpCode != 0 || h.Truncated || h.RecursionDesired || h.RecursionAvailable || h.AuthenticData || h.CheckingDisabled || h.RCode != 0 {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	if !response {
		question := m.Questions[0]
		if h.Authoritative || question.Name.String() != serviceName || question.Type != dnsmessage.TypePTR || question.Class != dnsmessage.ClassINET {
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
		return Advertisement{}, 0, true, nil
	}
	if !h.Authoritative {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	var out Advertisement
	var ptr *dnsmessage.PTRResource
	var srv *dnsmessage.SRVResource
	var txt *dnsmessage.TXTResource
	var addr netip.Addr
	var ptrName, srvName, txtName, addrName dnsmessage.Name
	ttl := m.Answers[0].Header.TTL
	if ttl > uint32(CandidateTTL/time.Second) {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	for _, r := range m.Answers {
		if r.Header.TTL != ttl || r.Header.Class&0x7fff != dnsmessage.ClassINET {
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
		switch body := r.Body.(type) {
		case *dnsmessage.PTRResource:
			if ptr != nil || r.Header.Class != dnsmessage.ClassINET {
				return Advertisement{}, 0, false, ErrInvalidPacket
			}
			ptr, ptrName = body, r.Header.Name
		case *dnsmessage.SRVResource:
			if srv != nil {
				return Advertisement{}, 0, false, ErrInvalidPacket
			}
			srv, srvName = body, r.Header.Name
		case *dnsmessage.TXTResource:
			if txt != nil {
				return Advertisement{}, 0, false, ErrInvalidPacket
			}
			txt, txtName = body, r.Header.Name
		case *dnsmessage.AResource:
			if addr.IsValid() {
				return Advertisement{}, 0, false, ErrInvalidPacket
			}
			addr, addrName = netip.AddrFrom4(body.A), r.Header.Name
		case *dnsmessage.AAAAResource:
			if addr.IsValid() {
				return Advertisement{}, 0, false, ErrInvalidPacket
			}
			addr, addrName = netip.AddrFrom16(body.AAAA), r.Header.Name
		default:
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
	}
	if ptr == nil || srv == nil || txt == nil || !addr.IsValid() || len(txt.TXT) != 5 || srv.Priority != 0 || srv.Weight != 0 {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	fields := make(map[string]string, 5)
	for _, field := range txt.TXT {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
		if _, duplicate := fields[key]; duplicate {
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
		switch key {
		case "v", "session", "alias", "protocol", "capability":
		default:
			return Advertisement{}, 0, false, ErrInvalidPacket
		}
		fields[key] = value
	}
	if fields["v"] != "1" {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	out = Advertisement{SessionID: fields["session"], Alias: fields["alias"], Protocol: fields["protocol"], Capability: fields["capability"], Bootstrap: netip.AddrPortFrom(addr, srv.Port)}
	if out.validate(policy) != nil || addr != p.Source.Addr() {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	instance, host := wireNames(out)
	if ptrName.String() != serviceName || ptr.PTR != instance || srvName != instance || txtName != instance || srv.Target != host || addrName != host {
		return Advertisement{}, 0, false, ErrInvalidPacket
	}
	return out, time.Duration(ttl) * time.Second, false, nil
}
