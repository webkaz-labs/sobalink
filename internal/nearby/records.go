package nearby

import (
	"encoding/base32"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"
)

// sessionFromName accepts only the full ephemeral identifier's canonical name.
// Arbitrary discovered hostnames are never queried or used as endpoint names.
func sessionFromName(name, suffix string) (string, bool) {
	if len(name) != 53+len(suffix) || name[0] != 's' || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	label := name[1:53]
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(label))
	if err != nil || len(raw) != 32 || strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)) != label {
		return "", false
	}
	id := hex.EncodeToString(raw)
	return id, id != strings.Repeat("0", 64)
}
func recordSession(r discoveryRecord) (string, bool) {
	switch r.Kind {
	case ptrRecord:
		if r.Name != serviceName {
			return "", false
		}
		return sessionFromName(r.Target, "."+serviceName)
	case srvRecord, txtRecord:
		return sessionFromName(r.Name, "."+serviceName)
	case aRecord:
		return sessionFromName(r.Name, ".local.")
	}
	return "", false
}
func txtAdvertisement(fields []string) (Advertisement, error) {
	if len(fields) != 5 {
		return Advertisement{}, ErrInvalidPacket
	}
	var out Advertisement
	seen := uint8(0)
	for _, field := range fields {
		if len(field) > 255 {
			return out, ErrInvalidPacket
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return out, ErrInvalidPacket
		}
		var bit uint8
		switch key {
		case "v":
			bit = 1
			if value != "1" {
				return out, ErrInvalidPacket
			}
		case "session":
			bit = 2
			out.SessionID = value
		case "alias":
			bit = 4
			out.Alias = value
		case "protocol":
			bit = 8
			out.Protocol = value
		case "capability":
			bit = 16
			out.Capability = value
		default:
			return out, ErrInvalidPacket
		}
		if seen&bit != 0 {
			return out, ErrInvalidPacket
		}
		seen |= bit
	}
	return out, nil
}

