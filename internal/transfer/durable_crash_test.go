package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const durableCrashExitCode = 89

type durableCrashFixture struct {
	LedgerDir string       `json:"ledgerDir"`
	Lease     DurableLease `json:"lease"`
}

// This helper uses the test binary only. There are no production environment
// switches, replacement filesystem operations, deferred closes or cleanup at
// the selected cut: os.Exit terminates the owning process immediately.
func TestDurableAbruptDeathChild(t *testing.T) {
	fixturePath := os.Getenv("SOBALINK_DURABLE_CRASH_FIXTURE")
	if fixturePath == "" {
		t.Skip("private subprocess helper")
	}
	phase := os.Getenv("SOBALINK_DURABLE_CRASH_PHASE")
	seam := phase
	switch phase {
	case "before_link", "after_link", "after_final_sync":
	case "saved_before_ack":
		// Deferred cleanup is reached after the saved ledger has been certified,
		// but before ReceiveDurableFile returns a result to its caller.
		seam = "before_cleanup"
	default:
		t.Fatalf("unknown private crash phase %q", phase)
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture durableCrashFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	m := durableTestManager(t, fixture.LedgerDir, false)
	m.durable.fault = func(at string) error {
		if at == seam {
			if _, err := os.Stdout.WriteString("DURABLE_RECEIVER_CUT=" + phase + "\n"); err != nil {
				t.Fatal(err)
			}
			os.Exit(durableCrashExitCode)
		}
		return nil
	}
	_, err = m.ReceiveDurableFile(context.Background(), testDurablePeer, fixture.Lease, "file-one", strings.NewReader("synthetic payload"))
	t.Fatalf("private crash helper returned without the cut: %v", err)
}

func TestDurableAbruptDeathPublicationBoundaries(t *testing.T) {
	for _, phase := range []string{"before_link", "after_link", "after_final_sync", "saved_before_ack"} {
		t.Run(phase, func(t *testing.T) {
			ledgerDir := t.TempDir()
			m := durableTestManager(t, ledgerDir, true)
			offer, batch := durableTestAccepted(t, m)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			fixturePath := filepath.Join(t.TempDir(), "fixture.json")
			fixture, err := json.Marshal(durableCrashFixture{LedgerDir: ledgerDir, Lease: offer.Lease})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurableAbruptDeathChild$")
			cmd.Env = append(os.Environ(), "SOBALINK_DURABLE_CRASH_FIXTURE="+fixturePath, "SOBALINK_DURABLE_CRASH_PHASE="+phase)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != durableCrashExitCode || !bytes.Contains(output, []byte("DURABLE_RECEIVER_CUT="+phase+"\n")) {
				t.Fatalf("helper did not reach the abrupt-death cut: %v, %s", err, output)
			}

			// Inspect the real on-disk intent before any recovery operation. It must
			// retain the original lease and an anchor with the original bytes and
			// native file identity, even when no final link was published.
			data, err := os.ReadFile(filepath.Join(ledgerDir, "receiver.json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot durableSnapshot
			if err = json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Epoch != testDurableEpoch || len(snapshot.Records) != 1 || snapshot.Records[0] == nil {
				t.Fatalf("unexpected crash ledger: %+v", snapshot)
			}
			record := snapshot.Records[0]
			if !sameDurableLease(record.Lease, offer.Lease) || len(record.Files) != 1 || record.Files[0].Anchor == "" {
				t.Fatalf("crash lost the original lease or anchor: %+v", record)
			}
			file := record.Files[0]
			wantPhase := "publish_pending"
			if phase == "saved_before_ack" {
				wantPhase = "saved"
			}
			if file.Phase != wantPhase || (phase == "saved_before_ack" && (record.State != Completed || file.SavedAt.IsZero())) {
				t.Fatalf("cut had unexpected durable phase: %+v", file)
			}
			anchor := filepath.Join(batch.Destination, durableStage, file.Anchor)
			anchorFile, err := os.Open(anchor)
			if err != nil {
				t.Fatal(err)
			}
			// Handle.Stat captures Windows file identity eagerly. A path-only
			// os.Stat result may try to reopen this anchor after cleanup deleted it.
			anchorInfo, statErr := anchorFile.Stat()
			anchorCloseErr := anchorFile.Close()
			if statErr != nil || anchorCloseErr != nil {
				t.Fatalf("capture native anchor identity: %v, %v", statErr, anchorCloseErr)
			}
			if payload, err := os.ReadFile(anchor); err != nil || string(payload) != "synthetic payload" {
				t.Fatalf("crash changed anchored bytes: %q, %v", payload, err)
			}
			root, err := os.OpenRoot(batch.Destination)
			if err != nil {
				t.Fatal(err)
			}
			identity, identityErr := durableIdentity(root, durableStage+"/"+file.Anchor, false)
			closeErr := root.Close()
			if identityErr != nil || closeErr != nil || identity != file.Identity {
				t.Fatalf("crash anchor identity changed: %q != %q, %v, %v", identity, file.Identity, identityErr, closeErr)
			}
			final := filepath.Join(batch.Destination, filepath.FromSlash(file.Entry.Path))
			finalInfo, finalErr := os.Stat(final)
			if phase == "before_link" {
				if !errors.Is(finalErr, os.ErrNotExist) {
					t.Fatalf("before-link cut already published output: %v", finalErr)
				}
			} else if finalErr != nil || !os.SameFile(anchorInfo, finalInfo) {
				t.Fatalf("published output is not the retained native file: %v", finalErr)
			}

			m = durableTestManager(t, ledgerDir, false)
			status, err := m.StatusDurable(testDurablePeer, offer.Lease)
			if err != nil || !sameDurableLease(status.Lease, offer.Lease) {
				t.Fatalf("reopen lost exact lease: %+v, %v", status, err)
			}
			if phase == "before_link" {
				if status.Receipt != nil || status.State == Completed {
					t.Fatalf("status invented an unpublished save: %+v", status)
				}
				if _, err = os.Stat(final); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read-only status reconstructed missing output: %v", err)
				}
				// Only an explicit request using the original lease may publish the
				// retained, verified intent. It must not consume another payload.
				status, err = m.ReceiveDurableFile(context.Background(), testDurablePeer, offer.Lease, file.Entry.ID, failReader{})
				if err != nil {
					t.Fatal(err)
				}
			}
			wantAck := FileAck{BatchID: offer.Lease.DeliveryID, FileID: file.Entry.ID, StoredName: file.Entry.Path, Size: file.Entry.Size, SHA256: file.Entry.SHA256}
			if status.State != Completed || status.Receipt == nil || !sameDurableLease(status.Receipt.Lease, offer.Lease) || status.Receipt.State != Completed || len(status.Receipt.Files) != 1 || status.Receipt.Files[0] != wantAck || status.Receipt.SavedAt.IsZero() {
				t.Fatalf("reopen did not certify exact evidence: %+v", status)
			}
			if phase == "saved_before_ack" && !status.Receipt.SavedAt.Equal(file.SavedAt) {
				t.Fatal("lost-ACK recovery replaced the durable saved timestamp")
			}
			if err = m.MaintainDurable(); err != nil {
				t.Fatal(err)
			}
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			m = durableTestManager(t, ledgerDir, false)
			again, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, offer.Lease, file.Entry.ID, failReader{})
			if err != nil || !reflect.DeepEqual(status.Receipt, again.Receipt) {
				t.Fatalf("second reopen changed saved evidence: %+v, %v", again, err)
			}
			afterInfo, err := os.Stat(final)
			if err != nil || !os.SameFile(anchorInfo, afterInfo) {
				t.Fatalf("recovery replaced native output identity: %v", err)
			}
			if payload, err := os.ReadFile(final); err != nil || string(payload) != "synthetic payload" {
				t.Fatalf("recovery changed output bytes: %q, %v", payload, err)
			}
			roots, err := os.ReadDir(filepath.Dir(batch.Destination))
			if err != nil || len(roots) != 1 || roots[0].Name() != filepath.Base(batch.Destination) {
				t.Fatalf("recovery created another output root: %v, %v", roots, err)
			}
			var outputs int
			if err = filepath.WalkDir(batch.Destination, func(_ string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() {
					outputs++
				}
				return nil
			}); err != nil || outputs != 1 {
				t.Fatalf("recovery did not leave exactly one output: %d, %v", outputs, err)
			}
		})
	}
}
