package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedRetryEligibilityAndInactiveRetryPreserveSuccessfulFiles(t *testing.T) {
	m, peer := testManager(t, Options{})
	batch := testAccepted(t, m, peer, testManifest("selected", testEntry("a", "a.txt", "first"), testEntry("b", "b.txt", "second")))
	if _, err := m.ReceiveFile(context.Background(), peer, batch.ID, "a", strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, batch.ID, "b", strings.NewReader("sec")); !errors.Is(err, ErrIntegrity) {
		t.Fatal(err)
	}
	if reason := m.RetryEligibility(batch.ID); reason != "" {
		t.Fatalf("failed eligibility = %s", reason)
	}
	before, err := os.Stat(filepath.Join(batch.Destination, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.batches[batch.ID].active++
	m.mu.Unlock()
	if reason := m.RetryEligibility(batch.ID); reason != "busy" {
		t.Fatalf("active eligibility=%s", reason)
	}
	if _, err := m.RetryFileInactive(batch.ID, "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("active retry=%v", err)
	}
	m.mu.Lock()
	m.batches[batch.ID].active--
	m.mu.Unlock()
	result, err := m.RetryFileInactive(batch.ID, "b")
	if err != nil || result.Files[0].State != FileSaved || result.Files[0].CompletedBytes != 5 || result.Files[1].State != FilePending {
		t.Fatalf("retry=%+v %v", result, err)
	}
	if _, err := m.ReceiveFile(context.Background(), peer, batch.ID, "b", strings.NewReader("second")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(batch.Destination, "a.txt"))
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("successful file replaced")
	}
	if reason := m.RetryEligibility(batch.ID); reason != "not-failed" {
		t.Fatalf("completed eligibility=%s", reason)
	}
	if _, err := m.RetryFileInactive(batch.ID, "b"); !errors.Is(err, ErrState) {
		t.Fatalf("completed retry=%v", err)
	}
}

func TestSelectedRetryEligibilityRejectsRepairedAndPausedPeers(t *testing.T) {
	for _, change := range []string{"repair", "pause", "cancel"} {
		t.Run(change, func(t *testing.T) {
			m, peer := testManager(t, Options{})
			b := testAccepted(t, m, peer, testManifest("selected", testEntry("b", "b.txt", "second")))
			if _, err := m.ReceiveFile(context.Background(), peer, b.ID, "b", strings.NewReader("sec")); !errors.Is(err, ErrIntegrity) {
				t.Fatal(err)
			}
			switch change {
			case "repair":
				peer.Generation++
				if err := m.BindPeer(peer); err != nil {
					t.Fatal(err)
				}
			case "pause":
				if err := m.PausePeer(peer.ID, true); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := m.Cancel(b.ID); err != nil {
					t.Fatal(err)
				}
			}
			if reason := m.RetryEligibility(b.ID); reason == "" {
				t.Fatal("changed eligibility admitted")
			}
			if _, err := m.RetryFileInactive(b.ID, "b"); err == nil {
				t.Fatal("changed retry admitted")
			}
		})
	}
}
