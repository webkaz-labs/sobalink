package nearby

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

type dnssdLegacyResult struct {
	Status                      string                `json:"status"`
	Reason                      string                `json:"reason"`
	Scope                       string                `json:"scope"`
	APIRecordsReady             bool                  `json:"apiRecordsReady"`
	OneQuerySent                bool                  `json:"oneQuerySent"`
	LegacySerializationVerified bool                  `json:"legacySerializationVerified"`
	OwnersJoined                bool                  `json:"ownersJoined"`
	ReceivePath                 string                `json:"receivePath"`
	FailureStage                string                `json:"failureStage"`
	FailureKind                 string                `json:"failureKind"`
	ReceiveCounters             *dnssdReceiveCounters `json:"receiveCounters"`
	RawPacketVerified           bool                  `json:"rawPacketVerified"`
	NetworkMulticastVerified    bool                  `json:"networkMulticastVerified"`
}

func dnssdLegacyDefault() dnssdLegacyResult {
	return dnssdLegacyResult{Status: "not_run", Reason: "opt_in_required", Scope: "selected_private_interface_legacy_dns", ReceivePath: "none", FailureStage: "none", FailureKind: "none"}
}

// Legacy evidence is independent of production multicast admission. Late native
// failures must downgrade a provisional result after all owner workers join.
func dnssdLegacyError(result *dnssdLegacyResult, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	result.LegacySerializationVerified = false
	var native dnssdFailure
	if errors.As(err, &native) {
		result.Status, result.Reason = "unavailable", "native_api_unavailable"
		result.FailureStage, result.FailureKind = native.stage, native.kind
		return
	}
	if errors.Is(err, ErrScopeChanged) {
		result.Status, result.Reason = "unavailable", "selected_binding_changed"
		return
	}
	result.Status, result.Reason = "failed", "provider_invariant"
}

// This socket budget counts bytes copied from all datagrams, including rejected
// or truncated input. It is separate from the OS provider's admitted-record cap.
const dnssdMaxCopiedReceiveBytes = MaxReceivedDatagrams * (MaxDatagramBytes + 1)

type dnssdReceiveCounters struct {
	ReceivedPackets     int   `json:"receivedPackets"`
	ReceivedBytes       int   `json:"receivedBytes"` // Bytes actually copied, bounded to 1201 per read.
	MetadataAccepted    int   `json:"metadataAccepted"`
	MetadataRejected    int   `json:"metadataRejected"`
	DecodedPackets      int   `json:"decodedPackets"`
	DecodeRejected      int   `json:"decodeRejected"`
	RateLimitedPackets  int   `json:"rateLimitedPackets"`
	MatchingRecordKinds uint8 `json:"matchingRecordKinds"`
}

