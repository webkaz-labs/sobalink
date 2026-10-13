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

// dnsWireName bounds decompression work and result size before appending. Only
// backward compression references are accepted; the step bound also stops
// cyclic label-plus-pointer paths. Noncanonical label bytes never match a name.
func dnsWireName(data []byte, offset int) (string, int, error) {
	if offset < 0 || offset >= len(data) {
		return "", 0, ErrInvalidPacket
	}
	var name [255]byte
	used, next, hops := 0, -1, 0
	canonical := true
	for {
		if offset >= len(data) || hops >= 128 {
			return "", 0, ErrInvalidPacket
		}
		hops++
		n := int(data[offset])
		offset++
		if n == 0 {
			if next < 0 {
				next = offset
			}
			if used == 0 {
				name[0] = '.'
				used = 1
			}
			if !canonical {
				return "", next, nil
			}
			return strings.ToLower(string(name[:used])), next, nil
		}
		if n&0xc0 == 0xc0 {
			if offset >= len(data) {
				return "", 0, ErrInvalidPacket
			}
			target := (n&0x3f)<<8 | int(data[offset])
			offset++
			if target >= offset-2 {
				return "", 0, ErrInvalidPacket
			}
			if next < 0 {
				next = offset
			}
			offset = target
			continue
		}
		if n > 63 || offset+n > len(data) || used+n+1 > len(name) {
			return "", 0, ErrInvalidPacket
		}
		for _, b := range data[offset : offset+n] {
			if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_') {
				canonical = false
			}
		}
		copy(name[used:], data[offset:offset+n])
		used += n
		name[used] = '.'
		used++
		offset += n
	}
}

func exactLocalQuestion(name string, kind uint16, local Advertisement) bool {
	instance, host := wireNames(local)
	return (name == serviceName && kind == 12) || (name == instance.String() && (kind == 33 || kind == 16)) || (name == host.String() && kind == 1)
}

// decodeRecordPacket handles bounded Answer and Additional records, including
// unrelated Bonjour records, without using Message.Unpack's attacker-sized
// section/TXT allocations. All section counts are checked before decoding.
func decodeRecordPacket(data []byte, local Advertisement) ([]discoveryRecord, bool, error) {
	if len(data) < 12 || len(data) > MaxDatagramBytes {
		return nil, false, ErrInvalidPacket
	}
	flags := binary.BigEndian.Uint16(data[2:4])
	response := flags&0x8000 != 0
	q := int(binary.BigEndian.Uint16(data[4:6]))
	an := int(binary.BigEndian.Uint16(data[6:8]))
	ns := int(binary.BigEndian.Uint16(data[8:10]))
	ar := int(binary.BigEndian.Uint16(data[10:12]))
	if binary.BigEndian.Uint16(data[:2]) != 0 || flags&^uint16(0x8400) != 0 || q > maxDNSQuestions || an+ns+ar > maxObservationRecords || (!response && (q == 0 || flags != 0)) {
		return nil, false, ErrInvalidPacket
	}
	offset := 12
	query := false
	for i := 0; i < q; i++ {
		name, next, err := dnsWireName(data, offset)
		if err != nil || next+4 > len(data) {
			return nil, false, ErrInvalidPacket
		}
		typ, class := binary.BigEndian.Uint16(data[next:next+2]), binary.BigEndian.Uint16(data[next+2:next+4])
		offset = next + 4
		if !response && class&0x7fff == 1 && exactLocalQuestion(name, typ, local) {
			query = true
		}
	}
	var records []discoveryRecord
	for i := 0; i < an+ns+ar; i++ {
		name, next, err := dnsWireName(data, offset)
		if err != nil || next+10 > len(data) {
			return nil, false, ErrInvalidPacket
		}
		typ, class := binary.BigEndian.Uint16(data[next:next+2]), binary.BigEndian.Uint16(data[next+2:next+4])
		ttl := binary.BigEndian.Uint32(data[next+4 : next+8])
		length := int(binary.BigEndian.Uint16(data[next+8 : next+10]))
		body := next + 10
		end := body + length
		if end > len(data) || length > maxRecordBytes {
			return nil, false, ErrInvalidPacket
		}
		offset = end
		// Authority/known-answer sections are structurally checked but never become
		// observations. Unknown types/classes/names cannot induce follow-up queries.
		if !response || (i >= an && i < an+ns) || class&0x7fff != 1 {
			continue
		}
		r := discoveryRecord{Name: name, TTL: time.Duration(ttl) * time.Second}
		switch typ {
		case 12:
			if name != serviceName {
				continue
			}
			target, n, err := dnsWireName(data, body)
			if err != nil || n != end {
				return nil, false, ErrInvalidPacket
			}
			r.Kind = ptrRecord
			r.Target = target
			if _, ok := recordSession(r); !ok {
				continue
			}
		case 33:
			if _, ok := sessionFromName(name, "."+serviceName); !ok {
				continue
			}
			if length < 7 {
				return nil, false, ErrInvalidPacket
			}
			r.Kind = srvRecord
			r.Priority = binary.BigEndian.Uint16(data[body : body+2])
			r.Weight = binary.BigEndian.Uint16(data[body+2 : body+4])
			r.Port = binary.BigEndian.Uint16(data[body+4 : body+6])
			target, n, err := dnsWireName(data, body+6)
			if err != nil || n != end {
				return nil, false, ErrInvalidPacket
			}
			r.Target = target
		case 16:
			if _, ok := sessionFromName(name, "."+serviceName); !ok {
				continue
			}
			// Count all strings before any TXT slice/string allocation.
			n, count := body, 0
			for n < end {
				size := int(data[n])
				n++
				if n+size > end {
					return nil, false, ErrInvalidPacket
				}
				n += size
				count++
				if count > 5 {
					return nil, false, ErrInvalidPacket
				}
			}
			if count != 5 {
				return nil, false, ErrInvalidPacket
			}
			r.Kind = txtRecord
			r.TXT = make([]string, 0, 5)
			for n = body; n < end; {
				size := int(data[n])
				n++
				r.TXT = append(r.TXT, string(data[n:n+size]))
				n += size
			}
		case 1:
			if _, ok := sessionFromName(name, ".local."); !ok {
				continue
			}
			if length != 4 {
				return nil, false, ErrInvalidPacket
			}
			r.Kind = aRecord
			r.Address = netip.AddrFrom4([4]byte(data[body:end]))
		default:
			continue
		}
		records = append(records, r)
	}
	if offset != len(data) {
		return nil, false, ErrInvalidPacket
	}
	return records, !response && query, nil
}

func packRecordQuery(sid string) []byte {
	instance, host := wireNames(Advertisement{SessionID: sid})
	m := dnsmessage.Message{Questions: []dnsmessage.Question{
		{Name: instance, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET},
		{Name: instance, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET},
		{Name: host, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
	}}
	b, _ := m.Pack()
	return b
}
