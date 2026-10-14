package nearby

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func testRecordObservation(t *testing.T, ad Advertisement, kind observationKind) recordObservation {
	t.Helper()
	b, err := packAdvertisement(ad)
	if err != nil {
		t.Fatal(err)
	}
	records, q, err := decodeRecordPacket(b, testAd(999))
	if err != nil || q {
		t.Fatal("record decode", err)
	}
	out := recordObservation{Kind: kind, InterfaceIndex: testScope().InterfaceIndex, Records: records}
	if kind == rawPacketObservation {
		out.Source = netip.MustParseAddrPort("192.168.40.9:5353")
	}
	return out
}
func testAssembler() (recordAssembler, candidateStore, time.Time) {
	now := time.Unix(1_700_000_000, 0)
	return newRecordAssembler(testScope(), discoveryPolicy{}, now.Add(SessionLifetime), testAd(999).SessionID), newStore(testScope()), now
}
func TestRecordsAssembleAcrossPacketsWithTruthfulProvenance(t *testing.T) {
	for _, kind := range []observationKind{rawPacketObservation, osDNSServiceObservation} {
		a, store, now := testAssembler()
		o := testRecordObservation(t, testAd(1), kind)
		for i := 3; i >= 0; i-- {
			one := o
			one.Records = o.Records[i : i+1]
			if err := a.observe(one, now.Add(time.Duration(3-i)*time.Second), &store); err != nil {
				t.Fatal(err)
			}
			if i != 0 && len(store.items) != 0 {
				t.Fatal("partial candidate")
			}
		}
		c := store.list(now.Add(3 * time.Second))[0]
		expiry := now.Add(CandidateTTL)
		if kind == osDNSServiceObservation {
			expiry = now.Add(SessionLifetime)
		}
		if c.Advertisement != testAd(1) || c.ObservedAt != now || c.ExpiresAt != expiry {
			t.Fatal("assembled hint/first expiry", c)
		}
		if kind == rawPacketObservation && (c.Provenance != RawPacketProvenance || c.Source != o.Source || c.Advertisement.Bootstrap.Addr() == c.Source.Addr()) {
			t.Fatal("raw source lost or equated with endpoint")
		}
		if kind == osDNSServiceObservation && (c.Provenance != OSDNSServiceProvenance || c.Source.IsValid()) {
			t.Fatal("fabricated OS source")
		}
	}
}
func TestRecordsRawRefreshOnlyWhileLiveAndNeverResurrect(t *testing.T) {
	a, s, now := testAssembler()
	o := testRecordObservation(t, testAd(1), rawPacketObservation)
	o.Records[0].TTL = 5 * time.Second
	o.Records[1].TTL = time.Hour
	if err := a.observe(o, now, &s); err != nil {
		t.Fatal(err)
	}
	c := s.list(now)[0]
	if c.ExpiresAt != now.Add(5*time.Second) {
		t.Fatal("TTL minimum lost")
	}
	if err := a.observe(o, now.Add(4*time.Second), &s); err != nil {
		t.Fatal(err)
	}
	refreshed := s.list(now.Add(4 * time.Second))[0]
	if refreshed.ID != c.ID || refreshed.ObservedAt != c.ObservedAt || refreshed.ExpiresAt != now.Add(9*time.Second) {
		t.Fatal("live raw observation did not preserve identity and refresh bounded TTL")
	}
	s.prune(now.Add(9 * time.Second))
	if err := a.observe(o, now.Add(9*time.Second), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.items) != 0 || len(a.bundles) != 1 {
		t.Fatal("prune resurrected replay or lost tombstone")
	}
	if _, err := s.lookup(c.ID, now.Add(9*time.Second)); !errors.Is(err, ErrCandidateUnavailable) {
		t.Fatal("stale ID resolved")
	}
}
func TestRecordsOSActiveQueryHintsBoundedByOwner(t *testing.T) {
	a, s, now := testAssembler()
	o := testRecordObservation(t, testAd(1), osDNSServiceObservation)
	for i := range o.Records {
		o.Records[i].TTL = time.Second
	}
	if err := a.observe(o, now, &s); err != nil {
		t.Fatal(err)
	}
	c := s.list(now)[0]
	if c.ExpiresAt != a.deadline {
		t.Fatal("OS active-query hint lacks owner deadline")
	}
	if _, err := s.lookup(c.ID, now.Add(time.Minute)); err != nil {
		t.Fatal("active OS query hint expired during identity comparison")
	}
	_ = a.observe(o, now.Add(time.Minute), &s)
	if s.list(now.Add(time.Minute))[0].ExpiresAt != a.deadline {
		t.Fatal("OS observation extended absolute owner lifetime")
	}
	if len(s.list(a.deadline)) != 0 {
		t.Fatal("OS query hint outlived owner")
	}
	if err := a.observe(o, a.deadline, &s); !errors.Is(err, ErrExpired) {
		t.Fatal("expired owner resurrected OS cache", err)
	}
}
func TestRecordsConflictsAndRemovalsInvalidateWholeBundle(t *testing.T) {
	for _, name := range []string{"ptr", "srv", "txt", "a", "removed", "zero", "invalid-address", "invalid-txt"} {
		t.Run(name, func(t *testing.T) {
			a, s, now := testAssembler()
			o := testRecordObservation(t, testAd(1), rawPacketObservation)
			if err := a.observe(o, now, &s); err != nil {
				t.Fatal(err)
			}
			id := s.list(now)[0].ID
			changed := testRecordObservation(t, testAd(1), rawPacketObservation)
			switch name {
			case "ptr": // A second endpoint reference in the same identity cannot be chosen.
				changed.Records[0].Port = 1
			case "srv":
				changed.Records[1].Port++
			case "txt":
				changed.Records[2].TXT[2] = "alias=Peer B"
			case "a":
				changed.Records[3].Address = netip.MustParseAddr("192.168.40.3")
			case "removed":
				changed.Records[1].Removed = true
			case "zero":
				changed.Records[0].TTL = 0
			case "invalid-address":
				changed.Records[3].Address = netip.MustParseAddr("192.168.41.3")
			case "invalid-txt":
				changed.Records[2].TXT[0] = "token=unsupported"
			}
			_ = a.observe(changed, now.Add(time.Second), &s)
			if _, err := s.lookup(id, now.Add(time.Second)); !errors.Is(err, ErrCandidateUnavailable) {
				t.Fatal("mutation retained authority")
			}
			_ = a.observe(o, now.Add(2*time.Second), &s)
			if len(s.items) != 0 {
				t.Fatal("old callback resurrected invalidated bundle")
			}
		})
	}
}
func TestRecordsDoNotMergeSourceInterfaceOrProvenance(t *testing.T) {
	a, s, now := testAssembler()
	o := testRecordObservation(t, testAd(1), rawPacketObservation)
	first := o
	first.Records = o.Records[:2]
	_ = a.observe(first, now, &s)
	rest := o
	rest.Records = o.Records[2:]
	rest.Source = netip.MustParseAddrPort("192.168.40.8:5353")
	_ = a.observe(rest, now, &s)
	rest.Source = o.Source
	rest.InterfaceIndex++
	if a.observe(rest, now, &s) == nil {
		t.Fatal("foreign interface accepted")
	}
	rest.InterfaceIndex = o.InterfaceIndex
	rest.Kind = osDNSServiceObservation
	if a.observe(rest, now, &s) == nil {
		t.Fatal("OS spoofed source accepted")
	}
	rest.Source = netip.AddrPort{}
	_ = a.observe(rest, now, &s)
	if len(s.items) != 0 {
		t.Fatal("cross-provenance/source partial records combined")
	}
	_ = a.observe(o, now, &s)
	rawID := s.list(now)[0].ID
	os := testRecordObservation(t, testAd(1), osDNSServiceObservation)
	_ = a.observe(os, now, &s)
	if len(s.items) != 2 {
		t.Fatal("source identities merged")
	}
	for _, c := range s.items {
		if c.Provenance == OSDNSServiceProvenance && c.ID == rawID {
			t.Fatal("provenance missing from ID")
		}
	}
}
func TestRecordsLifetimeBoundsAndDeadline(t *testing.T) {
	a, s, now := testAssembler()
	for i := 1; i <= MaxCandidates; i++ {
		if err := a.observe(testRecordObservation(t, testAd(i), osDNSServiceObservation), now, &s); err != nil {
			t.Fatal(err)
		}
	}
	s.prune(now.Add(CandidateTTL))
	if err := a.observe(testRecordObservation(t, testAd(65), osDNSServiceObservation), now.Add(CandidateTTL), &s); !errors.Is(err, ErrReceiveBudget) {
		t.Fatal("lifetime bundle cap renewed", err)
	}
	a, s, now = testAssembler()
	o := testRecordObservation(t, testAd(1), osDNSServiceObservation)
	for i := 0; ; i++ {
		err := a.observe(o, now, &s)
		if errors.Is(err, ErrReceiveBudget) {
			break
		}
		if err != nil || i > 2000 {
			t.Fatal("byte cap not reached", err)
		}
	}
	if a.admittedBytes > maxRecordLifetimeBytes {
		t.Fatal("byte budget overrun")
	}
	a, s, now = testAssembler()
	if err := a.observe(o, now.Add(SessionLifetime-3*time.Second), &s); err != nil {
		t.Fatal(err)
	}
	if s.list(now.Add(SessionLifetime - 2*time.Second))[0].ExpiresAt != now.Add(SessionLifetime) {
		t.Fatal("candidate outlives owner")
	}
	if err := a.observe(o, now.Add(SessionLifetime), &s); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}
