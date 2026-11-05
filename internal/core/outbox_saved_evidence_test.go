package core

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Only fixed classes and booleans may cross this boundary. Private ledger values
// are used for comparison, never as paths to open or values to print. Collection
// happens after failure and is not an atomic snapshot of ledger and filesystem.
type outboxSavedEvidence struct {
	BatchKind           string
	Ledger              string
	Record              string
	RecordRootMatches   bool
	LedgerEntry         string
	Phase               string
	AnchorPresent       bool
	Cleanup             bool
	StageCleanup        bool
	StageRead           string
	StageCount          string
	EntryKind           string
	AnchorComparison    string // Recorded name only, not certified ownership.
	SavedFileComparison string // Observed handles, not the ledger identity field.
}

type outboxEvidenceLedger struct {
	Records []outboxEvidenceRecord `json:"records"`
}
type outboxEvidenceRecord struct {
	Offer struct {
		DeliveryID string `json:"deliveryId"`
	} `json:"offer"`
	Destination  string               `json:"destination"`
	Root         string               `json:"root"`
	StageCleanup *bool                `json:"stageCleanup"`
	Files        []outboxEvidenceFile `json:"files"`
}
type outboxEvidenceFile struct {
	Entry   transfer.Entry `json:"entry"`
	Phase   *string        `json:"phase"`
	Anchor  *string        `json:"anchor"`
	Cleanup *bool          `json:"cleanup"`
}

func outboxEvidenceKind(kind transfer.Kind) string {
	switch kind {
	case transfer.File:
		return "file"
	case transfer.Directory:
		return "directory"
	default:
		return "other"
	}
}
func outboxEvidencePhase(phase string) string {
	switch phase {
	case "pending", "receiving", "publish_pending", "saved":
		return phase
	default:
		return "other"
	}
}
func outboxEvidenceMode(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case info.Mode().IsRegular():
		return "file"
	case info.IsDir():
		return "directory"
	default:
		return "other"
	}
}

// Reject links and recheck the opened identity. os.Root confines traversal but
// can follow links within its root, so confinement alone is not sufficient.
func outboxEvidenceRoot(path string) (*os.Root, string) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, outboxInventoryErrorClass(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "wrong_type"
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, outboxInventoryErrorClass(err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, "changed"
	}
	return root, "none"
}
func outboxEvidenceChild(root *os.Root, name string) (*os.Root, string) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, outboxInventoryErrorClass(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "wrong_type"
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, outboxInventoryErrorClass(err)
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, "changed"
	}
	return child, "none"
}
func outboxEvidenceFileHandle(root *os.Root, name string) (*os.File, os.FileInfo, string) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, outboxInventoryErrorClass(err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, "wrong_type"
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, outboxInventoryErrorClass(err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, "changed"
	}
	return file, opened, "none"
}

func readOutboxEvidenceLedger(profile string) (outboxEvidenceLedger, string) {
	var ledger outboxEvidenceLedger
	root, class := outboxEvidenceRoot(profile)
	if class != "none" {
		return ledger, class
	}
	defer root.Close()
	namespace, class := outboxEvidenceChild(root, "durable-v1")
	if class != "none" {
		return ledger, class
	}
	defer namespace.Close()
	file, _, class := outboxEvidenceFileHandle(namespace, "receiver.json")
	if class != "none" {
		return ledger, class
	}
	defer file.Close()
	// The two-entry integration fixture is small. Oversized evidence is omitted,
	// never used to weaken the assertion or to inspect an unbounded ledger.
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return ledger, outboxInventoryErrorClass(err)
	}
	if len(data) > limit {
		return ledger, "limit"
	}
	if json.Unmarshal(data, &ledger) != nil || ledger.Records == nil {
		return outboxEvidenceLedger{}, "invalid"
	}
	for _, record := range ledger.Records {
		if record.StageCleanup == nil {
			return outboxEvidenceLedger{}, "invalid"
		}
		for _, file := range record.Files {
			if file.Phase == nil || file.Anchor == nil || file.Cleanup == nil {
				return outboxEvidenceLedger{}, "invalid"
			}
		}
	}
	return ledger, "none"
}

