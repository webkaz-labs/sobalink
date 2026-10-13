//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestDispatchDiagnosticCanonicalStates(t *testing.T) {
	for _, state := range []string{"prepared", "awaiting-acceptance", "queued", "transferring", "saving", "failed", "completed", "cancelled", "declined",
		string(transfer.Pending), string(transfer.Accepted), string(transfer.Receiving), string(transfer.Partial), string(transfer.Rejected)} {
		if dispatchDiagnosticStatus(state) != state {
			t.Fatal("canonical status was lost")
		}
	}
	for phase := dispatchDiagnosticPreparing; phase <= dispatchDiagnosticCompleted; phase++ {
		if phase.String() == "other" {
			t.Fatal("canonical fixture phase was lost")
		}
	}
	// Exercise the actual outgoing reason projection without starting Core,
	// processes or transport, then preserve its closed values in the diagnostic.
	c := &Core{ctx: context.Background()}
	for _, tc := range []struct {
		state, spool, reason string
		running, approved    bool
	}{
		{"queued", "synthetic-spool", "not-failed", false, true},
		{"failed", "synthetic-spool", "busy", true, true},
		{"failed", "", "unavailable", false, true},
		{"failed", "synthetic-spool", "peer-unavailable", false, false},
		{"failed", "synthetic-spool", "", false, true},
	} {
		reason := c.outgoingRetryBlocked(&outgoingBatch{State: tc.state, Spool: tc.spool, running: tc.running}, Trust{}, tc.approved)
		if reason != tc.reason || dispatchDiagnosticReason(reason) != tc.reason {
			t.Fatal("actual retry-block reason was lost")
		}
	}
	var snapshot dispatchProcessSnapshot
	if err := json.Unmarshal([]byte(`{"transfers":[{"id":"synthetic-first","status":"completed","retryBlockedReason":"not-failed","completedBytes":52,"totalBytes":52},{"id":"synthetic-second","status":"failed","retryBlockedReason":"busy","retryable":false,"completedBytes":26,"totalBytes":52}]}`), &snapshot); err != nil {
		t.Fatal("synthetic diagnostic snapshot did not decode")
	}
	row := dispatchDiagnosticSelect(snapshot.Transfers, "synthetic-second")
	if row.matches != 1 || row.status != "failed" || row.reason != "busy" || row.retryable || row.completedBytes != 26 || row.totalBytes != 52 {
		t.Fatal("canonical selected row was lost")
	}
	var observations dispatchDiagnosticObservations
	observations.observe(nil, "synthetic-second")
	for _, tc := range []struct {
		status, reason string
		retryable      bool
	}{
		{"transferring", "not-failed", false}, {"failed", "busy", false},
		{"failed", "unavailable", false}, {"failed", "peer-unavailable", false}, {"failed", "", true},
	} {
		sample := snapshot.Transfers[1]
		sample.Status, sample.RetryBlockedReason, sample.Retryable = tc.status, tc.reason, tc.retryable
		observations.observe([]dispatchProcessTransferObservation{sample}, sample.ID)
	}
	observations.observe(nil, "synthetic-second")
	if observations.samples != 7 || observations.absent != 2 || observations.nonfailed != 1 || observations.failedBusy != 1 || observations.failedBlocked != 2 || observations.failedRetryable != 1 || observations.last.matches != 0 || observations.lastPresent.status != "failed" || !observations.lastPresent.retryable {
		t.Fatal("diagnostic samples did not distinguish state, eligibility and row disappearance")
	}
	diagnostic := dispatchProcessDiagnostic{phase: dispatchDiagnosticOffers, childIDs: [2]string{"synthetic-first", "synthetic-second"}}
	diagnostic.capture(2, snapshot)
	diagnostic.phase = dispatchDiagnosticSecondAccepted
	diagnostic.capture(0, snapshot)
	diagnostic.accept(1, transfer.Batch{State: transfer.Accepted, TotalBytes: 52, Files: []transfer.FileStatus{{State: transfer.FileSaved}, {State: transfer.FilePending}}})
	lines := diagnostic.failureLines(0, "synthetic-second", "failed", observations)
	if len(lines) != 6 || !strings.Contains(lines[0], "role=sender phase=second-accepted") || !strings.Contains(lines[3], "source=historical-sender-sibling seen=true phase=second-accepted matches=1 status=\"completed\"") || !strings.Contains(lines[4], "source=historical-receiver-snapshot seen=true phase=offers") || !strings.Contains(lines[5], "source=historical-accept-receipt seen=true phase=second-accepted state=\"accepted\"") || !strings.Contains(lines[5], "files=2 savedFiles=1") {
		t.Fatal("cached evidence lost its role, historical phase or independent sibling projection")
	}
}

