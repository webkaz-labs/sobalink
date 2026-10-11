package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTransferRetrySelectionCLI(t *testing.T) {
	process := strings.Repeat("a", 64)
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		legacy, err := transferRetryPayload([]string{"batch"}, ja, &out)
		if err != nil || legacy["transferId"] != "batch" || legacy["expected"] != nil {
			t.Fatalf("legacy changed: %v %v", legacy, err)
		}
		selected, err := transferRetryPayload([]string{"batch", "--peer", "peer", "--process", process, "--direction", "incoming"}, ja, &out)
		if err != nil {
			t.Fatal(err)
		}
		expected := selected["expected"].(map[string]string)
		if expected["processId"] != process || expected["peerId"] != "peer" || expected["direction"] != "incoming" {
			t.Fatal("selection changed")
		}
		for _, args := range [][]string{{"batch", "--peer", "peer"}, {"batch", "--peer", "", "--process", "", "--direction", ""}, {"batch", "--peer", "peer", "--process", "123", "--direction", "incoming"}, {"batch", "--peer", "peer", "--process", process, "--direction", "unknown"}, {"batch", "extra"}} {
			if _, err := transferRetryPayload(args, ja, &out); err == nil {
				t.Fatalf("incomplete selection accepted %v", args)
			}
		}
	}
}

func TestTransferActivityCLIGroupingAndStableJSON(t *testing.T) {
	process := strings.Repeat("a", 64)
	query := func(v any) error {
		return json.Unmarshal([]byte(`{"resourceCatalogProcessId":"`+process+`","peers":[{"id":"peer","name":"example"}],"transfers":[{"id":"saved","peerId":"peer","direction":"incoming","status":"completed","totalBytes":9007199254740993},{"id":"failed","peerId":"peer","direction":"outgoing","status":"failed","retryable":true},{"id":"active","peerId":"peer","direction":"incoming","status":"transferring"},{"id":"cancelled","peerId":"peer","direction":"outgoing","status":"cancelled"},{"id":"elsewhere","peerId":"other","direction":"incoming","status":"failed","retryable":true}]}`), v)
	}
	var machine []byte
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := transferActivityCommand([]string{"--peer", "peer", "--json"}, "", ja, &out, query); err != nil {
			t.Fatal(err)
		}
		if machine == nil {
			machine = append([]byte{}, out.Bytes()...)
		} else if !bytes.Equal(machine, out.Bytes()) {
			t.Fatal("machine JSON localized")
		}
		var result struct {
			Groups []transferActivityGroup `json:"groups"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Groups) != 1 {
			t.Fatal("peer filter changed")
		}
		g := result.Groups[0]
		if g.Completed != 1 || g.Failed != 1 || g.InProgress != 1 || g.Cancelled != 1 || len(g.Transfers) != 4 || !bytes.Contains(out.Bytes(), []byte("9007199254740993")) {
			t.Fatal("counts or raw JSON changed")
		}
		out.Reset()
		if err := transferActivityCommand([]string{"--peer", "peer"}, "", ja, &out, query); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--process "+process) || strings.Contains(out.String(), "retry saved") || strings.Contains(out.String(), "retry cancelled") || strings.Contains(out.String(), "elsewhere") {
			t.Fatal("human retry target changed")
		}
	}
}