func observeOutboxSavedInventory(profile, destination, batchRoot, deliveryID string, expected transfer.Entry) outboxSavedEvidence {
	e := outboxSavedEvidence{BatchKind: outboxEvidenceKind(expected.Kind), Ledger: "unavailable", Record: "unavailable", LedgerEntry: "unavailable", Phase: "unavailable", StageRead: "unavailable", StageCount: "unavailable", EntryKind: "unavailable", AnchorComparison: "unavailable", SavedFileComparison: "unavailable"}
	ledger, class := readOutboxEvidenceLedger(profile)
	e.Ledger = class
	var matched *outboxEvidenceFile
	if class == "none" {
		var record *outboxEvidenceRecord
		count := 0
		for i := range ledger.Records {
			if ledger.Records[i].Offer.DeliveryID == deliveryID {
				count++
				record = &ledger.Records[i]
			}
		}
		e.Record = outboxInventoryCount(count)
		if count == 1 {
			e.RecordRootMatches = filepath.Join(record.Destination, record.Root) == batchRoot
			e.StageCleanup = *record.StageCleanup
			count = 0
			for i := range record.Files {
				if record.Files[i].Entry == expected {
					count++
					matched = &record.Files[i]
				}
			}
			e.LedgerEntry = outboxInventoryCount(count)
			if count == 1 {
				e.Phase = outboxEvidencePhase(*matched.Phase)
				e.AnchorPresent = *matched.Anchor != ""
				e.Cleanup = *matched.Cleanup
			} else {
				matched = nil
			}
		}
	}
	if filepath.Dir(batchRoot) != destination || filepath.Base(batchRoot) == "." || filepath.Base(batchRoot) == ".." {
		e.StageRead = "wrong_root"
		return e
	}
	parent, class := outboxEvidenceRoot(destination)
	if class != "none" {
		e.StageRead = class
		return e
	}
	defer parent.Close()
	root, class := outboxEvidenceChild(parent, filepath.Base(batchRoot))
	if class != "none" {
		e.StageRead = class
		return e
	}
	defer root.Close()
	stage, class := outboxEvidenceChild(root, ".sobalink-durable-stage-v1")
	if class != "none" {
		e.StageRead = class
		return e
	}
	defer stage.Close()
	directory, err := stage.Open(".")
	if err != nil {
		e.StageRead = outboxInventoryErrorClass(err)
		return e
	}
	entries, err := directory.ReadDir(2)
	_ = directory.Close()
	if err != nil && err != io.EOF {
		e.StageRead = outboxInventoryErrorClass(err)
		return e
	}
	e.StageRead, e.StageCount = "none", outboxInventoryCount(len(entries))
	if len(entries) != 1 {
		return e
	}
	name := entries[0].Name()
	info, err := stage.Lstat(name)
	if err != nil {
		e.EntryKind = "unavailable"
		return e
	}
	e.EntryKind = outboxEvidenceMode(info)
	if matched != nil && e.RecordRootMatches {
		e.AnchorComparison = "different"
		if *matched.Anchor == "" {
			e.AnchorComparison = "absent"
		} else if *matched.Anchor == name {
			e.AnchorComparison = "same"
		}
	}
	if !info.Mode().IsRegular() || expected.Kind != transfer.File || filepath.Base(expected.Path) != expected.Path || expected.Path == "." || expected.Path == ".sobalink-durable-stage-v1" {
		return e
	}
	anchor, anchorInfo, class := outboxEvidenceFileHandle(stage, name)
	if class != "none" {
		return e
	}
	defer anchor.Close()
	output, outputInfo, class := outboxEvidenceFileHandle(root, expected.Path)
	if class != "none" {
		return e
	}
	defer output.Close()
	e.SavedFileComparison = "different"
	if os.SameFile(anchorInfo, outputInfo) {
		e.SavedFileComparison = "same"
	}
	return e
}