func TestDispatchDiagnosticRejectsFreeFormValues(t *testing.T) {
	for _, value := range []string{"/synthetic/private-path", `C:\synthetic\private-path`, "synthetic-key-and-identity", strings.Repeat("a", 64), "synthetic payload", "failed\nsynthetic payload", "busy synthetic payload", "Failed", " failed ", "failed\x00", "completed ", "\x1b[31mfailed"} {
		if dispatchDiagnosticStatus(value) != "other" || dispatchDiagnosticReason(value) != "other" {
			t.Fatal("noncanonical diagnostic token was retained")
		}
		var snapshot dispatchProcessSnapshot
		raw, err := json.Marshal(map[string]any{"resourceCatalogProcessId": value, "transfers": []any{map[string]any{
			"id": value, "peerId": value, "dispatchId": value, "direction": value, "status": value, "retryBlockedReason": value,
			"error": value, "name": value, "entries": []any{map[string]any{"path": value, "storedPath": value, "error": value}},
			"retryable": false, "completedBytes": int64(math.MaxInt64), "totalBytes": int64(-1),
		}}})
		if err != nil || json.Unmarshal(raw, &snapshot) != nil {
			t.Fatal("synthetic free-form snapshot did not decode")
		}
		diagnostic := dispatchProcessDiagnostic{phase: 255, childIDs: [2]string{"synthetic-sibling", value}}
		diagnostic.capture(0, snapshot)
		diagnostic.capture(2, snapshot)
		diagnostic.accept(1, transfer.Batch{ID: value, State: transfer.BatchState(value), Destination: value, Peer: transfer.Peer{ID: value},
			CompletedBytes: math.MaxInt64, TotalBytes: -1, Files: []transfer.FileStatus{{Entry: transfer.Entry{ID: value, Path: value, SHA256: value}, State: transfer.FileState(value), StoredName: value, Error: value}}})
		var observations dispatchDiagnosticObservations
		observations.observe(snapshot.Transfers, value)
		lines := diagnostic.failureLines(-1, value, value, observations)
		text := strings.Join(lines, "\n")
		if len(lines) != 6 || len(text) > 3000 || strings.Contains(text, value) || strings.Contains(text, "9223372036854775807") || strings.Contains(text, "Bytes=-1") || !strings.Contains(text, "role=other phase=other expected=\"other\"") || !strings.Contains(text, "status=\"other\" retryBlockedReason=\"other\"") || !strings.Contains(text, "completedBytes=0 completedBytesValid=false totalBytes=0 totalBytesValid=false") {
			t.Fatal("free-form or malformed diagnostic data escaped its bounded projection")
		}
	}
	for _, malformed := range []string{`{"transfers":[{"status":{}}]}`, `{"transfers":[{"retryBlockedReason":123}]}`, `{"transfers":[{"completedBytes":"synthetic-private-value"}]}`, `{"transfers":[{"totalBytes":9223372036854775808}]}`} {
		var snapshot dispatchProcessSnapshot
		if json.Unmarshal([]byte(malformed), &snapshot) == nil {
			t.Fatal("malformed diagnostic DTO field was silently accepted")
		}
	}
	if dispatchDiagnosticStatus("") != "other" || dispatchDiagnosticReason("") != "" {
		t.Fatal("missing status and canonical unblocked reason were conflated")
	}
}

func TestDispatchDiagnosticObservationsBounded(t *testing.T) {
	for _, value := range []int{-1, 0, 1, dispatchDiagnosticCountLimit, math.MaxInt} {
		if got := dispatchDiagnosticCount(value); got < 0 || got > dispatchDiagnosticCountLimit {
			t.Fatal("diagnostic count was unbounded")
		}
	}
	for _, value := range []int64{math.MinInt64, -1, dispatchDiagnosticByteLimit + 1, math.MaxInt64} {
		if got, valid := dispatchDiagnosticBytes(value); got != 0 || valid {
			t.Fatal("invalid byte count was emitted as an observation")
		}
	}
	for _, value := range []int64{0, 26, 52, dispatchDiagnosticByteLimit} {
		if got, valid := dispatchDiagnosticBytes(value); got != value || !valid {
			t.Fatal("valid bounded byte count was lost")
		}
	}
	for _, unseen := range []string{(dispatchDiagnosticRow{}).String(), (dispatchDiagnosticReceipt{}).String()} {
		if !strings.Contains(unseen, "completedBytesValid=false") || !strings.Contains(unseen, "totalBytesValid=false") {
			t.Fatal("missing evidence was labeled as valid observed bytes")
		}
	}
	var observations dispatchDiagnosticObservations
	for range dispatchDiagnosticCountLimit + 20 {
		observations.observe(nil, "synthetic-selected")
	}
	if observations.samples != dispatchDiagnosticCountLimit || observations.absent != dispatchDiagnosticCountLimit {
		t.Fatal("poll counters did not saturate")
	}
	rows := make([]dispatchProcessTransferObservation, dispatchDiagnosticCountLimit+20)
	for i := range rows {
		rows[i].ID, rows[i].Status = "synthetic-selected", "failed"
		rows[i].RetryBlockedReason = "busy"
	}
	if selected := dispatchDiagnosticSelect(rows, "synthetic-selected"); selected.matches != dispatchDiagnosticCountLimit || selected.status != "failed" || selected.reason != "busy" {
		t.Fatal("matching rows were not bounded")
	}
	if dispatchDiagnosticSelect(rows, "").matches != 0 || dispatchDiagnosticSelect(rows, "synthetic-other").matches != 0 {
		t.Fatal("absent selection matched unrelated rows")
	}
	diagnostic := dispatchProcessDiagnostic{childIDs: [2]string{"synthetic-selected", "synthetic-other"}}
	diagnostic.capture(-1, dispatchProcessSnapshot{Transfers: rows})
	diagnostic.capture(3, dispatchProcessSnapshot{Transfers: rows})
	diagnostic.accept(-1, transfer.Batch{State: transfer.Accepted})
	diagnostic.accept(2, transfer.Batch{State: transfer.Accepted})
	if diagnostic.cached[0].seen || diagnostic.accepted[0].seen || len(diagnostic.failureLines(0, "synthetic-unselected", "failed", observations)) != 3 {
		t.Fatal("invalid role or selection created a synthetic observation")
	}
}