func (c *dnssdReceiveCounters) received(p Packet) {
	c.ReceivedPackets++
	c.ReceivedBytes += len(p.Data)
}
func (c *dnssdReceiveCounters) exhausted() bool {
	return c.ReceivedPackets >= MaxReceivedDatagrams || c.ReceivedBytes >= dnssdMaxCopiedReceiveBytes
}
func dnssdRecordMatchesAdvertisement(r discoveryRecord, ad Advertisement) bool {
	if r.Removed || r.TTL <= 0 {
		return false
	}
	instance, host := wireNames(ad)
	switch r.Kind {
	case ptrRecord:
		return r.Name == serviceName && r.Target == instance.String()
	case srvRecord:
		return r.Name == instance.String() && r.Target == host.String() && r.Priority == 0 && r.Weight == 0 && r.Port == ad.Bootstrap.Port()
	case txtRecord:
		if r.Name != instance.String() {
			return false
		}
		got, err := txtAdvertisement(r.TXT)
		got.Bootstrap = ad.Bootstrap
		return err == nil && got == ad
	case aRecord:
		return r.Name == host.String() && r.Address == ad.Bootstrap.Addr()
	}
	return false
}
func (c *dnssdReceiveCounters) match(records []discoveryRecord, ad Advertisement) {
	for _, r := range records {
		if dnssdRecordMatchesAdvertisement(r, ad) {
			c.MatchingRecordKinds |= 1 << uint(r.Kind-1)
		}
	}
}
func legacyQuestions(ad Advertisement) []dnsmessage.Question {
	instance, host := wireNames(ad)
	service, _ := dnsmessage.NewName(serviceName)
	return []dnsmessage.Question{{Name: service, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}, {Name: instance, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET}, {Name: instance, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}, {Name: host, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
}
func packLegacyQuery(ad Advertisement, id uint16) ([]byte, error) {
	if id == 0 {
		return nil, ErrInvalidPacket
	}
	return (&dnsmessage.Message{Header: dnsmessage.Header{ID: id}, Questions: legacyQuestions(ad)}).Pack()
}

// This validates the distinct RFC6762 legacy envelope, then exercises the same
// bounded RR decoder with only the already-checked transaction ID normalized.
// It never constructs a Packet or feeds these unicast observations to Session.
func decodeLegacyReply(data []byte, ad Advertisement, id uint16) ([]discoveryRecord, error) {
	if id == 0 || len(data) < 12 || len(data) > 512 || binary.BigEndian.Uint16(data[:2]) != id {
		return nil, ErrInvalidPacket
	}
	flags := binary.BigEndian.Uint16(data[2:4])
	q := int(binary.BigEndian.Uint16(data[4:6]))
	an := int(binary.BigEndian.Uint16(data[6:8]))
	ns := int(binary.BigEndian.Uint16(data[8:10]))
	ar := int(binary.BigEndian.Uint16(data[10:12]))
	if flags != 0x8400 || q != 4 || an < 1 || ns != 0 || an+ar > maxObservationRecords {
		return nil, ErrInvalidPacket
	}
	expected := legacyQuestions(ad)
	seen := uint8(0)
	offset := 12
	for i := 0; i < q; i++ {
		name, next, err := dnsWireName(data, offset)
		if err != nil || next+4 > len(data) {
			return nil, ErrInvalidPacket
		}
		typ, class := binary.BigEndian.Uint16(data[next:]), binary.BigEndian.Uint16(data[next+2:])
		offset = next + 4
		found := false
		for j, want := range expected {
			if name == want.Name.String() && typ == uint16(want.Type) && class == uint16(want.Class) && seen&(1<<j) == 0 {
				seen |= 1 << j
				found = true
				break
			}
		}
		if !found {
			return nil, ErrInvalidPacket
		}
	}
	for i := 0; i < an+ar; i++ {
		_, next, err := dnsWireName(data, offset)
		if err != nil || next+10 > len(data) {
			return nil, ErrInvalidPacket
		}
		class := binary.BigEndian.Uint16(data[next+2:])
		ttl := binary.BigEndian.Uint32(data[next+4:])
		length := int(binary.BigEndian.Uint16(data[next+8:]))
		offset = next + 10 + length
		if class&0x8000 != 0 || ttl == 0 || ttl > 10 || length > maxRecordBytes || offset > len(data) {
			return nil, ErrInvalidPacket
		}
	}
	if offset != len(data) || seen != 15 {
		return nil, ErrInvalidPacket
	}
	normalized := append([]byte(nil), data...)
	normalized[0], normalized[1] = 0, 0
	records, query, err := decodeRecordPacket(normalized, ad)
	if err != nil || query {
		return nil, ErrInvalidPacket
	}
	return records, nil
}
func legacyMetadataPath(scope Scope, local netip.AddrPort, loopbackIndex int, p Packet) string {
	if local.Addr() != scope.LocalAddress || local.Port() < 1024 || local.Port() == mdnsPort || p.Source != netip.AddrPortFrom(scope.LocalAddress, mdnsPort) || p.Destination != scope.LocalAddress || p.HopLimit != 255 || len(p.Data) < 12 || len(p.Data) > MaxDatagramBytes {
		return "none"
	}
	if p.InterfaceIndex == scope.InterfaceIndex {
		return "selected_interface"
	}
	if loopbackIndex > 0 && p.InterfaceIndex == loopbackIndex {
		return "local_loopback"
	}
	return "none"
}
func TestDNSServiceLegacyEnvelopeAndMetadataBounds(t *testing.T) {
	ad := dnssdTestConfig().Advertisement
	query, err := packLegacyQuery(ad, 1234)
	if err != nil || len(query) > 512 {
		t.Fatal(err)
	}
	data, _ := packAdvertisement(ad)
	var message dnsmessage.Message
	if err := message.Unpack(data); err != nil {
		t.Fatal(err)
	}
	message.ID = 1234
	message.Questions = legacyQuestions(ad)
	for i := range message.Answers {
		message.Answers[i].Header.Class = dnsmessage.ClassINET
		message.Answers[i].Header.TTL = 10
	}
	valid, err := message.Pack()
	if err != nil || len(valid) > 512 {
		t.Fatalf("bounded compressed response: bytes=%d error=%v", len(valid), err)
	}
	records, err := decodeLegacyReply(valid, ad, 1234)
	if err != nil || len(records) != 4 {
		t.Fatalf("valid legacy response: %v", err)
	}
	counters := dnssdReceiveCounters{}
	counters.match(records, ad)
	if counters.MatchingRecordKinds != 15 {
		t.Fatal("exact record mask missing")
	}
	for name, modify := range map[string]func(*dnsmessage.Message){
		"zero id":              func(m *dnsmessage.Message) { m.ID = 0 },
		"wrong id":             func(m *dnsmessage.Message) { m.ID++ },
		"truncated":            func(m *dnsmessage.Message) { m.Truncated = true },
		"missing question":     func(m *dnsmessage.Message) { m.Questions = m.Questions[:3] },
		"wrong question name":  func(m *dnsmessage.Message) { m.Questions[0].Name = m.Questions[1].Name },
		"wrong question type":  func(m *dnsmessage.Message) { m.Questions[0].Type = dnsmessage.TypeAAAA },
		"wrong question class": func(m *dnsmessage.Message) { m.Questions[0].Class = dnsmessage.ClassANY },
		"duplicate question":   func(m *dnsmessage.Message) { m.Questions[1] = m.Questions[0] },
		"cache flush":          func(m *dnsmessage.Message) { m.Answers[0].Header.Class |= 0x8000 },
		"zero ttl":             func(m *dnsmessage.Message) { m.Answers[0].Header.TTL = 0 },
		"oversized ttl":        func(m *dnsmessage.Message) { m.Answers[0].Header.TTL = 11 },
	} {
		t.Run(name, func(t *testing.T) {
			var bad dnsmessage.Message
			if err := bad.Unpack(valid); err != nil {
				t.Fatal(err)
			}
			modify(&bad)
			packet, err := bad.Pack()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeLegacyReply(packet, ad, 1234); !errors.Is(err, ErrInvalidPacket) {
				t.Fatal("legacy mismatch accepted")
			}
		})
	}

	scope := dnssdTestConfig().Scope
	local := netip.AddrPortFrom(scope.LocalAddress, 55000)
	p := Packet{Data: valid, Source: netip.AddrPortFrom(scope.LocalAddress, mdnsPort), Destination: scope.LocalAddress, InterfaceIndex: scope.InterfaceIndex, HopLimit: 255}
	if legacyMetadataPath(scope, local, 9, p) != "selected_interface" {
		t.Fatal("selected path missing")
	}
	p.InterfaceIndex = 9
	if legacyMetadataPath(scope, local, 9, p) != "local_loopback" {
		t.Fatal("verified loopback path missing")
	}
	for name, change := range map[string]func(*Packet){
		"different interface":  func(p *Packet) { p.InterfaceIndex = 8 },
		"wrong source address": func(p *Packet) { p.Source = netip.MustParseAddrPort("192.0.2.200:5353") },
		"wrong source port":    func(p *Packet) { p.Source = netip.AddrPortFrom(scope.LocalAddress, 5354) },
		"wrong destination":    func(p *Packet) { p.Destination = netip.MustParseAddr("192.0.2.200") },
		"wrong ttl":            func(p *Packet) { p.HopLimit = 64 },
		"short packet":         func(p *Packet) { p.Data = valid[:11] },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			bad.InterfaceIndex = scope.InterfaceIndex
			change(&bad)
			if legacyMetadataPath(scope, local, 9, bad) != "none" {
				t.Fatal("incorrect metadata accepted")
			}
		})
	}
	if legacyMetadataPath(scope, local, 0, p) != "none" {
		t.Fatal("unverified loopback index accepted")
	}
	p.InterfaceIndex = scope.InterfaceIndex
	for _, badLocal := range []netip.AddrPort{netip.AddrPortFrom(scope.LocalAddress, mdnsPort), netip.AddrPortFrom(scope.LocalAddress, 53), netip.MustParseAddrPort("192.0.2.200:55000")} {
		if legacyMetadataPath(scope, badLocal, 9, p) != "none" {
			t.Fatal("incorrect bound endpoint accepted")
		}
	}
}

