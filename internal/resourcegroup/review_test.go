package resourcegroup

import (
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

func TestGroupReviewCanonicalVector(t *testing.T) {
	r := groupReviewFixture(t, 1)
	const want = "2430a6298e0e188c3b9e9a3c7e6555933c54e501204737c886b3be24a3749214"
	if r.Revision != want || r.Validate() != nil {
		t.Fatalf("canonical review revision: %s", r.Revision)
	}
}

func TestGroupReviewCanonicalOrderingAndExplicitSubset(t *testing.T) {
	r := groupReviewFixture(t, 3)
	r.Selection.Members[0], r.Selection.Members[2] = r.Selection.Members[2], r.Selection.Members[0]
	r.Rows[0], r.Rows[2] = r.Rows[2], r.Rows[0]
	r.ExecutionPeers[0], r.ExecutionPeers[2] = r.ExecutionPeers[2], r.ExecutionPeers[0]
	canonical, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers)
	if err != nil || canonical.Revision != r.Revision {
		t.Fatal("input order changed canonical revision")
	}
	partial, err := BuildReview(r.Selection, r.Rows, []string{canonical.Rows[1].PeerKey})
	if err != nil || len(partial.Rows) != 3 || len(partial.ExecutionPeers) != 1 || partial.Revision == canonical.Revision {
		t.Fatal("explicit subset lost selected rows or revision binding")
	}
	if _, err := ApplyRequestFor(partial, canonical.Rows[0].PeerKey); err == nil {
		t.Fatal("excluded ready peer admitted")
	}
	if _, err := ApplyRequestFor(partial, canonical.Rows[1].PeerKey); err != nil {
		t.Fatal(err)
	}
}

func TestGroupReviewIncludesEveryFailure(t *testing.T) {
	r := groupReviewFixture(t, 2)
	r.Rows[1].State = ReviewUnavailable
	r.Rows[1].Reply = nil
	partial, err := BuildReview(r.Selection, r.Rows, []string{r.Rows[0].PeerKey})
	if err != nil || len(partial.Rows) != 2 || partial.Rows[1].State != ReviewUnavailable {
		t.Fatal("unavailable selected member was omitted")
	}
	if _, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers); err == nil {
		t.Fatal("failed row included in execution")
	}
	if _, err := BuildReview(r.Selection, r.Rows[:1], []string{r.Rows[0].PeerKey}); err == nil {
		t.Fatal("missing failure accepted")
	}
	for i := range r.Rows {
		r.Rows[i].State, r.Rows[i].Reply = ReviewUnavailable, nil
	}
	failed, err := BuildReview(r.Selection, r.Rows, []string{})
	if err != nil || failed.Validate() != nil {
		t.Fatal("all-failed review should remain viewable")
	}
	if _, err := ApplyRequestFor(failed, failed.Rows[0].PeerKey); err == nil {
		t.Fatal("empty executable set produced apply")
	}
}

func TestGroupReviewRequiresExactPreviewCorrespondence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ReviewBody)
	}{
		{"other_peer", func(r *ReviewBody) { r.Rows[0].Reply = r.Rows[1].Reply }},
		{"selector_target", func(r *ReviewBody) { r.Rows[0].Reply.Target = r.Rows[1].Reply.Target }},
		{"selector_grant", func(r *ReviewBody) { r.Rows[0].Reply.GrantID = strings.Repeat("d", 32) }},
		{"selector_revision", func(r *ReviewBody) { r.Rows[0].Reply.GrantRevision++ }},
		{"selector_version", func(r *ReviewBody) { r.Rows[0].Reply.ProtocolVersion++ }},
		{"action", func(r *ReviewBody) { r.Rows[0].Reply.Action = resourcegrant.Inspect }},
		{"requested", func(r *ReviewBody) { r.Rows[0].Reply.Preview.Requested.TransferConcurrentFiles = capacity.Limited(8) }},
		{"operation", func(r *ReviewBody) { r.Rows[0].Reply.Preview.OperationID = "token" }},
		{"base", func(r *ReviewBody) { r.Rows[0].Reply.Preview.BaseRevision = "token" }},
		{"review", func(r *ReviewBody) { r.Rows[0].Reply.Preview.ReviewRevision = "token" }},
		{"effective", func(r *ReviewBody) { r.Rows[0].Reply.Preview.Effective.TransferConcurrentFiles = 0 }},
		{"missing_reply", func(r *ReviewBody) { r.Rows[0].Reply = nil }},
		{"extra_reply_arm", func(r *ReviewBody) {
			r.Rows[0].Reply.Unavailable = &resourcegrant.ManagementStatusRequest{OperationID: strings.Repeat("a", 64)}
		}},
		{"failure_with_reply", func(r *ReviewBody) { r.Rows[0].State = ReviewUnavailable }},
		{"remote_conflict", func(r *ReviewBody) { r.Rows[0].State = "conflict" }},
		{"remote_offline", func(r *ReviewBody) { r.Rows[0].State = "offline" }},
		{"row_version", func(r *ReviewBody) { r.Rows[0].SchemaVersion++ }},
		{"duplicate_row", func(r *ReviewBody) { r.Rows[1] = r.Rows[0] }},
		{"unselected_row", func(r *ReviewBody) { r.Rows[0].PeerKey = strings.Repeat("e", 64) }},
		{"unselected_execution", func(r *ReviewBody) { r.ExecutionPeers[0] = strings.Repeat("e", 64) }},
		{"duplicate_execution", func(r *ReviewBody) { r.ExecutionPeers[1] = r.ExecutionPeers[0] }},
		{"implicit_subset", func(r *ReviewBody) { r.ExecutionPeers = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := groupReviewFixture(t, 2)
			tc.change(&r)
			if got, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers); err == nil || !reflect.DeepEqual(got, ReviewBody{}) {
				t.Fatal("unmatched or ambiguous review accepted")
			}
		})
	}
}

