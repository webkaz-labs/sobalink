package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func resourceGroupCLITestSelection(t *testing.T, n int) resourcegroup.Selection {
	t.Helper()
	template, err := resourcegroup.NewTemplate(resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(2)})
	if err != nil {
		t.Fatal(err)
	}
	s := resourcegroup.Selection{SchemaVersion: 1, Template: template, Members: []resourcegroup.Member{}}
	for i := 0; i < n; i++ {
		s.Members = append(s.Members, resourcegroup.Member{PeerKey: fmt.Sprintf("%064x", i+1), Selector: resourcegrant.ManagementSelector{ProtocolVersion: 2, Target: resource.Target{SchemaVersion: 1, ResourceID: fmt.Sprintf("%032x", i+100)}, GrantID: fmt.Sprintf("%032x", i+200), GrantRevision: 1}})
	}
	return s
}
func resourceGroupCLITestPrepared(t *testing.T, s resourcegroup.Selection) resourcegroup.PreparedView {
	t.Helper()
	resolved, err := resourcegroup.ResolveSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	rows := []resourcegroup.ReviewRow{}
	peers := []string{}
	for _, member := range resolved.Members {
		rows = append(rows, resourcegroup.ReviewRow{SchemaVersion: 1, PeerKey: member.PeerKey, State: resourcegroup.ReviewReady, Reply: &resourcegrant.ManagementReply{ManagementSelector: member.Selector, Action: resourcegrant.PreviewAction, Preview: &resourcegrant.ManagementPreview{OperationID: strings.Repeat("a", 64), BaseRevision: strings.Repeat("b", 64), ReviewRevision: strings.Repeat("c", 64), Requested: member.Requested, Effective: resource.Effective{TransferConcurrentFiles: 8, TransferConcurrentPerPeer: 2}}}})
		peers = append(peers, member.PeerKey)
	}
	review, err := resourcegroup.BuildReview(resolved, rows, peers)
	if err != nil {
		t.Fatal(err)
	}
	first := true
	return resourcegroup.PreparedView{SchemaVersion: 1, ReviewID: strings.Repeat("d", 32), Review: review, AdmissionState: resourcegroup.AdmissionPrepared, InitializesLocalEvidence: &first}
}
func resourceGroupCLITestRun(t *testing.T, view resourcegroup.PreparedView) resourcegroup.RunView {
	t.Helper()
	rows, err := resourcegroup.NewEvidence(view.Review)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].LocalDurability = resourcegroup.LocalDurable
		rows[i].AdmissionStop = resourcegroup.StopUserCanceled
	}
	summary, err := resourcegroup.ReduceReview(view.Review, rows)
	if err != nil {
		t.Fatal(err)
	}
	return resourcegroup.RunView{SchemaVersion: 1, RunID: view.ReviewID, AcceptedAt: 1735689600, Review: view.Review, Evidence: resourcegroup.EvidenceBody{SchemaVersion: 1, Members: rows}, Summary: summary, LocalDurability: resourcegroup.LocalDurable, Activity: resourcegroup.ActivityIdle}
}
func resourceCollectionTestJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func resourceGroupCLITestArgs(t *testing.T, n int) []string {
	s := resourceGroupCLITestSelection(t, n)
	args := []string{"preview", "--concurrent-files", "default", "--concurrent-per-peer", "2"}
	for i := len(s.Members) - 1; i >= 0; i-- {
		m := s.Members[i]
		args = append(args, "--member", fmt.Sprintf("%s,%s,%s,%d", m.PeerKey, m.Selector.Target.ResourceID, m.Selector.GrantID, m.Selector.GrantRevision))
	}
	return args
}

