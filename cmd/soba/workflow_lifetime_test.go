package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
)

func TestWorkflowOwnerAndRuntimeLifetimeAreExplicit(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	if err := w.services([]string{"start", "saved-a", "--owner", "fixture-owner", "--ttl", "72h"}); err != nil {
		t.Fatal(err)
	}
	history := m.history()
	start := history[len(history)-1]
	if start.payload["owner"] != "fixture-owner" || start.payload["ttlSeconds"] != float64(72*60*60) || start.payload["lifetime"] != "finite" {
		t.Fatal(start)
	}
	if _, ok := start.payload["leaseSeconds"]; ok {
		t.Fatal("invented renewable lease")
	}
	if err := w.services([]string{"wait", "saved-a", "--owner", "fixture-owner"}); err != nil {
		t.Fatal(err)
	}
	if err := w.services([]string{"stop", "saved-a", "--owner", "fixture-owner"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range m.history() {
		if call.name == "services.ready" || call.name == "services.stop" {
			if call.payload["owner"] != "fixture-owner" {
				t.Fatal(call)
			}
		}
	}
	if m.selection.Services[0].Lifetime != "until-stopped" || m.selection.Services[0].TTLSeconds != 0 {
		t.Fatal("saved definition changed")
	}
}
func TestWorkflowOverrideReviewShowsActualLifetimeWithoutChangingRevision(t *testing.T) {
	m := newWorkflowMock()
	m.selection.Services[0].Direction = "share"
	m.selection.Services[0].Lifetime = "until-revoked"
	w, out := testWorkflow(t, m)
	w.in = strings.NewReader("q\n")
	err := w.services([]string{"start", "saved-a", "--ttl", "2h"})
	if err == nil {
		t.Fatal("unconfirmed share started")
	}
	if !strings.Contains(out.String(), "7200s") || strings.Contains(out.String(), "until-revoked") {
		t.Fatal(out.String())
	}
	if m.started {
		t.Fatal("canceled mutation")
	}
	review := workflowRuntimeReview(m.selection, 2*time.Hour)
	if review.Revision != m.selection.Revision || m.selection.Services[0].Lifetime != "until-revoked" {
		t.Fatal("review changed saved data")
	}
}
func TestWorkflowRuntimeOptionsRejectInvalidValuesBeforeQuery(t *testing.T) {
	for _, args := range [][]string{{"start", "saved-a", "--ttl", "0s"}, {"start", "saved-a", "--ttl", "500ms"}, {"start", "saved-a", "--ttl", "-1s"}, {"start", "saved-a", "--owner", ""}, {"wait", "saved-a", "--owner", "invalid owner"}, {"stop", "saved-a", "--ttl", "1h"}} {
		m := newWorkflowMock()
		w, _ := testWorkflow(t, m)
		if err := w.services(args); err == nil || len(m.history()) != 0 {
			t.Fatal(args, err, m.history())
		}
	}
}
func TestTaskRuntimeLifetimePreservesOwnerCleanup(t *testing.T) {
	m := newWorkflowMock()
	w, _ := testWorkflow(t, m)
	ran := false
	w.runProcess = func(context.Context, []string, io.Reader, io.Writer) error { ran = true; return nil }
	if err := w.task([]string{"--services", "saved-a", "--ttl", "45m", "--", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("task never ran")
	}
	owner := ""
	for _, call := range m.history() {
		if call.name == "services.start" {
			if call.payload["ttlSeconds"] != float64(2700) || call.payload["leaseSeconds"] != float64(30) {
				t.Fatal(call)
			}
			owner, _ = call.payload["owner"].(string)
		}
		if call.name == "services.stop" && call.payload["owner"] != owner {
			t.Fatal("cleanup lost owner")
		}
	}
	if owner == "" {
		t.Fatal("missing task owner")
	}
}
func TestWorkflowRuntimeDryRunKeepsMachineScope(t *testing.T) {
	m := newWorkflowMock()
	w, out := testWorkflow(t, m)
	w.dryRun = true
	if err := w.group([]string{"start", "example", "--ttl", "1h", "--owner", "fixture"}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.NewDecoder(bytes.NewReader(out.Bytes())).Decode(&value); err != nil {
		t.Fatal(err)
	}
	payload := value["payload"].(map[string]any)
	if payload["ttlSeconds"] != float64(3600) || payload["owner"] != "fixture" || payload["expectedRevision"] != m.selection.Revision {
		t.Fatal(value)
	}
	if m.started {
		t.Fatal("dry run changed runtime")
	}
}

func TestGroupReplacePreservesOrExplicitlyDetachesRustDeskMetadata(t *testing.T) {
	for _, detach := range []bool{false, true} {
		m := newWorkflowMock()
		m.groups = []workflowGroup{{Name: "example", ServiceIDs: []string{"saved-a"}, RustDesk: &core.RustDeskMetadata{}}}
		w, _ := testWorkflow(t, m)
		args := []string{"save", "example", "saved-a", "--replace"}
		if detach {
			args = append(args, "--remove-rustdesk")
		}
		if err := w.group(args); err != nil {
			t.Fatal(err)
		}
		calls := m.history()
		payload := calls[len(calls)-1].payload
		group := payload["group"].(map[string]any)
		_, present := group["rustdesk"]
		if present == detach {
			t.Fatal("RustDesk metadata changed implicitly", payload)
		}
		if detach && payload["removeRustDesk"] != true {
			t.Fatal(payload)
		}
	}
}