func TestGroupReviewRevisionBindsTokensValuesAndIntent(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ReviewBody)
	}{
		{"operation", func(r *ReviewBody) { r.Rows[0].Reply.Preview.OperationID = strings.Repeat("d", 64) }},
		{"base", func(r *ReviewBody) { r.Rows[0].Reply.Preview.BaseRevision = strings.Repeat("d", 64) }},
		{"review", func(r *ReviewBody) { r.Rows[0].Reply.Preview.ReviewRevision = strings.Repeat("d", 64) }},
		{"effective_files", func(r *ReviewBody) { r.Rows[0].Reply.Preview.Effective.TransferConcurrentFiles++ }},
		{"effective_peer", func(r *ReviewBody) { r.Rows[0].Reply.Preview.Effective.TransferConcurrentPerPeer++ }},
		{"grant", func(r *ReviewBody) {
			r.Selection.Members[0].Selector.GrantID = strings.Repeat("d", 32)
			r.Rows[0].Reply.GrantID = strings.Repeat("d", 32)
		}},
		{"grant_revision", func(r *ReviewBody) { r.Selection.Members[0].Selector.GrantRevision++; r.Rows[0].Reply.GrantRevision++ }},
		{"target", func(r *ReviewBody) {
			r.Selection.Members[0].Selector.Target.ResourceID = strings.Repeat("d", 32)
			r.Rows[0].Reply.Target.ResourceID = strings.Repeat("d", 32)
		}},
		{"peer", func(r *ReviewBody) {
			key := strings.Repeat("d", 64)
			r.Selection.Members[0].PeerKey = key
			r.Rows[0].PeerKey = key
			r.ExecutionPeers[0] = key
		}},
		{"equal_override", func(r *ReviewBody) {
			c := capacity.Limited(3)
			r.Selection.Members[0].Override = &Override{TransferConcurrentPerPeer: &c}
		}},
		{"subset", func(r *ReviewBody) { r.ExecutionPeers = r.ExecutionPeers[:1] }},
		{"failure", func(r *ReviewBody) {
			r.Rows[1].State, r.Rows[1].Reply = ReviewUnavailable, nil
			r.ExecutionPeers = r.ExecutionPeers[:1]
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := groupReviewFixture(t, 2)
			before := r.Revision
			tc.change(&r)
			revision, err := ReviewRevision(r)
			if err != nil || revision == before || r.Validate() == nil {
				t.Fatal("meaningful edit failed to invalidate frozen revision")
			}
			if _, err := ApplyRequestFor(r, r.ExecutionPeers[0]); err == nil {
				t.Fatal("changed body accepted under old revision")
			}
		})
	}
	r := groupReviewFixture(t, 2)
	r.Rows[1].State, r.Rows[1].Reply = ReviewUnavailable, nil
	a, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers[:1])
	if err != nil {
		t.Fatal(err)
	}
	r.Rows[1].State = ReviewUnsupported
	b, err := BuildReview(r.Selection, r.Rows, r.ExecutionPeers[:1])
	if err != nil || a.Revision == b.Revision {
		t.Fatal("failure classification not bound")
	}
}

func TestGroupApplyCopiesExactOpaqueTokensWithoutAuthority(t *testing.T) {
	r := groupReviewFixture(t, 1)
	request, err := ApplyRequestFor(r, r.Rows[0].PeerKey)
	if err != nil || request.Validate() != nil || request.ManagementSelector != r.Rows[0].Reply.ManagementSelector {
		t.Fatal("invalid apply projection")
	}
	p := r.Rows[0].Reply.Preview
	if request.Apply.OperationID != p.OperationID || request.Apply.BaseRevision != p.BaseRevision || request.Apply.ReviewRevision != p.ReviewRevision || request.Apply.ReviewRevision == r.Revision || !equalSettings(request.Apply.Settings, p.Requested) {
		t.Fatal("preview tokens or exact requested settings changed")
	}
	data := string(groupJSON(t, request))
	for _, field := range []string{"confirm", "reviewId", "origin", "pairBinding", "command", "authority"} {
		if strings.Contains(data, `"`+field+`"`) {
			t.Fatalf("authority field %s appeared", field)
		}
	}
	*request.Apply.Settings.TransferConcurrentPerPeer.Value = 91
	if *p.Requested.TransferConcurrentPerPeer.Value != 3 || r.Validate() != nil {
		t.Fatal("emitted request aliases stored review")
	}
}

func TestGroupReviewCopiesInputAndOutputPointers(t *testing.T) {
	s, err := ResolveSelection(groupSelectionFixture(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	rows := groupRowsFixture(s)
	peers := []string{rows[0].PeerKey, rows[1].PeerKey}
	r, err := BuildReview(s, rows, peers)
	if err != nil {
		t.Fatal(err)
	}
	before := string(groupJSON(t, r))
	*rows[0].Reply.Preview.Requested.TransferConcurrentPerPeer.Value = 29
	*s.Template.Settings.TransferConcurrentPerPeer.Value = 31
	s.Members[0].PeerKey = strings.Repeat("e", 64)
	peers[0] = strings.Repeat("f", 64)
	if string(groupJSON(t, r)) != before || r.Validate() != nil {
		t.Fatal("caller mutation changed frozen review")
	}
	copy, err := CloneReview(r)
	if err != nil {
		t.Fatal(err)
	}
	*copy.Rows[0].Reply.Preview.Requested.TransferConcurrentPerPeer.Value = 41
	copy.ExecutionPeers[0] = strings.Repeat("e", 64)
	if string(groupJSON(t, r)) != before {
		t.Fatal("presentation copy changed stored review")
	}
}
