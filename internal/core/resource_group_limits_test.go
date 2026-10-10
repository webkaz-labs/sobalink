package core

import (
	"encoding/json"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

func TestGroupLocalEnvelopesFitMaximumModelWithoutGlobalExpansion(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, resourcegroup.MaxMembers)
	worst, err := resourceGroupWorstRecord(run)
	if err != nil {
		t.Fatal(err)
	}
	view, err := resourceGroupRunView(worst, resourcegroup.ActivityRefreshing)
	if err != nil {
		t.Fatal(err)
	}
	if !resourceGroupResponseFits(view, control.DefaultLimits(), 128) {
		t.Fatal("maximum model cannot fit existing IPC/Web response envelopes")
	}
	selection := resourcegroup.Selection{SchemaVersion: resourcegroup.SchemaVersion, Template: run.Review.Selection.Template, Members: make([]resourcegroup.Member, len(run.Review.Selection.Members))}
	for i, member := range run.Review.Selection.Members {
		selection.Members[i] = resourcegroup.Member{PeerKey: member.PeerKey, Selector: member.Selector, Override: member.Override}
	}
	input := resourcegroup.PreviewInput{SchemaVersion: resourcegroup.SchemaVersion, Selection: selection}
	data, err := json.Marshal(input)
	if err != nil || !resourceGroupRequestFits(resourcegroup.LocalPreviewCommand, data, control.DefaultLimits()) {
		t.Fatal("maximum preview cannot fit existing escaped command envelopes")
	}
}

func TestGroupLocalResponseChecksBothOuterWrappersAndPolicy(t *testing.T) {
	run := groupStoreRecordFixture(t, 1, 1)
	view, err := resourceGroupRunView(run, resourcegroup.ActivityIdle)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(view)
	ipc, _ := json.Marshal(control.Response{Data: data})
	web, _ := json.Marshal(struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{true, data})
	limits := control.DefaultLimits()
	limits.ResponseBytes = int64(max(len(ipc), len(web)) + 1)
	if !resourceGroupResponseFits(view, limits, 0) {
		t.Fatal("exact complete response boundary refused")
	}
	limits.ResponseBytes--
	if resourceGroupResponseFits(view, limits, 0) {
		t.Fatal("outer response overflow accepted")
	}
	limits = control.DefaultLimits()
	limits.CommandBytes = 1
	if resourceGroupRequestFits(resourcegroup.LocalStatusCommand, []byte(`{"schemaVersion":1}`), limits) {
		t.Fatal("selected command limit bypassed")
	}
	limits = control.DefaultLimits()
	limits.RequestBytes = 1
	if resourceGroupRequestFits(resourcegroup.LocalStatusCommand, []byte(`{"schemaVersion":1}`), limits) {
		t.Fatal("escaped IPC request limit bypassed")
	}
	if resourceGroupResponseFits(view, control.Limits{}, 0) || resourceGroupResponseFits(view, control.DefaultLimits(), -1) {
		t.Fatal("invalid finite limits or reserve accepted")
	}
}
