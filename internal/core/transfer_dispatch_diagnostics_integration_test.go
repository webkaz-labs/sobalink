//go:build lanlink_integration && directlan_integration

package core

import (
	"fmt"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// These projections use only existing fixture replies. They add no IPC, product
// hooks, locks, waits or success-path output. Cached receiver/accept observations
// are historical evidence, never a claim about current receiver or owner state.
const dispatchDiagnosticCountLimit = 4096
const dispatchDiagnosticByteLimit int64 = 1 << 20

type dispatchDiagnosticPhase uint8

const (
	dispatchDiagnosticPreparing dispatchDiagnosticPhase = iota
	dispatchDiagnosticOffers
	dispatchDiagnosticFirstAccepted
	dispatchDiagnosticFirstSaved
	dispatchDiagnosticStagedEntryRemoved
	dispatchDiagnosticSecondAccepted
	dispatchDiagnosticRetryInvoked
	dispatchDiagnosticCompleted
)

func (phase dispatchDiagnosticPhase) String() string {
	switch phase {
	case dispatchDiagnosticPreparing:
		return "preparing"
	case dispatchDiagnosticOffers:
		return "offers"
	case dispatchDiagnosticFirstAccepted:
		return "first-accepted"
	case dispatchDiagnosticFirstSaved:
		return "first-saved"
	case dispatchDiagnosticStagedEntryRemoved:
		return "staged-entry-removed"
	case dispatchDiagnosticSecondAccepted:
		return "second-accepted"
	case dispatchDiagnosticRetryInvoked:
		return "retry-invoked"
	case dispatchDiagnosticCompleted:
		return "completed"
	default:
		return "other"
	}
}

func dispatchDiagnosticStatus(value string) string {
	switch value {
	case "prepared", "awaiting-acceptance", "queued", "transferring", "saving", "failed", "completed", "cancelled", "declined",
		"pending", "accepted", "receiving", "partial", "rejected":
		return value
	default:
		return "other"
	}
}

func dispatchDiagnosticReason(value string) string {
	switch value {
	case "", "not-failed", "busy", "unavailable", "peer-unavailable":
		return value
	default:
		return "other"
	}
}

func dispatchDiagnosticCount(value int) int {
	return max(0, min(value, dispatchDiagnosticCountLimit))
}

func dispatchDiagnosticBytes(value int64) (int64, bool) {
	if value < 0 || value > dispatchDiagnosticByteLimit {
		return 0, false
	}
	return value, true
}

type dispatchDiagnosticRow struct {
	matches                    int
	status, reason             string
	retryable                  bool
	completedBytes, totalBytes int64
}

func dispatchDiagnosticSelect(rows []dispatchProcessTransferObservation, id string) dispatchDiagnosticRow {
	var selected dispatchDiagnosticRow
	for _, row := range rows {
		if id == "" || row.ID != id {
			continue
		}
		if selected.matches == 0 {
			selected.status, selected.reason = dispatchDiagnosticStatus(row.Status), dispatchDiagnosticReason(row.RetryBlockedReason)
			selected.retryable = row.Retryable
			selected.completedBytes, selected.totalBytes = row.CompletedBytes, row.TotalBytes
		}
		selected.matches = dispatchDiagnosticCount(selected.matches + 1)
	}
	return selected
}

func (row dispatchDiagnosticRow) String() string {
	completed, completedValid := dispatchDiagnosticBytes(row.completedBytes)
	total, totalValid := dispatchDiagnosticBytes(row.totalBytes)
	completedValid, totalValid = completedValid && row.matches > 0, totalValid && row.matches > 0
	return fmt.Sprintf("matches=%d status=%q retryBlockedReason=%q retryable=%t completedBytes=%d completedBytesValid=%t totalBytes=%d totalBytesValid=%t",
		dispatchDiagnosticCount(row.matches), dispatchDiagnosticStatus(row.status), dispatchDiagnosticReason(row.reason), row.retryable, completed, completedValid, total, totalValid)
}

type dispatchDiagnosticObservations struct {
	samples, absent, nonfailed, failedBusy, failedBlocked, failedRetryable int
	last, lastPresent                                                      dispatchDiagnosticRow
}

func (observations *dispatchDiagnosticObservations) observe(rows []dispatchProcessTransferObservation, id string) {
	observations.samples = dispatchDiagnosticCount(observations.samples + 1)
	observations.last = dispatchDiagnosticSelect(rows, id)
	row := observations.last
	if row.matches > 0 {
		observations.lastPresent = row
	}
	switch {
	case row.matches == 0:
		observations.absent = dispatchDiagnosticCount(observations.absent + 1)
	case row.status != "failed":
		observations.nonfailed = dispatchDiagnosticCount(observations.nonfailed + 1)
	case row.retryable:
		observations.failedRetryable = dispatchDiagnosticCount(observations.failedRetryable + 1)
	case row.reason == "busy":
		observations.failedBusy = dispatchDiagnosticCount(observations.failedBusy + 1)
	default:
		observations.failedBlocked = dispatchDiagnosticCount(observations.failedBlocked + 1)
	}
}

type dispatchDiagnosticSnapshot struct {
	seen  bool
	phase dispatchDiagnosticPhase
	rows  [2]dispatchDiagnosticRow
}

type dispatchDiagnosticReceipt struct {
	seen                       bool
	phase                      dispatchDiagnosticPhase
	state                      string
	completedBytes, totalBytes int64
	files, savedFiles          int
}

func (receipt dispatchDiagnosticReceipt) String() string {
	completed, completedValid := dispatchDiagnosticBytes(receipt.completedBytes)
	total, totalValid := dispatchDiagnosticBytes(receipt.totalBytes)
	completedValid, totalValid = completedValid && receipt.seen, totalValid && receipt.seen
	return fmt.Sprintf("seen=%t phase=%s state=%q completedBytes=%d completedBytesValid=%t totalBytes=%d totalBytesValid=%t files=%d savedFiles=%d",
		receipt.seen, receipt.phase, dispatchDiagnosticStatus(receipt.state), completed, completedValid, total, totalValid,
		dispatchDiagnosticCount(receipt.files), dispatchDiagnosticCount(receipt.savedFiles))
}

type dispatchProcessDiagnostic struct {
	phase    dispatchDiagnosticPhase
	childIDs [2]string // Selection only; never formatted.
	cached   [3]dispatchDiagnosticSnapshot
	accepted [2]dispatchDiagnosticReceipt
}

func (diagnostic *dispatchProcessDiagnostic) capture(role int, snapshot dispatchProcessSnapshot) {
	if role < 0 || role >= len(diagnostic.cached) {
		return
	}
	cached := dispatchDiagnosticSnapshot{seen: true, phase: diagnostic.phase}
	for i, id := range diagnostic.childIDs {
		cached.rows[i] = dispatchDiagnosticSelect(snapshot.Transfers, id)
	}
	diagnostic.cached[role] = cached
}

func (diagnostic *dispatchProcessDiagnostic) accept(child int, batch transfer.Batch) {
	if child < 0 || child >= len(diagnostic.accepted) {
		return
	}
	receipt := dispatchDiagnosticReceipt{seen: true, phase: diagnostic.phase, state: dispatchDiagnosticStatus(string(batch.State)),
		completedBytes: batch.CompletedBytes, totalBytes: batch.TotalBytes, files: dispatchDiagnosticCount(len(batch.Files))}
	for _, file := range batch.Files {
		if file.State == transfer.FileSaved {
			receipt.savedFiles = dispatchDiagnosticCount(receipt.savedFiles + 1)
		}
	}
	diagnostic.accepted[child] = receipt
}

func (diagnostic dispatchProcessDiagnostic) failureLines(role int, id, expected string, observations dispatchDiagnosticObservations) []string {
	roleName := "other"
	switch role {
	case 0:
		roleName = "sender"
	case 1:
		roleName = "receiver-first"
	case 2:
		roleName = "receiver-second"
	}
	lines := []string{
		fmt.Sprintf("dispatch failure role=%s phase=%s expected=%q countLimit=%d byteLimit=%d samples=%d absent=%d nonfailed=%d failedBusy=%d failedBlocked=%d failedRetryable=%d",
			roleName, diagnostic.phase, dispatchDiagnosticStatus(expected), dispatchDiagnosticCountLimit, dispatchDiagnosticByteLimit,
			dispatchDiagnosticCount(observations.samples), dispatchDiagnosticCount(observations.absent), dispatchDiagnosticCount(observations.nonfailed),
			dispatchDiagnosticCount(observations.failedBusy), dispatchDiagnosticCount(observations.failedBlocked), dispatchDiagnosticCount(observations.failedRetryable)),
		"dispatch failure source=wait-last " + observations.last.String(),
		"dispatch failure source=wait-last-present " + observations.lastPresent.String(),
	}
	for child, childID := range diagnostic.childIDs {
		if id == "" || id != childID {
			continue
		}
		sender, receiver := diagnostic.cached[0], diagnostic.cached[child+1]
		lines = append(lines,
			fmt.Sprintf("dispatch failure source=historical-sender-sibling seen=%t phase=%s %s", sender.seen, sender.phase, sender.rows[1-child]),
			fmt.Sprintf("dispatch failure source=historical-receiver-snapshot seen=%t phase=%s %s", receiver.seen, receiver.phase, receiver.rows[child]),
			"dispatch failure source=historical-accept-receipt "+diagnostic.accepted[child].String())
		break
	}
	return lines
}
