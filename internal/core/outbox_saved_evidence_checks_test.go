package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func outboxEvidenceValue[T any](value T) *T { return &value }

type outboxEvidenceFixture struct {
	profile, root, stage, output, anchor, ledgerPath string
	entry                                            transfer.Entry
	ledger                                           outboxEvidenceLedger
}

func newOutboxEvidenceFixture(t *testing.T) *outboxEvidenceFixture {
	t.Helper()
	base := t.TempDir()
	f := &outboxEvidenceFixture{profile: filepath.Join(base, "profile"), root: filepath.Join(base, "destination", "batch"), entry: transfer.Entry{ID: "synthetic-private-id", Path: "reviewed", Kind: transfer.File, Size: 5}}
	f.stage = filepath.Join(f.root, ".sobalink-durable-stage-v1")
	f.output, f.anchor = filepath.Join(f.root, f.entry.Path), filepath.Join(f.stage, "synthetic-private-anchor")
	f.ledgerPath = filepath.Join(f.profile, "durable-v1", "receiver.json")
	if os.MkdirAll(filepath.Dir(f.ledgerPath), 0700) != nil || os.MkdirAll(f.stage, 0700) != nil || os.WriteFile(f.output, []byte("saved"), 0600) != nil || os.Link(f.output, f.anchor) != nil {
		t.Fatal("synthetic inventory evidence fixture unavailable")
	}
	var record outboxEvidenceRecord
	record.Offer.DeliveryID = "synthetic-private-delivery"
	record.Destination, record.Root = filepath.Dir(f.root), filepath.Base(f.root)
	record.StageCleanup = outboxEvidenceValue(false)
	record.Files = []outboxEvidenceFile{{Entry: f.entry, Phase: outboxEvidenceValue("saved"), Anchor: outboxEvidenceValue(filepath.Base(f.anchor)), Cleanup: outboxEvidenceValue(false)}}
	f.ledger.Records = []outboxEvidenceRecord{record}
	f.save(t)
	return f
}
func (f *outboxEvidenceFixture) save(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.ledger)
	if err != nil || os.WriteFile(f.ledgerPath, data, 0600) != nil {
		t.Fatal("synthetic evidence ledger unavailable")
	}
}
func (f *outboxEvidenceFixture) observe() outboxSavedEvidence {
	return observeOutboxSavedInventory(f.profile, filepath.Dir(f.root), f.root, "synthetic-private-delivery", f.entry)
}
func requireOutboxEvidenceClosed(t *testing.T, evidence outboxSavedEvidence) {
	t.Helper()
	allowed := map[string]bool{}
	for _, v := range []string{"unavailable", "none", "permission", "not_found", "exists", "other", "wrong_type", "wrong_root", "changed", "limit", "invalid", "zero", "one", "multiple", "file", "directory", "symlink", "pending", "receiving", "publish_pending", "saved", "different", "same", "absent"} {
		allowed[v] = true
	}
	value := reflect.ValueOf(evidence)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Bool && (field.Kind() != reflect.String || !allowed[field.String()]) {
			t.Fatal("inventory evidence contains a value outside the closed schema")
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", evidence), "synthetic-private") {
		t.Fatal("inventory evidence exposed a private fixture value")
	}
}

func TestOutboxSavedEvidenceKnownAnchorAndReadOnly(t *testing.T) {
	f := newOutboxEvidenceFixture(t)
	before, err := os.ReadFile(f.ledgerPath)
	if err != nil {
		t.Fatal("synthetic ledger unavailable")
	}
	_, beforeFailure := outboxSavedInventory(f.root, f.entry)
	e := f.observe()
	requireOutboxEvidenceClosed(t, e)
	if e.BatchKind != "file" || e.Ledger != "none" || e.Record != "one" || !e.RecordRootMatches || e.LedgerEntry != "one" || e.Phase != "saved" || !e.AnchorPresent || e.Cleanup || e.StageCleanup || e.StageRead != "none" || e.StageCount != "one" || e.EntryKind != "file" || e.AnchorComparison != "same" || e.SavedFileComparison != "same" {
		t.Fatal("known saved anchor evidence was not distinguished")
	}
	after, err := os.ReadFile(f.ledgerPath)
	_, afterFailure := outboxSavedInventory(f.root, f.entry)
	output, outputErr := os.ReadFile(f.output)
	anchor, anchorErr := os.ReadFile(f.anchor)
	if err != nil || !bytes.Equal(before, after) || outputErr != nil || anchorErr != nil || string(output) != "saved" || !bytes.Equal(output, anchor) || beforeFailure == nil || afterFailure == nil || beforeFailure.Error() != afterFailure.Error() {
		t.Fatal("evidence changed ledger, saved data, retained anchor or original failure")
	}
}