func TestRecordsRejectBoundsBeforeCopying(t *testing.T) {
	for _, mutate := range []func(*recordObservation){
		func(o *recordObservation) { o.Records = make([]discoveryRecord, maxObservationRecords+1) },
		func(o *recordObservation) { o.Records[0].Name = strings.Repeat("a", 256) },
		func(o *recordObservation) { o.Records[2].TXT = make([]string, 6) },
		func(o *recordObservation) { o.Records[2].TXT[0] = strings.Repeat("a", 256) },
		func(o *recordObservation) { o.Records[3].Address = netip.MustParseAddr("::ffff:192.168.40.2") },
		func(o *recordObservation) { o.Records[3].TTL = -time.Second },
		func(o *recordObservation) { o.Records[0].Target = "arbitrary.local." },
	} {
		a, s, now := testAssembler()
		o := testRecordObservation(t, testAd(1), osDNSServiceObservation)
		mutate(&o)
		if a.observe(o, now, &s) == nil || len(s.items) != 0 || a.admittedBytes != 0 {
			t.Fatal("malformed records admitted/copied")
		}
	}
}
func TestRecordWireAdditionalUnknownAndQuestions(t *testing.T) {
	packet := testPacket(t, testAd(1))
	var m dnsmessage.Message
	_ = m.Unpack(packet.Data)
	m.Answers[0].Header.TTL = 60
	m.Answers[1].Header.TTL = 5
	unknown, _ := dnsmessage.NewName("unrelated.local.")
	m.Additionals = append(m.Answers[1:], dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: unknown, Type: dnsmessage.Type(47), Class: 1, TTL: 30}, Body: &dnsmessage.UnknownResource{Type: dnsmessage.Type(47), Data: []byte{0}}})
	m.Answers = m.Answers[:1]
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	records, q, err := decodeRecordPacket(b, testAd(999))
	if err != nil || q || len(records) != 4 {
		t.Fatal("Bonjour answer/additional layout", len(records), err)
	}
	a, s, now := testAssembler()
	o := recordObservation{Kind: osDNSServiceObservation, InterfaceIndex: 7, Records: records}
	if err = a.observe(o, now, &s); err != nil || len(s.items) != 1 {
		t.Fatal(err)
	}
	local := testAd(999)
	instance, host := wireNames(local)
	service, _ := dnsmessage.NewName(serviceName)
	questions := []dnsmessage.Question{{Name: service, Type: 12, Class: 0x8001}, {Name: instance, Type: 33, Class: 1}, {Name: instance, Type: 16, Class: 1}, {Name: host, Type: 1, Class: 1}}
	m = dnsmessage.Message{Questions: questions, Answers: m.Answers}
	b, _ = m.Pack()
	records, q, err = decodeRecordPacket(b, local)
	if err != nil || !q || len(records) != 0 {
		t.Fatal("bounded QU/known answer query", err)
	}
	m.Questions = []dnsmessage.Question{{Name: unknown, Type: 1, Class: 1}}
	b, _ = m.Pack()
	_, q, err = decodeRecordPacket(b, local)
	if err != nil || q {
		t.Fatal("arbitrary query causes reply")
	}
}
func TestRecordWireRejectsCountTXTCompressionAndTrailingBounds(t *testing.T) {
	p := testPacket(t, testAd(1))
	for _, offset := range []int{4, 6, 8, 10} {
		b := append([]byte(nil), p.Data...)
		binary.BigEndian.PutUint16(b[offset:offset+2], 65535)
		if _, _, err := decodeRecordPacket(b, testAd(999)); err == nil {
			t.Fatal("unbounded section count")
		}
	}
	for _, b := range [][]byte{nil, make([]byte, MaxDatagramBytes+1), append(append([]byte(nil), p.Data...), 0), {0xc0, 0}, {63, 1}} {
		if _, _, err := decodeRecordPacket(b, testAd(999)); err == nil {
			t.Fatal("invalid packet accepted")
		}
	}
	if _, _, err := dnsWireName([]byte{0xc0, 0}, 0); err == nil {
		t.Fatal("compression cycle")
	}
	var m dnsmessage.Message
	_ = m.Unpack(p.Data)
	m.Answers[2].Body.(*dnsmessage.TXTResource).TXT = make([]string, 100)
	b, _ := m.Pack()
	if _, _, err := decodeRecordPacket(b, testAd(999)); err == nil {
		t.Fatal("TXT count allocation bomb")
	}
}
func FuzzRecordWireBounds(f *testing.F) {
	b, _ := packAdvertisement(testAd(1))
	f.Add(b)
	f.Add(packQuery())
	f.Add([]byte{0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		records, _, err := decodeRecordPacket(data, testAd(999))
		if err == nil && len(records) > maxObservationRecords {
			t.Fatal("record bound")
		}
	})
}