func TestResourceGroupCLIPreviewOneCoordinatorCallAndCanonicalSubset(t *testing.T) {
	for _, ja := range []bool{false, true} {
		calls := 0
		var out bytes.Buffer
		expected := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 2))
		client := func(_ context.Context, dir, raw string, target any) error {
			calls++
			if dir != "synthetic-profile" {
				t.Fatal("profile changed")
			}
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != resourcegroup.LocalPreviewCommand {
				t.Fatal("not one group command")
			}
			input, err := resourcegroup.DecodePreviewInput(cmd.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if input.Selection.Members[0].PeerKey >= input.Selection.Members[1].PeerKey {
				t.Fatal("not canonical")
			}
			return json.Unmarshal(resourceCollectionTestJSON(t, expected), target)
		}
		if err := resourceGroupCLI(t.Context(), append(resourceGroupCLITestArgs(t, 2), "--json"), "synthetic-profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal(calls)
		}
		got, err := resourcegroup.DecodePreparedView(out.Bytes())
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatal("machine envelope changed", err)
		}
	}
}
func TestResourceGroupCLIDryRunEveryActionHasZeroCalls(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("dry run contacted agent"); return nil }
	view := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	peer := view.Review.ExecutionPeers[0]
	cases := [][]string{
		resourceGroupCLITestArgs(t, 1),
		{"select", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer},
		{"select", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--exclude-all"},
		{"apply", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer, "--confirm"},
		{"status", "--run-id", view.ReviewID},
		{"refresh", "--run-id", view.ReviewID, "--peer", peer},
		{"cancel", "--review-id", view.ReviewID},
	}
	for _, ja := range []bool{false, true} {
		for _, args := range cases {
			var out bytes.Buffer
			if err := resourceGroupCLI(t.Context(), args, "unused", ja, true, &out, noCall); err != nil {
				t.Fatal(args, err)
			}
			if !strings.Contains(out.String(), `"validation": "local-input-only"`) || !strings.Contains(out.String(), `"applied": false`) {
				t.Fatal(out.String())
			}
		}
	}
}
func TestResourceGroupCLIRejectsAmbiguousInputsBeforeCalls(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("invalid input contacted agent"); return nil }
	view := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	peer := view.Review.ExecutionPeers[0]
	cases := [][]string{
		{"apply", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer},
		{"apply", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--confirm"},
		{"select", "--review-id", view.ReviewID, "--revision", view.Review.Revision},
		{"select", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer, "--exclude-all"},
		{"select", "--review-file", "missing", "--review-id", view.ReviewID, "--execute-peer", peer},
		{"select", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer, "--execute-peer", peer},
		{"refresh", "--run-id", view.ReviewID},
		{"refresh", "--run-id", view.ReviewID, "--peer", "*"},
		{"status", "--run-id", view.ReviewID, "--peer", peer},
		{"preview", "--selection-file", "missing", "--concurrent-files", "default"},
		{"preview", "--command", "resource.remote.management.apply"},
		append(resourceGroupCLITestArgs(t, 1), "--offline"),
	}
	duplicate := resourceGroupCLITestArgs(t, 1)
	duplicate = append(duplicate, "--member", duplicate[len(duplicate)-1])
	cases = append(cases, duplicate)
	for _, args := range cases {
		if err := resourceGroupCLI(t.Context(), args, "unused", false, false, io.Discard, noCall); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestResourceGroupCLISelectShowsNewFullReviewAndExactSubset(t *testing.T) {
	old := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 2))
	next := old
	next.ReviewID = strings.Repeat("e", 32)
	var err error
	next.Review, err = resourcegroup.BuildReview(old.Review.Selection, old.Review.Rows, old.Review.ExecutionPeers[:1])
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"select", "--review-id", old.ReviewID, "--revision", old.Review.Revision, "--execute-peer", old.Review.ExecutionPeers[0]}
	calls := 0
	var out bytes.Buffer
	client := func(_ context.Context, _ string, raw string, target any) error {
		calls++
		var cmd webui.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != resourcegroup.LocalSelectCommand {
			t.Fatal("wrong command")
		}
		input, err := resourcegroup.DecodeSelectInput(cmd.Payload)
		if err != nil || input.ReviewID != old.ReviewID || input.ReviewRevision != old.Review.Revision || !resourceGroupSamePeers(input.ExecutionPeers, next.Review.ExecutionPeers) {
			t.Fatal("wrong subset")
		}
		return json.Unmarshal(resourceCollectionTestJSON(t, next), target)
	}
	if err := resourceGroupCLI(t.Context(), args, "/synthetic/profile path", false, false, &out, client); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), next.ReviewID) || !strings.Contains(out.String(), next.Review.Revision) || !strings.Contains(out.String(), old.Review.ExecutionPeers[1]) || !strings.Contains(out.String(), "excluded") || !strings.Contains(out.String(), `argv: ["soba","--state-dir","/synthetic/profile path"`) {
		t.Fatal(out.String())
	}
	// An unchanged subset retains both the original ID and content digest.
	next.Review = old.Review
	next.ReviewID = old.ReviewID
	args = append(args, "--execute-peer", old.Review.ExecutionPeers[1])
	if err := resourceGroupCLI(t.Context(), args, "unused", false, false, io.Discard, client); err != nil {
		t.Fatal(err)
	}
}
func TestResourceGroupCLIBoundedDataFilesAndOptionalCorrespondence(t *testing.T) {
	dir := t.TempDir()
	selection := resourceGroupCLITestSelection(t, 1)
	view := resourceGroupCLITestPrepared(t, selection)
	path := filepath.Join(dir, "selection.json")
	if err := os.WriteFile(path, resourceCollectionTestJSON(t, selection), 0600); err != nil {
		t.Fatal(err)
	}
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("data read dry-run contacted agent")
		return nil
	}
	if err := resourceGroupCLI(t.Context(), []string{"preview", "--selection-file", path}, "unused", false, true, io.Discard, noCall); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte(strings.Repeat(" ", resourcegroup.MaxSelectionBytes+1)), []byte(`{"command":"resource.group.apply"}`), append(resourceCollectionTestJSON(t, selection), []byte(`{}`)...)} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := resourceGroupCLI(t.Context(), []string{"preview", "--selection-file", path}, "unused", false, true, io.Discard, noCall); err == nil {
			t.Fatal("bad selection accepted")
		}
	}
	if _, err := resourceGroupReadFile(t.Context(), dir, resourcegroup.MaxSelectionBytes); err == nil {
		t.Fatal("directory read")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err == nil {
		if _, err := resourceGroupReadFile(t.Context(), link, resourcegroup.MaxSelectionBytes); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	reviewPath := filepath.Join(dir, "review.json")
	if err := os.WriteFile(reviewPath, resourceCollectionTestJSON(t, view), 0600); err != nil {
		t.Fatal(err)
	}
	next := view
	client := func(_ context.Context, _ string, _ string, target any) error {
		return json.Unmarshal(resourceCollectionTestJSON(t, next), target)
	}
	args := []string{"select", "--review-file", reviewPath, "--execute-peer", view.Review.ExecutionPeers[0]}
	if err := resourceGroupCLI(t.Context(), args, "unused", false, false, io.Discard, client); err != nil {
		t.Fatal(err)
	}
	altered := resourceGroupCLITestSelection(t, 1)
	altered.Members[0].Selector.GrantRevision = 2
	next = resourceGroupCLITestPrepared(t, altered)
	next.ReviewID = strings.Repeat("e", 32)
	if err := resourceGroupCLI(t.Context(), args, "unused", false, false, io.Discard, client); err == nil {
		t.Fatal("changed recorded selection accepted")
	}
}
func TestResourceGroupCLIStatusUnknownBarrierAndLocalOnlyCommand(t *testing.T) {
	view := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	run := resourceGroupCLITestRun(t, view)
	row := &run.Evidence.Members[0]
	row.Dispatch = resourcegroup.DispatchUnknown
	row.Status = resourcegroup.StatusObservation{State: resourcegroup.StatusUnavailable, Sequence: 1, ObservedAt: 1735689601}
	var err error
	run.Summary, err = resourcegroup.ReduceReview(run.Review, run.Evidence.Members)
	if err != nil {
		t.Fatal(err)
	}
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		calls := 0
		client := func(_ context.Context, _ string, raw string, target any) error {
			calls++
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != resourcegroup.LocalStatusCommand {
				t.Fatal("status dispatched network operation")
			}
			return json.Unmarshal(resourceCollectionTestJSON(t, run), target)
		}
		if err := resourceGroupCLI(t.Context(), []string{"status", "--run-id", run.RunID}, "synthetic-profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(out.String(), text(ja, "remains a barrier", "遮断")) || !strings.Contains(out.String(), text(ja, "unavailable query", "取得不能")) || !strings.Contains(out.String(), "--run-id "+run.RunID) {
			t.Fatal(out.String())
		}
	}
}
func TestResourceGroupCLIRejectsMismatchedAndMalformedReplies(t *testing.T) {
	view := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	run := resourceGroupCLITestRun(t, view)
	for _, reply := range [][]byte{[]byte(`{}`), append(resourceCollectionTestJSON(t, run), []byte(`{}`)...), []byte(strings.Replace(string(resourceCollectionTestJSON(t, run)), `,"allApplied":false`, "", 1)), []byte(strings.Replace(string(resourceCollectionTestJSON(t, run)), run.RunID, strings.Repeat("e", 32), 1))} {
		client := func(_ context.Context, _ string, _ string, target any) error {
			p := target.(*json.RawMessage)
			*p = append((*p)[:0], reply...)
			return nil
		}
		var out bytes.Buffer
		if err := resourceGroupCLI(t.Context(), []string{"status", "--run-id", run.RunID, "--json"}, "unused", false, false, &out, client); err == nil || out.Len() != 0 {
			t.Fatal("unmatched response exposed")
		}
	}
}
func TestResourceCollectionErrorsAreFixedAndBilingual(t *testing.T) {
	for code := range resourceCollectionErrorTexts {
		original := &control.RemoteError{Code: code, Message: "/synthetic-private/socket 192.0.2.21"}
		projected := resourceCollectionControlError(original)
		for _, ja := range []bool{false, true} {
			err := localizeResourceCollectionError(ja, projected)
			var coded interface{ ErrorCode() string }
			if !errors.As(err, &coded) || coded.ErrorCode() != code || !errors.Is(err, original) || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "192.0.2.21") {
				t.Fatal(code, err)
			}
			if (err.Error() != projected.Error()) != ja {
				t.Fatal("locale mismatch")
			}
		}
	}
	if !errors.Is(resourceCollectionControlError(fmt.Errorf("private: %w", context.Canceled)), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestResourceGroupCLIApplyRefreshCancelExactSingleRequests(t *testing.T) {
	view := resourceGroupCLITestPrepared(t, resourceGroupCLITestSelection(t, 1))
	run := resourceGroupCLITestRun(t, view)
	peer := view.Review.ExecutionPeers[0]
	canceled := view
	canceled.AdmissionState = resourcegroup.AdmissionCanceled
	cases := []struct {
		args  []string
		name  string
		reply any
	}{
		{[]string{"apply", "--review-id", view.ReviewID, "--revision", view.Review.Revision, "--execute-peer", peer, "--confirm", "--json"}, resourcegroup.LocalApplyCommand, run},
		{[]string{"refresh", "--run-id", run.RunID, "--peer", peer, "--json"}, resourcegroup.LocalRefreshCommand, run},
		{[]string{"cancel", "--review-id", view.ReviewID, "--json"}, resourcegroup.LocalCancelCommand, resourcegroup.CancelView{SchemaVersion: 1, Prepared: &canceled}},
		{[]string{"cancel", "--review-id", run.RunID, "--json"}, resourcegroup.LocalCancelCommand, resourcegroup.CancelView{SchemaVersion: 1, Run: &run}},
	}
	for _, tc := range cases {
		calls := 0
		var out bytes.Buffer
		client := func(_ context.Context, _ string, raw string, target any) error {
			calls++
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != tc.name {
				t.Fatal("unexpected command")
			}
			switch tc.name {
			case resourcegroup.LocalApplyCommand:
				in, err := resourcegroup.DecodeApplyInput(cmd.Payload)
				if err != nil || !in.Confirm || in.ReviewID != view.ReviewID || in.ReviewRevision != view.Review.Revision || !resourceGroupSamePeers(in.ExecutionPeers, view.Review.ExecutionPeers) {
					t.Fatal("apply changed")
				}
			case resourcegroup.LocalRefreshCommand:
				in, err := resourcegroup.DecodeRefreshInput(cmd.Payload)
				if err != nil || in.RunID != run.RunID || !resourceGroupSamePeers(in.Peers, []string{peer}) {
					t.Fatal("refresh changed")
				}
			case resourcegroup.LocalCancelCommand:
				in, err := resourcegroup.DecodeCancelInput(cmd.Payload)
				if err != nil || in.ReviewID != view.ReviewID {
					t.Fatal("cancel changed")
				}
			}
			return json.Unmarshal(resourceCollectionTestJSON(t, tc.reply), target)
		}
		if err := resourceGroupCLI(t.Context(), tc.args, "unused", false, false, &out, client); err != nil || calls != 1 || !json.Valid(out.Bytes()) {
			t.Fatal(err, calls, out.String())
		}
	}
}
func TestResourceGroupCLIAllFailedPreviewCannotAdvertiseApply(t *testing.T) {
	selection := resourceGroupCLITestSelection(t, 1)
	view := resourceGroupCLITestPrepared(t, selection)
	rows := []resourcegroup.ReviewRow{{SchemaVersion: 1, PeerKey: selection.Members[0].PeerKey, State: resourcegroup.ReviewUnavailable}}
	var err error
	view.Review, err = resourcegroup.BuildReview(view.Review.Selection, rows, []string{})
	if err != nil {
		t.Fatal(err)
	}
	view.AdmissionState = resourcegroup.AdmissionUnavailable
	client := func(_ context.Context, _ string, _ string, target any) error {
		return json.Unmarshal(resourceCollectionTestJSON(t, view), target)
	}
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := resourceGroupCLI(t.Context(), resourceGroupCLITestArgs(t, 1), "unused", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), selection.Members[0].PeerKey) || !strings.Contains(out.String(), text(ja, "cannot be applied", "適用できません")) || strings.Contains(out.String(), "--confirm") {
			t.Fatal(out.String())
		}
	}
}