func TestOutboxSavedEvidenceDistinguishesEntryFacts(t *testing.T) {
	for _, name := range []string{"different_file", "unknown_anchor", "anchor_absent", "cleanup_intent", "stage_cleanup_intent", "unknown_phase", "directory_batch", "directory_entry", "multiple_entries", "missing_stage", "missing_output", "wrong_root", "duplicate_record", "duplicate_entry", "changed_entry"} {
		t.Run(name, func(t *testing.T) {
			f := newOutboxEvidenceFixture(t)
			removeAnchor := func() {
				t.Helper()
				if os.Remove(f.anchor) != nil {
					t.Fatal("synthetic anchor replacement unavailable")
				}
			}
			switch name {
			case "different_file":
				removeAnchor()
				if os.WriteFile(f.anchor, []byte("saved"), 0600) != nil {
					t.Fatal("synthetic file unavailable")
				}
			case "unknown_anchor":
				f.ledger.Records[0].Files[0].Anchor = outboxEvidenceValue("synthetic-private-other")
			case "anchor_absent":
				f.ledger.Records[0].Files[0].Anchor = outboxEvidenceValue("")
			case "cleanup_intent":
				f.ledger.Records[0].Files[0].Cleanup = outboxEvidenceValue(true)
			case "stage_cleanup_intent":
				f.ledger.Records[0].StageCleanup = outboxEvidenceValue(true)
			case "unknown_phase":
				f.ledger.Records[0].Files[0].Phase = outboxEvidenceValue("synthetic-private-secret")
			case "directory_batch":
				f.entry.Kind, f.entry.Size = transfer.Directory, 0
				f.ledger.Records[0].Files[0].Entry = f.entry
				f.ledger.Records[0].Files[0].Anchor = outboxEvidenceValue("")
			case "directory_entry":
				removeAnchor()
				if os.Mkdir(f.anchor, 0700) != nil {
					t.Fatal("synthetic directory unavailable")
				}
			case "multiple_entries":
				if os.WriteFile(filepath.Join(f.stage, "synthetic-private-extra"), nil, 0600) != nil {
					t.Fatal("synthetic extra entry unavailable")
				}
			case "missing_stage":
				removeAnchor()
				if os.Remove(f.stage) != nil {
					t.Fatal("synthetic stage removal unavailable")
				}
			case "missing_output":
				if os.Remove(f.output) != nil {
					t.Fatal("synthetic output removal unavailable")
				}
			case "wrong_root":
				f.ledger.Records[0].Root = "synthetic-private-wrong"
			case "duplicate_record":
				f.ledger.Records = append(f.ledger.Records, f.ledger.Records[0])
			case "duplicate_entry":
				f.ledger.Records[0].Files = append(f.ledger.Records[0].Files, f.ledger.Records[0].Files[0])
			case "changed_entry":
				f.ledger.Records[0].Files[0].Entry.Path = "synthetic-private-changed"
			}
			f.save(t)
			e := f.observe()
			requireOutboxEvidenceClosed(t, e)
			valid := false
			switch name {
			case "different_file":
				valid = e.AnchorComparison == "same" && e.SavedFileComparison == "different"
			case "unknown_anchor":
				valid = e.AnchorComparison == "different" && e.SavedFileComparison == "same"
			case "anchor_absent":
				valid = !e.AnchorPresent && e.AnchorComparison == "absent"
			case "cleanup_intent":
				valid = e.Cleanup && e.AnchorComparison == "same"
			case "stage_cleanup_intent":
				valid = e.StageCleanup
			case "unknown_phase":
				valid = e.Phase == "other"
			case "directory_batch":
				valid = e.BatchKind == "directory" && e.AnchorComparison == "absent" && e.SavedFileComparison == "unavailable"
			case "directory_entry":
				valid = e.EntryKind == "directory" && e.SavedFileComparison == "unavailable"
			case "multiple_entries":
				valid = e.StageCount == "multiple" && e.AnchorComparison == "unavailable" && e.EntryKind == "unavailable"
			case "missing_stage":
				valid = e.StageRead == "not_found" && e.AnchorComparison == "unavailable"
			case "missing_output":
				valid = e.SavedFileComparison == "unavailable"
			case "wrong_root":
				valid = !e.RecordRootMatches && e.AnchorComparison == "unavailable"
			case "duplicate_record":
				valid = e.Record == "multiple" && e.Phase == "unavailable" && e.AnchorComparison == "unavailable"
			case "duplicate_entry":
				valid = e.LedgerEntry == "multiple" && e.Phase == "unavailable" && e.AnchorComparison == "unavailable"
			case "changed_entry":
				valid = e.LedgerEntry == "zero" && e.Phase == "unavailable" && e.AnchorComparison == "unavailable"
			}
			if !valid {
				t.Fatal("inventory evidence conflated distinct observed facts")
			}
		})
	}
}

