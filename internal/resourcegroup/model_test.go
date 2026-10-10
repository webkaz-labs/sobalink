package resourcegroup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func groupSelectionFixture(t *testing.T, count int) Selection {
	t.Helper()
	template, err := NewTemplate(resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(3)})
	if err != nil {
		t.Fatal(err)
	}
	s := Selection{SchemaVersion: SchemaVersion, Template: template, Members: make([]Member, count)}
	for i := range s.Members {
		s.Members[i] = Member{
			PeerKey: fmt.Sprintf("%064x", i+1),
			Selector: resourcegrant.ManagementSelector{
				ProtocolVersion: resourcegrant.ManagementProtocolVersion,
				Target:          resource.Target{SchemaVersion: resource.SchemaVersion, ResourceID: fmt.Sprintf("%032x", i+100)},
				GrantID:         fmt.Sprintf("%032x", i+1000), GrantRevision: 1,
			},
		}
	}
	return s
}

func groupRowsFixture(s ResolvedSelection) []ReviewRow {
	rows := make([]ReviewRow, len(s.Members))
	for i, member := range s.Members {
		rows[i] = ReviewRow{
			SchemaVersion: SchemaVersion, PeerKey: member.PeerKey, State: ReviewReady,
			Reply: &resourcegrant.ManagementReply{
				ManagementSelector: member.Selector, Action: resourcegrant.PreviewAction,
				Preview: &resourcegrant.ManagementPreview{
					OperationID: strings.Repeat("a", 64), BaseRevision: strings.Repeat("b", 64), ReviewRevision: strings.Repeat("c", 64),
					Requested: cloneSettings(member.Requested), Effective: resource.Effective{TransferConcurrentFiles: 8, TransferConcurrentPerPeer: 3},
				},
			},
		}
	}
	return rows
}

func groupReviewFixture(t *testing.T, count int) ReviewBody {
	t.Helper()
	s, err := ResolveSelection(groupSelectionFixture(t, count))
	if err != nil {
		t.Fatal(err)
	}
	peers := make([]string, len(s.Members))
	for i, member := range s.Members {
		peers[i] = member.PeerKey
	}
	r, err := BuildReview(s, groupRowsFixture(s), peers)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func groupJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGroupTemplateCanonicalVector(t *testing.T) {
	template := groupSelectionFixture(t, 1).Template
	const want = "dee87ed640530919edd7773915c5a15d06adf792c1a73d5fcb6ee92bdb81bd62"
	if template.Revision != want || template.Validate() != nil {
		t.Fatalf("canonical template revision: %s", template.Revision)
	}
	settings := cloneSettings(template.Settings)
	settings.TransferConcurrentFiles = capacity.Limited(8)
	changed, err := NewTemplate(settings)
	if err != nil || changed.Revision == template.Revision {
		t.Fatal("files choice not bound")
	}
	settings = cloneSettings(template.Settings)
	settings.TransferConcurrentPerPeer = capacity.Default()
	changed, err = NewTemplate(settings)
	if err != nil || changed.Revision == template.Revision {
		t.Fatal("per-peer choice not bound")
	}
	template.SchemaVersion++
	if template.Validate() == nil {
		t.Fatal("future schema accepted")
	}
}

func TestGroupSelectionResolvesExplicitOverrides(t *testing.T) {
	s := groupSelectionFixture(t, 3)
	defaultChoice := capacity.Default()
	equalChoice := capacity.Limited(3)
	s.Members[0].Override = &Override{TransferConcurrentPerPeer: &defaultChoice}
	s.Members[1].Override = &Override{TransferConcurrentPerPeer: &equalChoice}
	s.Members[0], s.Members[2] = s.Members[2], s.Members[0]
	r, err := ResolveSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Members[0].PeerKey >= r.Members[1].PeerKey || r.Members[0].Requested.TransferConcurrentPerPeer.Mode != "default" || r.Members[0].Override == nil {
		t.Fatal("explicit default or canonical order lost")
	}
	if r.Members[1].Override == nil || !equalSettings(r.Members[1].Requested, r.Members[2].Requested) || r.Members[2].Override != nil {
		t.Fatal("explicit equal override collapsed into inheritance")
	}
	if r.Members[0].Requested.TransferConcurrentFiles.Mode != "default" {
		t.Fatal("full template was not inherited")
	}
}

func TestGroupSelectionRejectsInvalidMembersAndChoices(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Selection)
	}{
		{"empty", func(s *Selection) { s.Members = []Member{} }},
		{"count", func(s *Selection) { s.Members = make([]Member, MaxMembers+1) }},
		{"duplicate", func(s *Selection) { s.Members[1] = s.Members[0] }},
		{"duplicate_different_selector", func(s *Selection) { s.Members[1].PeerKey = s.Members[0].PeerKey }},
		{"wildcard", func(s *Selection) { s.Members[0].PeerKey = "*" }},
		{"uppercase_peer", func(s *Selection) { s.Members[0].PeerKey = strings.Repeat("A", 64) }},
		{"target", func(s *Selection) { s.Members[0].Selector.Target.ResourceID = "target" }},
		{"grant", func(s *Selection) { s.Members[0].Selector.GrantID = "grant" }},
		{"protocol", func(s *Selection) { s.Members[0].Selector.ProtocolVersion = 1 }},
		{"zero_revision", func(s *Selection) { s.Members[0].Selector.GrantRevision = 0 }},
		{"overflow_revision", func(s *Selection) { s.Members[0].Selector.GrantRevision = uint64(capacity.MaxJSONInteger) + 1 }},
		{"empty_override", func(s *Selection) { s.Members[0].Override = &Override{} }},
		{"unlimited", func(s *Selection) {
			c := capacity.Unlimited()
			s.Members[0].Override = &Override{TransferConcurrentFiles: &c}
		}},
		{"zero_choice", func(s *Selection) {
			c := capacity.Limited(0)
			s.Members[0].Override = &Override{TransferConcurrentFiles: &c}
		}},
		{"negative_choice", func(s *Selection) {
			c := capacity.Limited(-1)
			s.Members[0].Override = &Override{TransferConcurrentFiles: &c}
		}},
		{"overflow_choice", func(s *Selection) {
			c := capacity.Limited(capacity.MaxJSONInteger + 1)
			s.Members[0].Override = &Override{TransferConcurrentFiles: &c}
		}},
		{"changed_template", func(s *Selection) { s.Template.Settings.TransferConcurrentFiles = capacity.Limited(7) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := groupSelectionFixture(t, 2)
			tc.change(&s)
			if got, err := ResolveSelection(s); err == nil || !reflect.DeepEqual(got, ResolvedSelection{}) {
				t.Fatal("invalid selection accepted")
			}
		})
	}
}

func TestGroupSelectionDeepCopiesChoicesAndOverrides(t *testing.T) {
	s := groupSelectionFixture(t, 2)
	c := capacity.Limited(5)
	s.Members[0].Override = &Override{TransferConcurrentFiles: &c}
	r, err := ResolveSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	before := string(groupJSON(t, r))
	*s.Template.Settings.TransferConcurrentPerPeer.Value = 19
	*c.Value = 17
	s.Members[0].PeerKey = strings.Repeat("f", 64)
	if string(groupJSON(t, r)) != before {
		t.Fatal("caller mutation changed resolved selection")
	}
	*r.Members[0].Requested.TransferConcurrentFiles.Value = 11
	if *r.Members[0].Override.TransferConcurrentFiles.Value != 5 {
		t.Fatal("resolved choice aliases retained override")
	}
	if r.Validate() == nil {
		t.Fatal("changed resolved settings accepted")
	}
}
