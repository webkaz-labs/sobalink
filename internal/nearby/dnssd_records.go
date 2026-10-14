package nearby

import (
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"
)

const (
	dnssdTypeA       uint16 = 1
	dnssdTypePTR     uint16 = 12
	dnssdTypeTXT     uint16 = 16
	dnssdTypeSRV     uint16 = 33
	dnssdMaxQueries         = 1 + 3*MaxCandidates
	dnssdMaxAPICalls        = 512
)

// These bounded values cross the native boundary; no native buffer is exposed
// to a consumer, and no source/destination/TTL packet metadata is synthesized.
type dnssdRecordData struct {
	Name string
	Type uint16
	Data []byte
}
type dnssdEvent struct {
	InterfaceIndex uint32
	Name           string
	Type, Class    uint16
	Data           []byte
	TTL            uint32
	Removed        bool
}
type dnssdRegistration struct {
	InterfaceIndex uint32
	Records        [4]dnssdRecordData
}

func dnssdRegistrationFor(cfg Config, policy discoveryPolicy) (dnssdRegistration, error) {
	if err := policy.validateScope(cfg.Scope); err != nil {
		return dnssdRegistration{}, err
	}
	if err := cfg.Advertisement.validate(policy); err != nil {
		return dnssdRegistration{}, err
	}
	if !cfg.Scope.LocalAddress.Is4() || cfg.Advertisement.Bootstrap.Addr() != cfg.Scope.LocalAddress || uint64(cfg.Scope.InterfaceIndex) > uint64(^uint32(0)>>1) {
		return dnssdRegistration{}, ErrInvalidScope
	}
	instance, host := wireNames(cfg.Advertisement)
	srv := make([]byte, 6)
	binary.BigEndian.PutUint16(srv[4:], cfg.Advertisement.Bootstrap.Port())
	srv = append(srv, dnssdEncodeName(host.String())...)
	var txt []byte
	for _, value := range []string{"v=1", "session=" + cfg.Advertisement.SessionID, "alias=" + cfg.Advertisement.Alias, "protocol=" + Protocol, "capability=" + Capability} {
		if len(value) > 255 {
			return dnssdRegistration{}, ErrInvalidAdvertisement
		}
		txt = append(txt, byte(len(value)))
		txt = append(txt, value...)
	}
	ip := cfg.Scope.LocalAddress.As4()
	return dnssdRegistration{uint32(cfg.Scope.InterfaceIndex), [4]dnssdRecordData{
		{serviceName, dnssdTypePTR, dnssdEncodeName(instance.String())},
		{instance.String(), dnssdTypeSRV, srv},
		{instance.String(), dnssdTypeTXT, txt},
		{host.String(), dnssdTypeA, append([]byte(nil), ip[:]...)},
	}}, nil
}
func dnssdEncodeName(name string) []byte {
	var out []byte
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

// DNSService RDATA contains an individual record, so compression pointers cannot
// refer to any packet. Only plain canonical labels in our fixed profile pass.
func dnssdDecodeName(data []byte) (string, error) {
	if len(data) == 0 || len(data) > 255 {
		return "", ErrInvalidPacket
	}
	var out strings.Builder
	for pos := 0; pos < len(data); {
		size := int(data[pos])
		pos++
		if size == 0 {
			if pos != len(data) || out.Len() == 0 {
				return "", ErrInvalidPacket
			}
			return out.String(), nil
		}
		if size > 63 || pos+size >= len(data) {
			return "", ErrInvalidPacket
		}
		for _, ch := range data[pos : pos+size] {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return "", ErrInvalidPacket
			}
		}
		out.WriteString(strings.ToLower(string(data[pos : pos+size])))
		out.WriteByte('.')
		pos += size
	}
	return "", ErrInvalidPacket
}
func dnssdSessionNames(instance string) (session, host string, ok bool) {
	suffix := "." + serviceName
	if !strings.HasSuffix(instance, suffix) {
		return "", "", false
	}
	label := strings.TrimSuffix(instance, suffix)
	if len(label) != 53 || label[0] != 's' {
		return "", "", false
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(label[1:]))
	if err != nil || len(raw) != 32 {
		return "", "", false
	}
	session = hex.EncodeToString(raw)
	if session == strings.Repeat("0", 64) {
		return "", "", false
	}
	expected, h := wireNames(Advertisement{SessionID: session})
	if instance != expected.String() {
		return "", "", false
	}
	return session, h.String(), true
}
func dnssdCanonicalName(name string) (string, bool) {
	if len(name) == 0 || len(name) > 255 || !strings.HasSuffix(name, ".") {
		return "", false
	}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return "", false
		}
		for _, ch := range []byte(label) {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return "", false
			}
		}
	}
	return strings.ToLower(name), true
}
func dnssdDecodeRecord(e dnssdEvent) (discoveryRecord, error) {
	if len(e.Name) == 0 || len(e.Name) > 255 || len(e.Data) > maxRecordBytes || e.Class != 1 {
		return discoveryRecord{}, ErrInvalidPacket
	}
	canonical, ok := dnssdCanonicalName(e.Name)
	if !ok {
		return discoveryRecord{}, ErrInvalidPacket
	}
	e.Name = canonical
	r := discoveryRecord{Name: e.Name, TTL: time.Duration(e.TTL) * time.Second, Removed: e.Removed || e.TTL == 0}
	switch e.Type {
	case dnssdTypePTR:
		r.Kind = ptrRecord
		target, err := dnssdDecodeName(e.Data)
		if err != nil || e.Name != serviceName {
			return discoveryRecord{}, ErrInvalidPacket
		}
		if _, _, ok := dnssdSessionNames(target); !ok {
			return discoveryRecord{}, ErrInvalidPacket
		}
		r.Target = target
	case dnssdTypeSRV:
		r.Kind = srvRecord
		_, host, ok := dnssdSessionNames(e.Name)
		if !ok || len(e.Data) < 7 {
			return discoveryRecord{}, ErrInvalidPacket
		}
		target, err := dnssdDecodeName(e.Data[6:])
		if err != nil || target != host {
			return discoveryRecord{}, ErrInvalidPacket
		}
		r.Priority = binary.BigEndian.Uint16(e.Data)
		r.Weight = binary.BigEndian.Uint16(e.Data[2:])
		r.Port = binary.BigEndian.Uint16(e.Data[4:])
		r.Target = target
		if r.Priority != 0 || r.Weight != 0 {
			return discoveryRecord{}, ErrInvalidPacket
		}
	case dnssdTypeTXT:
		r.Kind = txtRecord
		if _, _, ok := dnssdSessionNames(e.Name); !ok {
			return discoveryRecord{}, ErrInvalidPacket
		}
		for pos := 0; pos < len(e.Data); {
			size := int(e.Data[pos])
			pos++
			if size == 0 || pos+size > len(e.Data) || len(r.TXT) >= 5 {
				return discoveryRecord{}, ErrInvalidPacket
			}
			r.TXT = append(r.TXT, string(e.Data[pos:pos+size]))
			pos += size
		}
		if len(r.TXT) != 5 {
			return discoveryRecord{}, ErrInvalidPacket
		}
	case dnssdTypeA:
		r.Kind = aRecord
		if len(e.Data) != 4 {
			return discoveryRecord{}, ErrInvalidPacket
		}
		label := strings.TrimSuffix(e.Name, ".local.")
		if _, host, ok := dnssdSessionNames(label + "." + serviceName); !ok || host != e.Name {
			return discoveryRecord{}, ErrInvalidPacket
		}
		r.Address = netip.AddrFrom4([4]byte(e.Data))
	default:
		return discoveryRecord{}, ErrInvalidPacket
	}
	return r, nil
}