func TestOutboxSavedEvidenceRejectsUnsafeLedger(t *testing.T) {
	for _, kind := range []string{"invalid", "limit", "missing", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newOutboxEvidenceFixture(t)
			var err error
			switch kind {
			case "invalid":
				err = os.WriteFile(f.ledgerPath, []byte("synthetic-private-invalid-json"), 0600)
			case "limit":
				err = os.WriteFile(f.ledgerPath, bytes.Repeat([]byte(" "), (1<<20)+1), 0600)
			case "missing", "directory":
				err = os.Remove(f.ledgerPath)
				if err == nil && kind == "directory" {
					err = os.Mkdir(f.ledgerPath, 0700)
				}
			}
			if err != nil {
				t.Fatal("synthetic invalid ledger unavailable")
			}
			e := f.observe()
			requireOutboxEvidenceClosed(t, e)
			want := kind
			if kind == "missing" {
				want = "not_found"
			}
			if kind == "directory" {
				want = "wrong_type"
			}
			if e.Ledger != want || e.Record != "unavailable" || e.Phase != "unavailable" || e.AnchorComparison != "unavailable" {
				t.Fatal("invalid ledger was treated as known state")
			}
		})
	}
}

func TestOutboxSavedEvidenceRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"ledger", "namespace", "profile", "destination", "batch", "stage", "anchor", "output"} {
		t.Run(target, func(t *testing.T) {
			f := newOutboxEvidenceFixture(t)
			path := map[string]string{"ledger": f.ledgerPath, "namespace": filepath.Dir(f.ledgerPath), "profile": f.profile, "destination": filepath.Dir(f.root), "batch": f.root, "stage": f.stage, "anchor": f.anchor, "output": f.output}[target]
			replaced := filepath.Join(t.TempDir(), "original")
			if os.Rename(path, replaced) != nil {
				t.Fatal("synthetic link fixture unavailable")
			}
			if os.Symlink(replaced, path) != nil {
				t.Skip("symlink unavailable")
			}
			e := f.observe()
			requireOutboxEvidenceClosed(t, e)
			valid := false
			switch target {
			case "ledger", "namespace", "profile":
				valid = e.Ledger == "wrong_type" && e.AnchorComparison == "unavailable"
			case "destination", "batch", "stage":
				valid = e.StageRead == "wrong_type" && e.SavedFileComparison == "unavailable"
			case "anchor":
				valid = e.EntryKind == "symlink" && e.SavedFileComparison == "unavailable"
			case "output":
				valid = e.SavedFileComparison == "unavailable"
			}
			if !valid {
				t.Fatal("inventory evidence followed a symbolic fixture component")
			}
		})
	}
}

func TestOutboxSavedEvidenceRequiresFieldPresence(t *testing.T) {
	for _, field := range []string{"phase", "anchor", "cleanup", "stageCleanup"} {
		for _, missing := range []bool{false, true} {
			t.Run(field+fmt.Sprint(missing), func(t *testing.T) {
				f := newOutboxEvidenceFixture(t)
				data, err := os.ReadFile(f.ledgerPath)
				var raw map[string]any
				if err != nil || json.Unmarshal(data, &raw) != nil {
					t.Fatal("synthetic ledger unavailable")
				}
				record := raw["records"].([]any)[0].(map[string]any)
				target := record
				if field != "stageCleanup" {
					target = record["files"].([]any)[0].(map[string]any)
				}
				if missing {
					delete(target, field)
				} else {
					target[field] = nil
				}
				data, err = json.Marshal(raw)
				if err != nil || os.WriteFile(f.ledgerPath, data, 0600) != nil {
					t.Fatal("synthetic incomplete ledger unavailable")
				}
				e := f.observe()
				requireOutboxEvidenceClosed(t, e)
				if e.Ledger != "invalid" || e.Phase != "unavailable" || e.AnchorComparison != "unavailable" {
					t.Fatal("missing observation field was reported as known state")
				}
			})
		}
	}
}