func TestDNSServiceLegacyExactRecordMask(t *testing.T) {
	ad := dnssdTestConfig().Advertisement
	data, err := packAdvertisement(ad)
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := decodeRecordPacket(data, ad)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range records {
		if !dnssdRecordMatchesAdvertisement(original, ad) {
			t.Fatal("fixture does not match")
		}
		bad := original
		switch bad.Kind {
		case ptrRecord:
			bad.Target = "unrelated.local."
		case srvRecord:
			bad.Port++
		case txtRecord:
			bad.TXT = []string{"unrelated=value"}
		case aRecord:
			bad.Address = netip.MustParseAddr("192.0.2.200")
		}
		counters := dnssdReceiveCounters{}
		counters.match([]discoveryRecord{bad}, ad)
		if counters.MatchingRecordKinds != 0 {
			t.Fatal("unrelated record matched synthetic advert")
		}
		bad = original
		bad.Removed = true
		if dnssdRecordMatchesAdvertisement(bad, ad) {
			t.Fatal("removed record accepted")
		}
		bad = original
		bad.TTL = 0
		if dnssdRecordMatchesAdvertisement(bad, ad) {
			t.Fatal("expired record accepted")
		}
	}
}

func TestDNSServiceLegacyLateFailureAndCopiedByteBudget(t *testing.T) {
	for _, failure := range []error{dnssdFailure{stage: "query", kind: "unavailable"}, ErrScopeChanged, ErrInvalidPacket} {
		result := dnssdLegacyDefault()
		result.Status, result.Reason = "verified", "native_legacy_serialization_observed"
		result.APIRecordsReady, result.OneQuerySent, result.LegacySerializationVerified, result.OwnersJoined = true, true, true, true
		dnssdLegacyError(&result, failure)
		if result.Status == "verified" || result.LegacySerializationVerified || result.RawPacketVerified || result.NetworkMulticastVerified {
			t.Fatal("late failure retained or expanded proof")
		}
		if !result.APIRecordsReady || !result.OneQuerySent {
			t.Fatal("late failure erased prior observations")
		}
	}
	counters := dnssdReceiveCounters{}
	for n := 0; n < MaxReceivedDatagrams; n++ {
		if counters.exhausted() {
			t.Fatal("budget ended early")
		}
		counters.received(Packet{Data: make([]byte, MaxDatagramBytes+1)})
	}
	if counters.ReceivedBytes != 614912 || !counters.exhausted() {
		t.Fatal("copied-byte cap missing")
	}
	if !(&dnssdReceiveCounters{ReceivedBytes: dnssdMaxCopiedReceiveBytes}).exhausted() {
		t.Fatal("independent byte cap missing")
	}
}
