package transfer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func nextEvidenceOffer(t *testing.T, m *Manager, id string) DurableOffer {
	t.Helper()
	now := m.durable.now().UTC()
	o := DurableOffer{Version: DurableVersion, DeliveryID: id, RecipientID: "synthetic-receiver", Manifest: Manifest{ID: id, Entries: []Entry{durableTestEntry()}}, CreatedAt: now, SentAt: now, ExpiresAt: now.Add(time.Hour)}
	var err error
	o.Digest, err = DurableDigest(o)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestDurableTerminalEvidenceSharesOnlyActiveAdmission(t *testing.T) {
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	if err := m.UpdateLimits(Limits{MaxBatches: 1, MaxPendingBatches: 1, MaxPendingPerPeer: 1, DiskReserveBytes: 1}); err != nil {
		t.Fatal(err)
	}
	first := durableTestOffer(t, m, "retained-original", durableTestEntry())
	if _, err := m.Cancel(first.Lease.DeliveryID); err != nil {
		t.Fatal(err)
	}
	if m.durableCountLocked() != 0 || m.durableMetadataLocked() != 0 || len(m.durable.state.Records) != 1 {
		t.Fatal("terminal evidence consumed live admission or disappeared")
	}
	ordinary := Manifest{ID: "ordinary-new", Entries: []Entry{durableTestEntry()}}
	if _, err := m.Offer(testDurablePeer, ordinary); err != nil {
		t.Fatal("terminal durable evidence blocked ordinary work", err)
	}
	secondOffer := nextEvidenceOffer(t, m, "durable-new")
	if _, err := m.OfferDurable(testDurablePeer, secondOffer.RecipientID, secondOffer); !errors.Is(err, ErrDurableCapacity) {
		t.Fatal("ordinary and durable offers used separate live budgets", err)
	}
	if _, err := m.Cancel(ordinary.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget(ordinary.ID); err != nil {
		t.Fatal(err)
	}
	second, err := m.OfferDurable(testDurablePeer, secondOffer.RecipientID, secondOffer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(testDurablePeer, ordinary); !errors.Is(err, ErrLimit) {
		t.Fatal("ordinary offer bypassed active durable admission", err)
	}
	if _, err := m.Cancel(second.Lease.DeliveryID); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	m = durableTestManager(t, dir, false)
	status, err := m.StatusDurable(testDurablePeer, first.Lease)
	if err != nil || status.State != Cancelled || !sameDurableLease(status.Lease, first.Lease) || m.durableCountLocked() != 0 {
		t.Fatal("restart changed retained evidence or its live charge", err)
	}
	original := m.durable.state.Records[0].Offer
	original.SentAt = time.Now().UTC()
	status, err = m.OfferDurable(testDurablePeer, original.RecipientID, original)
	if err != nil || !sameDurableLease(status.Lease, first.Lease) || len(m.durable.state.Records) != 2 {
		t.Fatal("retained replay gained another lease or record", err)
	}
	original.Manifest.Entries = append([]Entry(nil), original.Manifest.Entries...)
	original.Manifest.Entries[0].Path = "different.txt"
	original.Digest, _ = DurableDigest(original)
	if _, err := m.OfferDurable(testDurablePeer, original.RecipientID, original); !errors.Is(err, ErrDurableConflict) {
		t.Fatal("evidence separation weakened exact replay", err)
	}
}

func TestDurableActiveEvidencePhysicalAndUncertainFences(t *testing.T) {
	for _, fence := range []string{"nonterminal", "preparing", "physical_owner", "publish_pending", "uncertain"} {
		t.Run(fence, func(t *testing.T) {
			m := durableTestManager(t, t.TempDir(), true)
			s := durableTestOffer(t, m, "retained", durableTestEntry())
			if _, err := m.Cancel(s.Lease.DeliveryID); err != nil {
				t.Fatal(err)
			}
			r := m.durable.state.Records[0]
			switch fence {
			case "nonterminal":
				r.State = Partial
			case "preparing":
				r.Preparing = true
			case "physical_owner":
				r.active = map[string]context.CancelFunc{"file-one": func() {}}
			case "publish_pending":
				r.Files[0].Phase = "publish_pending"
			case "uncertain":
				m.durable.blocked = ErrDurableRecovery
			}
			if err := m.UpdateLimits(Limits{MaxBatches: 1, MaxPendingBatches: 1, MaxPendingPerPeer: 1, DiskReserveBytes: 1}); err != nil {
				t.Fatal(err)
			}
			if m.durableCountLocked() != 1 || m.durableMetadataLocked() != r.ReservedMetadata {
				t.Fatal("live fence released admission or metadata")
			}
			if _, err := m.Offer(testDurablePeer, Manifest{ID: "ordinary", Entries: []Entry{durableTestEntry()}}); !errors.Is(err, ErrLimit) {
				t.Fatal("ordinary offer bypassed retained live fence", err)
			}
			offer := nextEvidenceOffer(t, m, "durable")
			_, err := m.OfferDurable(testDurablePeer, offer.RecipientID, offer)
			if fence == "uncertain" {
				if !errors.Is(err, ErrDurableRecovery) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrDurableCapacity) {
				t.Fatal("durable offer bypassed retained live fence", err)
			}
		})
	}
}

func TestDurableTerminalCleanupKeepsBytesWithoutActiveSlot(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	s, _ := durableTestAccepted(t, m)
	m.durable.fault = func(at string) error {
		if at == "before_cleanup" {
			return errors.New("synthetic cleanup failure")
		}
		return nil
	}
	result, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload"))
	if err != nil || result.State != Completed || result.Receipt == nil {
		t.Fatal("fixture did not retain completed physical bytes", err)
	}
	r := m.durable.state.Records[0]
	before := m.durableReservedLocked()
	if r.Files[0].Anchor == "" || before != durableTestEntry().Size || m.durableCountLocked() != 0 || m.durableMetadataLocked() != 0 {
		t.Fatal("terminal physical cleanup consumed live slots or lost its bytes")
	}
	if err := m.UpdateLimits(Limits{MaxBatches: 1, MaxReservedBytes: before, DiskReserveBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Offer(testDurablePeer, Manifest{ID: "ordinary", Entries: []Entry{durableTestEntry()}}); !errors.Is(err, ErrLimit) {
		t.Fatal("terminal retained bytes were reused prematurely", err)
	}
	m.durable.fault = nil
	if err := m.MaintainDurable(); err != nil {
		t.Fatal(err)
	}
	if m.durableReservedLocked() != 0 || len(m.durable.state.Records) != 1 {
		t.Fatal("cleanup changed evidence retention or failed byte release")
	}
	if _, err := m.Offer(testDurablePeer, Manifest{ID: "ordinary", Entries: []Entry{durableTestEntry()}}); err != nil {
		t.Fatal("certified cleanup did not restore byte admission", err)
	}
	status, err := m.StatusDurable(testDurablePeer, s.Lease)
	if err != nil || !reflect.DeepEqual(status.Receipt, result.Receipt) {
		t.Fatal("cleanup changed saved evidence", err)
	}
}

func TestDurableUncertainCleanupKeepsCertifiedByteFloor(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	s, _ := durableTestAccepted(t, m)
	m.durable.fault = func(at string) error {
		if at == "before_cleanup" {
			return errors.New("synthetic deferred cleanup")
		}
		return nil
	}
	if _, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, s.Lease, "file-one", strings.NewReader("synthetic payload")); err != nil {
		t.Fatal(err)
	}
	before := m.durableReservedLocked()
	m.durable.fault = func(at string) error {
		if at == "before_ledger" && m.durable.state.Records[0].Files[0].Anchor == "" {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	if err := m.MaintainDurable(); err == nil {
		t.Fatal("fixture did not fail cleanup certification")
	}
	if m.durable.blocked == nil || m.durableReservedLocked() < before || m.durableCountLocked() != 1 || m.durableMetadataLocked() == 0 {
		t.Fatal("uncertain cleanup released certified bytes or live admission")
	}
}

func TestDurableEvidenceHardCapsPreserveExistingOperations(t *testing.T) {
	for _, capKind := range []string{"per_peer", "metadata", "records"} {
		t.Run(capKind, func(t *testing.T) {
			m := durableTestManager(t, t.TempDir(), true)
			s := durableTestOffer(t, m, "original", durableTestEntry())
			if _, err := m.Cancel(s.Lease.DeliveryID); err != nil {
				t.Fatal(err)
			}
			d := m.durable
			source := d.state.Records[0]
			count := 128
			if capKind == "metadata" {
				count = int(durableLedgerBytes / source.ReservedMetadata)
			} else if capKind == "records" {
				count = durableLedgerRecords
			}
			for i := 1; i < count; i++ {
				copy := *source
				copy.Offer = nextEvidenceOffer(t, m, fmt.Sprintf("retained-%d", i))
				copy.Offer.SentAt = time.Time{}
				copy.Lease.DeliveryID = copy.Offer.DeliveryID
				copy.Lease.Digest = copy.Offer.Digest
				copy.Lease.ExpiresAt = copy.Offer.ExpiresAt
				copy.Lease.ReceiptUntil = copy.Offer.ExpiresAt.Add(DurableReceiptGrace)
				copy.Files = append([]durableFile(nil), source.Files...)
				if capKind != "per_peer" {
					copy.Peer.ID = fmt.Sprintf("synthetic-peer-%d", i/100)
				}
				d.state.Records = append(d.state.Records, &copy)
			}
			if err := d.save(); err != nil {
				t.Fatal(err)
			}
			if m.durableCountLocked() != 0 || m.durableMetadataLocked() != 0 {
				t.Fatal("evidence fixture consumed live admission")
			}
			offer := nextEvidenceOffer(t, m, "new")
			if _, err := m.OfferDurable(testDurablePeer, offer.RecipientID, offer); !errors.Is(err, ErrDurableEvidenceCapacity) || DurableErrorCode(err) != "durable_evidence_capacity" {
				t.Fatalf("hard evidence cap returned wrong error: %v", err)
			}
			status, err := m.StatusDurable(testDurablePeer, s.Lease)
			if err != nil || status.State != Cancelled || !sameDurableLease(status.Lease, s.Lease) {
				t.Fatal("full evidence pool blocked existing status", err)
			}
			original := source.Offer
			original.SentAt = time.Now().UTC()
			status, err = m.OfferDurable(testDurablePeer, original.RecipientID, original)
			if err != nil || !sameDurableLease(status.Lease, s.Lease) {
				t.Fatal("full evidence pool blocked exact replay", err)
			}
			if err := m.cleanupDurableLocked(source); err != nil {
				t.Fatal("full evidence pool blocked existing cleanup", err)
			}
			if _, err := m.Offer(testDurablePeer, Manifest{ID: "ordinary-new", Entries: []Entry{durableTestEntry()}}); err != nil {
				t.Fatal("evidence hard cap blocked ordinary admission", err)
			}
		})
	}
}

func TestDurableUncertainRetirementKeepsCertifiedPeerBudget(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	s := durableTestOffer(t, m, "retained-peer-evidence", durableTestEntry())
	if _, err := m.Cancel(s.Lease.DeliveryID); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateLimits(Limits{MaxBatches: 256, MaxPendingBatches: 32, MaxPendingPerPeer: 1, DiskReserveBytes: 1}); err != nil {
		t.Fatal(err)
	}
	m.durable.state.Records = nil // Candidate retirement has not been certified.
	m.durable.blocked = ErrDurableRecovery
	if m.durableCountLocked() != 1 || m.durableMetadataLocked() == 0 {
		t.Fatal("uncertain removal lost certified retained charges")
	}
	if _, err := m.Offer(testDurablePeer, Manifest{ID: "new", Entries: []Entry{durableTestEntry()}}); !errors.Is(err, ErrLimit) {
		t.Fatal("uncertain removal bypassed certified per-peer bound", err)
	}
}

func TestDurableTerminalStageCleanupIsEvidenceOnly(t *testing.T) {
	m := durableTestManager(t, t.TempDir(), true)
	s := durableTestOffer(t, m, "retained-stage", durableTestEntry())
	if _, err := m.Cancel(s.Lease.DeliveryID); err != nil {
		t.Fatal(err)
	}
	r := m.durable.state.Records[0]
	r.StageCleanup = true
	r.Files[0].Anchor = "owned-anchor"
	if m.durableCountLocked() != 0 || m.durableMetadataLocked() != 0 || m.durableReservedLocked() != durableTestEntry().Size {
		t.Fatal("terminal stage cleanup mixed physical and active accounting")
	}
}