func TestOutboxSavedEvidenceReadsProductionLedgerAfterOwnerRefusal(t *testing.T) {
	profile, destination := t.TempDir(), t.TempDir()
	if os.Mkdir(filepath.Join(profile, "durable-v1"), 0700) != nil {
		t.Fatal("synthetic namespace unavailable")
	}
	refuseCleanup, refused := false, false
	// Test-only owner admission refuses after the saved ledger is committed.
	// This verifies a permitted sequence, not the cause of native Windows CI.
	ownerOperation := func(operation func() error) error {
		if refuseCleanup {
			refused = true
			return transfer.ErrBusy
		}
		err := operation()
		ledger, class := readOutboxEvidenceLedger(profile)
		if err == nil && class == "none" && len(ledger.Records) == 1 && len(ledger.Records[0].Files) == 1 && *ledger.Records[0].Files[0].Phase == "saved" {
			refuseCleanup = true
		}
		return err
	}
	m, err := transfer.NewManager(transfer.Options{Limits: transfer.Limits{DiskReserveBytes: 1}, Durable: &transfer.DurableOptions{Path: filepath.Join(profile, "durable-v1", "receiver.json"), Initialize: true, Epoch: "1234567890abcdef1234567890abcdef", OwnerOperation: ownerOperation}})
	if err != nil {
		t.Fatal("synthetic receiver unavailable")
	}
	t.Cleanup(func() { _ = m.Close() })
	peer := transfer.Peer{ID: "synthetic-peer", Generation: 1}
	if m.BindPeer(peer) != nil || m.BindDurableRelationship(peer, "synthetic", "epoch-one") != nil {
		t.Fatal("synthetic relationship unavailable")
	}
	now := time.Now().UTC()
	manifest := coreCrashManifest("synthetic-delivery", "synthetic payload")
	manifest.Entries[0].Path = "reviewed"
	offer := transfer.DurableOffer{Version: 1, DeliveryID: manifest.ID, RecipientID: "synthetic-receiver", Manifest: manifest, CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	offer.Digest, err = transfer.DurableDigest(offer)
	if err != nil {
		t.Fatal("synthetic offer unavailable")
	}
	status, err := m.OfferDurable(peer, offer.RecipientID, offer)
	if err != nil {
		t.Fatal("synthetic offer refused")
	}
	batch, err := m.Accept(offer.DeliveryID, destination)
	if err != nil {
		t.Fatal("synthetic acceptance unavailable")
	}
	if _, err = m.StartDurable(context.Background(), peer, status.Lease); err != nil {
		t.Fatal("synthetic start unavailable")
	}
	saved, err := m.ReceiveDurableFile(context.Background(), peer, status.Lease, manifest.Entries[0].ID, strings.NewReader("synthetic payload"))
	if err != nil || saved.State != transfer.Completed || saved.Receipt == nil || !refused {
		t.Fatal("owner cleanup refusal changed the saved receipt")
	}
	batch, err = m.Get(offer.DeliveryID)
	if err != nil {
		t.Fatal("synthetic completed batch unavailable")
	}
	e := observeOutboxSavedInventory(profile, destination, batch.Destination, offer.DeliveryID, manifest.Entries[0])
	requireOutboxEvidenceClosed(t, e)
	if e.Ledger != "none" || e.Record != "one" || !e.RecordRootMatches || e.LedgerEntry != "one" || e.Phase != "saved" || e.Cleanup || e.StageCleanup || e.AnchorComparison != "same" || e.SavedFileComparison != "same" {
		t.Fatal("production saved ledger and retained hardlink were not observed correctly")
	}
	if _, err = outboxSavedInventory(batch.Destination, manifest.Entries[0]); err == nil {
		t.Fatal("retained anchor stopped failing the original inventory assertion")
	}
}