func TestRawSessionAssemblesAdditionalAndSeparatePackets(t *testing.T) {
	s, _, tr := startFake(t)
	ad := testAd(1)
	p := testPacket(t, ad)
	p.Source = netip.MustParseAddrPort("192.168.40.9:5353")
	var m dnsmessage.Message
	_ = m.Unpack(p.Data)
	all := m.Answers
	for _, part := range [][]dnsmessage.Resource{all[:1], all[2:3], {all[1], all[3]}} {
		m.Answers = nil
		m.Additionals = part
		p.Data, _ = m.Pack()
		tr.incoming <- p
	}
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	c := s.Candidates()[0]
	if c.Advertisement != ad || c.Source != p.Source || c.Provenance != RawPacketProvenance {
		t.Fatal("multi-packet raw record path mismatch", c)
	}
}

func TestRecordWirePreservesDNSLabelBoundaries(t *testing.T) {
	// Two wire labels must never be flattened into the same apparent name as
	// three labels merely because a label contains a literal dot byte.
	encoded := []byte{15}
	encoded = append(encoded, []byte("_sobalink-setup")...)
	encoded[0] = byte(len("_sobalink-setup"))
	encoded = append(encoded, 10)
	encoded = append(encoded, []byte("_tcp.local")...)
	encoded = append(encoded, 0)
	name, next, err := dnsWireName(encoded, 0)
	if err != nil || next != len(encoded) || name == serviceName {
		t.Fatal("embedded dot forged canonical service", name, next, err)
	}
	var b []byte
	for _, label := range strings.Split(strings.TrimSuffix(strings.ToUpper(serviceName), "."), ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	name, next, err = dnsWireName(b, 0)
	if err != nil || name != serviceName || next != len(b) {
		t.Fatal("normal case-insensitive DNS name rejected", name, err)
	}
	// Label traversal followed by a backward pointer can cycle; the explicit
	// decompression step cap must terminate even though every pointer is backward.
	if _, _, err = dnsWireName([]byte{1, 'a', 0xc0, 0}, 0); err == nil {
		t.Fatal("cyclic label/pointer path accepted")
	}
}

func TestRawPTRFollowupCompletesLatePeerExactlyOnce(t *testing.T) {
	s, _, tr := startFake(t)
	p := testPacket(t, testAd(1))
	var m dnsmessage.Message
	_ = m.Unpack(p.Data)
	all := m.Answers
	m.Answers = all[:1]
	p.Data, _ = m.Pack()
	tr.incoming <- p
	eventually(t, func() bool { return tr.count() == 3 })
	tr.mu.Lock()
	query := append([]byte(nil), tr.writes[2]...)
	tr.mu.Unlock()
	var q dnsmessage.Message
	if err := q.Unpack(query); err != nil {
		t.Fatal(err)
	}
	instance, host := wireNames(testAd(1))
	expected := []dnsmessage.Question{{Name: instance, Type: dnsmessage.TypeSRV, Class: 1}, {Name: instance, Type: dnsmessage.TypeTXT, Class: 1}, {Name: host, Type: dnsmessage.TypeA, Class: 1}}
	if len(q.Questions) != 3 || q.Header.Response || len(query) > MaxDatagramBytes {
		t.Fatal("unbounded followup")
	}
	for i, want := range expected {
		if q.Questions[i] != want {
			t.Fatal("arbitrary name/type/unicast followup", q.Questions)
		}
	}
	if len(s.Candidates()) != 0 {
		t.Fatal("PTR alone became a candidate")
	}
	tr.incoming <- p // Duplicate PTR cannot schedule a retry.
	m.Answers = nil
	m.Additionals = all[1:]
	p.Data, _ = m.Pack()
	tr.incoming <- p
	eventually(t, func() bool { return len(s.Candidates()) == 1 })
	if tr.count() != 3 || s.Candidates()[0].Advertisement != testAd(1) {
		t.Fatal("late peer incomplete or followup retried")
	}
}
func TestRawPTRFollowupSkipsInvalidLocalCompleteAndExpired(t *testing.T) {
	for _, name := range []string{"complete", "self", "poisoned", "expired", "foreign-interface", "foreign-source"} {
		t.Run(name, func(t *testing.T) {
			a, s, now := testAssembler()
			o := testRecordObservation(t, testAd(1), rawPacketObservation)
			switch name {
			case "complete":
				_ = a.observe(o, now, &s)
			case "self":
				o = testRecordObservation(t, testAd(999), rawPacketObservation)
				_ = a.observe(o, now, &s)
			default:
				first := o
				first.Records = o.Records[:1]
				_ = a.observe(first, now, &s)
				if name == "poisoned" {
					remove := first
					remove.Records = append([]discoveryRecord(nil), first.Records...)
					remove.Records[0].Removed = true
					_ = a.observe(remove, now, &s)
				}
				if name == "expired" {
					now = now.Add(CandidateTTL)
				}
				if name == "foreign-interface" {
					o.InterfaceIndex++
				}
				if name == "foreign-source" {
					o.Source = netip.MustParseAddrPort("192.168.41.2:5353")
				}
			}
			if got := a.requestQueries(o, now, &s); len(got) != 0 {
				t.Fatal("unsafe/unnecessary followup", got)
			}
		})
	}
	a, s, now := testAssembler()
	o := testRecordObservation(t, testAd(1), rawPacketObservation)
	o.Records = o.Records[:1]
	_ = a.observe(o, now, &s)
	if got := a.requestQueries(o, now, &s); len(got) != 1 {
		t.Fatal("valid partial not scheduled")
	}
	// Reserving precedes the write attempt, so even failed writes cannot retry.
	if got := a.requestQueries(o, now, &s); len(got) != 0 {
		t.Fatal("failed or duplicate request can retry")
	}
}
