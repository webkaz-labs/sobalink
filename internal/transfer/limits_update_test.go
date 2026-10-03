package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimitReductionPreservesExistingOffersAndHistory(t *testing.T) {
	m, peer := testManager(t, Options{})
	manifest := testManifest("existing", testEntry("a", "a", "retained"))
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateLimits(Limits{MaxBatches: 1, MaxReservedBytes: 1, MaxFileBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatalf("lower limit broke idempotent offer: %v", err)
	}
	if _, err := m.Offer(peer, testManifest("new", testEntry("a", "a", "x"))); !errors.Is(err, ErrLimit) {
		t.Fatalf("new admission ignored lower limit: %v", err)
	}
	if len(m.List()) != 1 {
		t.Fatal("limit update evicted history")
	}
	if err := m.UpdateLimits(Limits{MaxReservedBytes: -1}); !errors.Is(err, ErrLimit) {
		t.Fatal("invalid update accepted")
	}
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal("failed update changed existing state", err)
	}
}

func TestPathLimitReductionPreservesAdmittedReceive(t *testing.T) {
	for state, acceptBeforeUpdate := range map[string]bool{"pending": false, "accepted": true} {
		t.Run(state, func(t *testing.T) {
			m, peer := testManager(t, Options{Limits: Limits{MaxDepth: 32, MaxPathBytes: 8192}})
			name := strings.Repeat("d/", 20) + "file"
			manifest := testManifest("existing", testEntry("file", name, "payload"))
			if _, err := m.Offer(peer, manifest); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if acceptBeforeUpdate {
				if _, err := m.Accept(manifest.ID, destination); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.UpdateLimits(Limits{MaxDepth: 1, MaxPathBytes: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Offer(peer, manifest); err != nil {
				t.Fatalf("retained offer failed: %v", err)
			}
			if _, err := m.Accept(manifest.ID, destination); err != nil {
				t.Fatalf("retained acceptance failed: %v", err)
			}
			if _, err := m.ReceiveFile(context.Background(), peer, manifest.ID, "file", strings.NewReader("payload")); err != nil {
				t.Fatalf("admitted stream failed: %v", err)
			}
			batch, err := m.Get(manifest.ID)
			if err != nil || batch.State != Completed {
				t.Fatalf("batch did not complete: %+v, %v", batch, err)
			}
			if data, err := os.ReadFile(filepath.Join(batch.Destination, filepath.FromSlash(name))); err != nil || string(data) != "payload" {
				t.Fatalf("saved payload = %q, %v", data, err)
			}
			manifest.ID = "new"
			if _, err := m.Offer(peer, manifest); err == nil {
				t.Fatal("lowered path choices admitted new offer")
			}
		})
	}
}

func TestRetainedMetadataBudgetSurvivesLowerAdmission(t *testing.T) {
	m, peer := testManager(t, Options{})
	manifest := testManifest("retained", testEntry("entry", "path", "payload"))
	if _, err := m.Offer(peer, manifest); err != nil {
		t.Fatal(err)
	}
	before := m.RetainedMetadataBytes()
	if before != ManifestMetadataBytes(manifest) {
		t.Fatalf("retained accounting %d", before)
	}
	if err := m.UpdateLimits(Limits{MaxMetadataBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if m.RetainedMetadataBytes() != before {
		t.Fatal("new admission policy erased retained read budget")
	}
}
