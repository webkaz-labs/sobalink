package nearby

import (
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const serviceName = "_sobalink-setup._tcp.local."
const mdnsPort = 5353

var multicast4 = netip.MustParseAddr("224.0.0.251")
var multicast6 = netip.MustParseAddr("ff02::fb")

func wireNames(a Advertisement) (dnsmessage.Name, dnsmessage.Name) {
	raw, _ := hex.DecodeString(a.SessionID)
	// The label is derived only from an ephemeral random session, never the OS
	// hostname or alias. Full 256 bits fit in one DNS label using base32.
	label := "s" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	instance, _ := dnsmessage.NewName(label + "." + serviceName)
	host, _ := dnsmessage.NewName(label + ".local.")
	return instance, host
}
func packAdvertisement(a Advertisement) ([]byte, error) {
	return packAdvertisementWithPolicy(a, discoveryPolicy{})
}

func packAdvertisementWithPolicy(a Advertisement, policy discoveryPolicy) ([]byte, error) {
	if err := a.validate(policy); err != nil {
		return nil, err
	}
	instance, host := wireNames(a)
	service, _ := dnsmessage.NewName(serviceName)
	header := func(n dnsmessage.Name, typ dnsmessage.Type, unique bool) dnsmessage.ResourceHeader {
		class := dnsmessage.ClassINET
		if unique {
			class |= 0x8000
		}
		return dnsmessage.ResourceHeader{Name: n, Type: typ, Class: class, TTL: uint32(CandidateTTL / time.Second)}
	}
	resources := []dnsmessage.Resource{
		{Header: header(service, dnsmessage.TypePTR, false), Body: &dnsmessage.PTRResource{PTR: instance}},
		{Header: header(instance, dnsmessage.TypeSRV, true), Body: &dnsmessage.SRVResource{Port: a.Bootstrap.Port(), Target: host}},
		{Header: header(instance, dnsmessage.TypeTXT, true), Body: &dnsmessage.TXTResource{TXT: []string{"v=1", "session=" + a.SessionID, "alias=" + a.Alias, "protocol=" + a.Protocol, "capability=" + a.Capability}}},
	}
	if a.Bootstrap.Addr().Is4() {
		resources = append(resources, dnsmessage.Resource{Header: header(host, dnsmessage.TypeA, true), Body: &dnsmessage.AResource{A: a.Bootstrap.Addr().As4()}})
	} else {
		resources = append(resources, dnsmessage.Resource{Header: header(host, dnsmessage.TypeAAAA, true), Body: &dnsmessage.AAAAResource{AAAA: a.Bootstrap.Addr().As16()}})
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{Response: true, Authoritative: true}, Answers: resources}
	b, err := m.Pack()
	if err != nil || len(b) > MaxDatagramBytes {
		return nil, ErrInvalidAdvertisement
	}
	return b, nil
}
func packQuery() []byte {
	service, _ := dnsmessage.NewName(serviceName)
	m := dnsmessage.Message{Questions: []dnsmessage.Question{{Name: service, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}}
	b, _ := m.Pack()
	return b
}

func validatePacket(s Scope, p Packet) bool { return validatePacketWithPolicy(s, p, discoveryPolicy{}) }

func validatePacketWithPolicy(s Scope, p Packet, policy discoveryPolicy) bool {
	group := multicast6
	if s.LocalAddress.Is4() {
		group = multicast4
	}
	return len(p.Data) >= 12 && len(p.Data) <= MaxDatagramBytes && p.InterfaceIndex == s.InterfaceIndex && p.HopLimit == 255 && p.Destination == group && p.Source.Port() == mdnsPort && policy.validAddress(p.Source.Addr()) && p.Source.Addr().BitLen() == s.LocalAddress.BitLen() && s.Prefix.Contains(p.Source.Addr())
}

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