// validateRecord checks every allocation-relevant bound before copying. A
// record contains only fields of its own type; removed records retain identity.
func validateRecord(r discoveryRecord, scope Scope, policy discoveryPolicy) (string, int, error) {
	if len(r.Name) > 255 || len(r.Target) > 255 || len(r.TXT) > 5 || r.TTL < 0 || r.TTL > time.Duration(^uint32(0))*time.Second {
		return "", 0, ErrInvalidPacket
	}
	size := len(r.Name) + len(r.Target) + 24
	for _, s := range r.TXT {
		if len(s) > 255 {
			return "", 0, ErrInvalidPacket
		}
		size += len(s) + 1
	}
	if size > maxRecordBytes {
		return "", 0, ErrInvalidPacket
	}
	sid, ok := recordSession(r)
	if !ok {
		return "", 0, ErrInvalidPacket
	}
	switch r.Kind {
	case ptrRecord:
		if r.Priority != 0 || r.Weight != 0 || r.Port != 0 || len(r.TXT) != 0 || r.Address.IsValid() {
			return "", 0, ErrInvalidPacket
		}
	case srvRecord:
		_, host := wireNames(Advertisement{SessionID: sid})
		if r.Priority != 0 || r.Weight != 0 || r.Port < 1024 || (r.Port >= 54543 && r.Port <= 54545) || r.Target != host.String() || len(r.TXT) != 0 || r.Address.IsValid() {
			return "", 0, ErrInvalidPacket
		}
	case txtRecord:
		if r.Target != "" || r.Priority != 0 || r.Weight != 0 || r.Port != 0 || r.Address.IsValid() {
			return "", 0, ErrInvalidPacket
		}
		ad, err := txtAdvertisement(r.TXT)
		ad.Bootstrap = netip.AddrPortFrom(scope.LocalAddress, 42000)
		if err != nil || ad.SessionID != sid || ad.validate(policy) != nil {
			return "", 0, ErrInvalidPacket
		}
	case aRecord:
		if r.Target != "" || r.Priority != 0 || r.Weight != 0 || r.Port != 0 || len(r.TXT) != 0 || !r.Address.Is4() || !policy.validAddress(r.Address) || !scope.Prefix.Contains(r.Address) {
			return "", 0, ErrInvalidPacket
		}
	default:
		return "", 0, ErrInvalidPacket
	}
	return sid, size, nil
}
func sameRecord(a, b discoveryRecord) bool {
	if a.Kind != b.Kind || a.Name != b.Name || a.Target != b.Target || a.Priority != b.Priority || a.Weight != b.Weight || a.Port != b.Port || a.Address != b.Address || len(a.TXT) != len(b.TXT) {
		return false
	}
	// TXT order is not meaningful; validation guarantees exactly the five keys.
	for _, x := range a.TXT {
		found := false
		for _, y := range b.TXT {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type observedRecord struct {
	value          discoveryRecord
	first, expires time.Time
}
type recordBundle struct {
	records [4]*observedRecord
	blocked bool
	queried bool
}
type recordAssembler struct {
	scope         Scope
	policy        discoveryPolicy
	deadline      time.Time
	localSession  string
	bundles       map[string]*recordBundle
	admittedBytes int
}

func newRecordAssembler(scope Scope, policy discoveryPolicy, deadline time.Time, local string) recordAssembler {
	return recordAssembler{scope: scope, policy: policy, deadline: deadline, localSession: local, bundles: make(map[string]*recordBundle)}
}
func recordObservationKey(kind observationKind, source netip.AddrPort, sid string) string {
	if kind == osDNSServiceObservation {
		return "os/" + sid
	}
	return "raw/" + source.String() + "/" + sid
}
func (a *recordAssembler) observe(o recordObservation, now time.Time, store *candidateStore) error {
	if !now.Before(a.deadline) {
		return ErrExpired
	}
	if o.InterfaceIndex != a.scope.InterfaceIndex || len(o.Records) > maxObservationRecords {
		return ErrInvalidPacket
	}
	switch o.Kind {
	case rawPacketObservation:
		if o.Source.Port() != mdnsPort || !o.Source.Addr().Is4() || !a.policy.validAddress(o.Source.Addr()) || !a.scope.Prefix.Contains(o.Source.Addr()) {
			return ErrInvalidPacket
		}
	case osDNSServiceObservation:
		if o.Source != (netip.AddrPort{}) || o.query {
			return ErrInvalidPacket
		}
	default:
		return ErrInvalidPacket
	}
	// Preflight the whole event before mutation or copying. Malformed records
	// whose identity is still known poison that identity, never leave a stale hint.
	type checked struct {
		r   discoveryRecord
		sid string
	}
	var records [maxObservationRecords]checked
	total := 0
	for i, r := range o.Records {
		sid, size, err := validateRecord(r, a.scope, a.policy)
		if err != nil {
			if sid, ok := recordSession(r); ok {
				key := recordObservationKey(o.Kind, o.Source, sid)
				if b := a.bundles[key]; b != nil {
					b.blocked = true
					delete(store.items, key)
				}
			}
			return err
		}
		records[i] = checked{r, sid}
		total += size
	}
	if total > maxRecordLifetimeBytes-a.admittedBytes {
		return ErrReceiveBudget
	}
	a.admittedBytes += total
	for _, c := range records[:len(o.Records)] {
		if c.sid == a.localSession {
			continue
		}
		key := recordObservationKey(o.Kind, o.Source, c.sid)
		b := a.bundles[key]
		if b == nil {
			if len(a.bundles) >= MaxCandidates {
				return ErrReceiveBudget
			}
			b = &recordBundle{}
			a.bundles[key] = b
		}
		if b.blocked {
			continue
		}
		slot := int(c.r.Kind) - 1
		prior := b.records[slot]
		if c.r.Removed || c.r.TTL == 0 || (prior != nil && (!sameRecord(prior.value, c.r) || !now.Before(prior.expires))) {
			b.blocked = true
			delete(store.items, key)
			continue
		}
		expiry := a.deadline
		if o.Kind == rawPacketObservation {
			expiry = minTime(now.Add(min(c.r.TTL, CandidateTTL)), a.deadline)
		}
		// A matching, newly received raw observation may refresh only a live
		// record. OS hints instead remain bounded by their active query owner;
		// they make no claim about cache age, packet source or reachability.
		if prior == nil {
			r := c.r
			r.TXT = append([]string(nil), r.TXT...)
			b.records[slot] = &observedRecord{value: r, first: now, expires: expiry}
		} else {
			prior.expires = expiry
		}
		complete := true
		first, expiry := now, a.deadline
		for _, r := range b.records {
			if r == nil {
				complete = false
				continue
			}
			first = minTime(first, r.first)
			expiry = minTime(expiry, r.expires)
			if !now.Before(r.expires) {
				b.blocked = true
				delete(store.items, key)
				complete = false
			}
		}
		if !complete || b.blocked {
			continue
		}
		ad, _ := txtAdvertisement(b.records[2].value.TXT)
		ad.Bootstrap = netip.AddrPortFrom(b.records[3].value.Address, b.records[1].value.Port)
		if ad.validate(a.policy) != nil {
			b.blocked = true
			delete(store.items, key)
			continue
		}
		store.publish(key, ad, o.Kind, o.Source, first, expiry)
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// requestQueries reserves each exact-name followup before its one attempted
// write. It adds no queue or owner: bundle tombstones retain the reservation.
func (a *recordAssembler) requestQueries(o recordObservation, now time.Time, store *candidateStore) []string {
	if o.Kind != rawPacketObservation || !now.Before(a.deadline) || o.InterfaceIndex != a.scope.InterfaceIndex || len(o.Records) > maxObservationRecords || o.Source.Port() != mdnsPort || !a.scope.Prefix.Contains(o.Source.Addr()) {
		return nil
	}
	var out []string
	for _, r := range o.Records {
		if r.Kind != ptrRecord {
			continue
		}
		sid, _, err := validateRecord(r, a.scope, a.policy)
		if err != nil || sid == a.localSession {
			continue
		}
		key := recordObservationKey(o.Kind, o.Source, sid)
		b := a.bundles[key]
		if b == nil || b.blocked || b.queried || b.records[0] == nil {
			continue
		}
		if _, complete := store.items[key]; complete {
			continue
		}
		live := true
		for _, rec := range b.records {
			if rec != nil && !now.Before(rec.expires) {
				live = false
				break
			}
		}
		if !live {
			b.blocked = true
			delete(store.items, key)
			continue
		}
		b.queried = true
		out = append(out, sid)
	}
	return out
}
