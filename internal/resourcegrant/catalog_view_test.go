package resourcegrant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixtureCatalogView() CatalogViewEnvelope {
	r := fixtureRecord()
	return CatalogViewEnvelope{Version: CatalogViewVersion, Target: r.Target, HighWater: revision(1), Clock: &WallCheckpoint{Seconds: 100}, Records: []CatalogViewRecord{{ID: r.ID, Revision: r.Revision, Target: r.Target, Relationship: r.Relationship, ServiceIDs: []string{strings.Repeat("f", 32)}, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt, State: r.State}}}
}

func TestCatalogViewExactSelection(t *testing.T) {
	r := fixtureCatalogView().Records[0]
	if !r.MatchesSelection(r.Relationship, r.ServiceIDs[0], time.Unix(150, 0)) {
		t.Fatal("exact current selection rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*CatalogViewRecord, *Relationship, *string, *time.Time)
	}{
		{"revoked", func(r *CatalogViewRecord, _ *Relationship, _ *string, _ *time.Time) { r.State = Revoked }},
		{"expired state", func(r *CatalogViewRecord, _ *Relationship, _ *string, _ *time.Time) { r.State = Expired }},
		{"deadline", func(_ *CatalogViewRecord, _ *Relationship, _ *string, now *time.Time) { *now = time.Unix(200, 0) }},
		{"before issuance", func(_ *CatalogViewRecord, _ *Relationship, _ *string, now *time.Time) { *now = time.Unix(99, 0) }},
		{"repaired", func(_ *CatalogViewRecord, peer *Relationship, _ *string, _ *time.Time) {
			peer.PairBinding = strings.Repeat("a", 64)
		}},
		{"different peer", func(_ *CatalogViewRecord, peer *Relationship, _ *string, _ *time.Time) {
			peer.PeerKey = strings.Repeat("a", 64)
		}},
		{"new share generation", func(_ *CatalogViewRecord, _ *Relationship, service *string, _ *time.Time) {
			*service = strings.Repeat("a", 32)
		}},
		{"wildcard", func(_ *CatalogViewRecord, _ *Relationship, service *string, _ *time.Time) { *service = "*" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureCatalogView().Records[0]
			peer, service, now := r.Relationship, r.ServiceIDs[0], time.Unix(150, 0)
			tc.change(&r, &peer, &service, &now)
			if r.MatchesSelection(peer, service, now) {
				t.Fatal("changed or terminal selection accepted")
			}
		})
	}
}

func TestCatalogViewCoexistsWithoutChangingLegacyGrant(t *testing.T) {
	legacy, catalog := fixtureEnvelope(), fixtureCatalogView()
	before, _ := json.Marshal(legacy)
	if legacy.Validate() != nil || catalog.Validate() != nil {
		t.Fatal("independent active grants rejected")
	}
	raw, _ := json.Marshal(catalog)
	if _, err := DecodeCatalogView(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("catalog decoded as legacy authority")
	}
	if _, err := DecodeCatalogView(before); err == nil {
		t.Fatal("legacy grant decoded as catalog authority")
	}
	after, _ := json.Marshal(legacy)
	if string(before) != string(after) {
		t.Fatal("legacy grant changed")
	}
	for _, extra := range []string{`,"actions":["connect"]`, `,"managementRecords":[]`} {
		bad := append(append([]byte{}, raw[:len(raw)-1]...), []byte(extra+"}")...)
		if _, err := DecodeCatalogView(bad); err == nil {
			t.Fatal("extra authority accepted")
		}
	}
}

func TestCatalogViewBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CatalogViewEnvelope)
	}{
		{"no services", func(e *CatalogViewEnvelope) { e.Records[0].ServiceIDs = nil }},
		{"duplicate services", func(e *CatalogViewEnvelope) {
			e.Records[0].ServiceIDs = []string{strings.Repeat("f", 32), strings.Repeat("f", 32)}
		}},
		{"wildcard", func(e *CatalogViewEnvelope) { e.Records[0].ServiceIDs = []string{"*"} }},
		{"two active", func(e *CatalogViewEnvelope) {
			r := e.Records[0]
			r.ID = strings.Repeat("b", 32)
			r.Revision = 2
			e.Records = append(e.Records, r)
			e.HighWater = revision(2)
		}},
		{"high water mismatch", func(e *CatalogViewEnvelope) { e.HighWater = revision(2) }},
		{"no clock", func(e *CatalogViewEnvelope) { e.Clock = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixtureCatalogView()
			tc.change(&e)
			if e.Validate() == nil {
				t.Fatal("invalid catalog eligibility accepted")
			}
		})
	}
}

func TestCatalogViewRevocationPreservesManagementGrant(t *testing.T) {
	management := managementEnvelopeFixture()
	before, err := json.Marshal(management)
	if err != nil || management.Validate() != nil {
		t.Fatal("invalid management fixture")
	}
	catalog := fixtureCatalogView()
	if catalog.Validate() != nil {
		t.Fatal("independent active catalog grant rejected")
	}
	catalog.Records[0].State = Revoked
	if catalog.Validate() != nil || catalog.Records[0].MatchesSelection(catalog.Records[0].Relationship, catalog.Records[0].ServiceIDs[0], time.Unix(150, 0)) {
		t.Fatal("catalog revoke did not remove eligibility")
	}
	after, _ := json.Marshal(management)
	if string(before) != string(after) || management.Validate() != nil {
		t.Fatal("catalog revoke changed management grant")
	}
	if _, err := DecodeCatalogView(before); err == nil {
		t.Fatal("management decoded as catalog authority")
	}
}
