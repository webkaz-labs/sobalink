package core

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resourcegroup"
)

// This cohort has native filesystem effects: a synthetic temporary profile,
// actual process lock, and the additive sidecar only. It never calls Core.Open,
// initializes a target journal/grant, starts a backend, or performs an exchange.
func groupOwnedStoreFixture(t *testing.T) (*Core, *resourceGroupStore) {
	t.Helper()
	dir := t.TempDir()
	owner, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("release synthetic lifecycle owner: %v", err)
		}
	})
	c := &Core{dir: dir, resourceLock: owner, resourceIdentity: strings.Repeat("a", 32)}
	c.op.Lock()
	t.Cleanup(c.op.Unlock)
	s := c.openResourceGroupStore()
	if s.frozen || !s.firstUse || !s.certified || s.current(c) != nil {
		t.Fatal("synthetic owned first use unavailable")
	}
	return c, s
}

func groupOwnedAcceptedFixture(t *testing.T, c *Core, s *resourceGroupStore) resourceGroupEnvelope {
	t.Helper()
	next, err := withResourceGroupRun(s.state, groupStoreRecordFixture(t, *s.state.HighWater+1, 2))
	if err != nil || s.publish(c, next) != nil {
		t.Fatal("owned acceptance publication failed")
	}
	return next
}

func groupOwnedSnapshot(t *testing.T, c *Core, s *resourceGroupStore) resourceGroupFileSnapshot {
	t.Helper()
	var snapshot resourceGroupFileSnapshot
	err := c.resourceLock.WithOwnershipInfo(c.dir, func(profile, lock os.FileInfo) (result error) {
		b, err := s.openBinding(c, profile, lock)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, b.close()) }()
		if err := b.openGroup(s.directory); err != nil {
			return err
		}
		snapshot, err = readResourceGroupSnapshot(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestGroupOwnedStoreFirstUseIsReadOnly(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	for i := 0; i < 3; i++ {
		if err := s.current(c); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "process.lock" {
		t.Fatal("read-only first use allocated state or writer metadata")
	}
	if _, err := os.Lstat(filepath.Dir(resourceGroupStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first use created group directory")
	}
}

func TestGroupOwnedStorePublishesOnlyOwnedModelTransitions(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	next := groupOwnedAcceptedFixture(t, c, s)
	if s.firstUse || s.frozen || s.uncertain || !resourceGroupJSONEqual(s.state, next) || s.current(c) != nil {
		t.Fatal("durable acceptance not certified")
	}
	// The publication owns its decoded copy, not the caller's desired value.
	next.Runs[0].Origins[0].Relationship.PairBinding = strings.Repeat("1", 64)
	if resourceGroupJSONEqual(s.state, next) {
		t.Fatal("published snapshot aliases caller")
	}
	run, err := cloneResourceGroupRecord(s.state.Runs[0])
	if err != nil {
		t.Fatal(err)
	}
	run.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	run.UpdateSequence++
	updated, err := withResourceGroupUpdate(s.state, run)
	if err != nil || s.publish(c, updated) != nil || s.current(c) != nil {
		t.Fatal("exact model update rejected")
	}
	if !s.state.Runs[0].pinned() {
		t.Fatal("possible dispatch was not retained")
	}
}

func TestGroupOwnedStoreReopenNormalizesWithoutAdmission(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	run, _ := cloneResourceGroupRecord(s.state.Runs[0])
	run.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	run.UpdateSequence++
	next, err := withResourceGroupUpdate(s.state, run)
	if err != nil || s.publish(c, next) != nil {
		t.Fatal("dispatch evidence publication failed")
	}
	beforeFile := s.file
	reopened := c.openResourceGroupStore()
	if reopened.frozen || reopened.firstUse || !reopened.certified || reopened.current(c) != nil || os.SameFile(beforeFile, reopened.file) {
		t.Fatal("owned reopen did not republish and recertify")
	}
	got := reopened.state.Runs[0]
	if got.Admission != resourceGroupAdmissionFinished || got.Evidence.Members[0].Dispatch != resourcegroup.DispatchUnknown || got.Evidence.Members[1].AdmissionStop != resourcegroup.StopRestarted || !got.pinned() {
		t.Fatal("reopen restored admission or lost possible execution")
	}
	if got.InputHash != run.InputHash || !resourceGroupJSONEqual(got.Review, run.Review) || !resourceGroupJSONEqual(got.Origins, run.Origins) {
		t.Fatal("reopen changed original reviewed identity or request")
	}
}

func TestGroupOwnedStoreExistingInvalidStateFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "truncated", "future-version", "wrong-controller", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			path := resourceGroupStatePath(c.dir)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				data = []byte("null")
			case "truncated":
				data = data[:len(data)/2]
			case "future-version":
				data = []byte(strings.Replace(string(data), `"schemaVersion":1`, `"schemaVersion":2`, 1))
			case "wrong-controller":
				data = []byte(strings.Replace(string(data), `"controllerResourceId":"`+c.resourceIdentity+`"`, `"controllerResourceId":"`+strings.Repeat("b", 32)+`"`, 1))
			case "oversized":
				data = []byte(strings.Repeat(" ", resourceGroupMaxStoreBytes+1))
			}
			if kind != "missing" {
				if err := config.AtomicWritePrivate(path, data); err != nil {
					t.Fatal(err)
				}
			}
			reopened := c.openResourceGroupStore()
			if !reopened.frozen || reopened.firstUse || reopened.certified || reopened.current(c) == nil || len(reopened.state.Runs) != 0 {
				t.Fatal("invalid saved state was repaired or acknowledged")
			}
			if kind != "missing" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(data) {
					t.Fatal("failed reopen changed saved evidence")
				}
			}
		})
	}
}

func TestGroupOwnedStoreObservedAbsenceNeverResetsFirstUse(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	path := filepath.Dir(resourceGroupStatePath(c.dir))
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if s.current(c) == nil || !s.frozen || s.firstUse {
		t.Fatal("raced group directory was adopted as first use")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s.current(c) == nil || s.firstUse {
		t.Fatal("removed observed directory reset first use")
	}
}

func TestGroupOwnedStorePrewriteFailureKeepsConfirmedSnapshot(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	before, _ := cloneResourceGroupEnvelope(s.state)
	beforeFile := s.file
	foreign := filepath.Join(filepath.Dir(resourceGroupStatePath(c.dir)), ".sobalink-atomic-v1", "foreign")
	if err := os.WriteFile(foreign, []byte("synthetic recovery blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	run, _ := cloneResourceGroupRecord(s.state.Runs[0])
	run.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	run.UpdateSequence++
	next, err := withResourceGroupUpdate(s.state, run)
	if err != nil {
		t.Fatal(err)
	}
	if s.publish(c, next) == nil || !s.frozen || s.uncertain || !resourceGroupJSONEqual(s.state, before) || !os.SameFile(beforeFile, s.file) {
		t.Fatal("prewrite failure promoted evidence or claimed possible commit")
	}
	if s.current(c) != nil || !s.frozen || s.publish(c, next) == nil {
		t.Fatal("readable prior snapshot cleared the failure latch")
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatal("unknown atomic inventory was removed")
	}
}

func TestGroupOwnedStoreTerminalSaveFailureKeepsDispatchIntent(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	run, _ := cloneResourceGroupRecord(s.state.Runs[0])
	run.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
	run.UpdateSequence++
	dispatching, err := withResourceGroupUpdate(s.state, run)
	if err != nil || s.publish(c, dispatching) != nil {
		t.Fatal("dispatch intent was not saved")
	}
	// Model an unknown completion without fabricating a target response. The
	// source-only fixture sends nothing and the missing response stays unknown.
	completed, err := normalizeResourceGroupEnvelope(dispatching)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(filepath.Dir(resourceGroupStatePath(c.dir)), ".sobalink-atomic-v1", "foreign")
	if err := os.WriteFile(foreign, []byte("synthetic terminal-save blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.publish(c, completed) == nil || !s.frozen || s.uncertain || s.state.Runs[0].Evidence.Members[0].Dispatch != resourcegroup.Dispatching || !s.state.Runs[0].pinned() || s.current(c) != nil {
		t.Fatal("failed terminal publication discarded possible execution")
	}
}

func TestGroupOwnedStoreFailedReopenCannotClaimDecodedDurability(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	groupOwnedAcceptedFixture(t, c, s)
	before := groupOwnedSnapshot(t, c, s)
	foreign := filepath.Join(filepath.Dir(resourceGroupStatePath(c.dir)), ".sobalink-atomic-v1", "foreign")
	if err := os.WriteFile(foreign, []byte("synthetic recertification blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened := c.openResourceGroupStore()
	if !reopened.frozen || reopened.certified || reopened.firstUse || reopened.current(c) == nil || len(reopened.state.Runs) != 0 {
		t.Fatal("failed startup write treated decoded localDurability as proof")
	}
	after := groupOwnedSnapshot(t, c, s)
	if !resourceGroupCandidate(before).matches(after) {
		t.Fatal("failed recertification changed the prior evidence")
	}
}

func TestGroupOwnedStoreCandidateClassifierNeverPromotes(t *testing.T) {
	// This exercises the production failure classifier using real metadata.
	// It does not inject a failure into the composed atomic publication path;
	// actual post-rename sync failure remains separate config/native coverage.
	for _, kind := range []string{"matched-receipt", "missing-receipt", "wrong-receipt", "wrong-digest"} {
		t.Run(kind, func(t *testing.T) {
			c, s := groupOwnedStoreFixture(t)
			groupOwnedAcceptedFixture(t, c, s)
			before, _ := cloneResourceGroupEnvelope(s.state)
			prior := s.confirmedCandidate()
			run, _ := cloneResourceGroupRecord(s.state.Runs[0])
			run.Evidence.Members[0].Dispatch = resourcegroup.Dispatching
			run.UpdateSequence++
			next, err := withResourceGroupUpdate(s.state, run)
			if err != nil {
				t.Fatal(err)
			}
			data := groupStoreJSON(t, next)
			if err := config.AtomicWritePrivate(resourceGroupStatePath(c.dir), data); err != nil {
				t.Fatal(err)
			}
			attempted := groupOwnedSnapshot(t, c, s)
			receipt := attempted.file
			switch kind {
			case "missing-receipt":
				receipt = nil
			case "wrong-receipt":
				receipt = prior.file
			case "wrong-digest":
				attempted.digest = sha256.Sum256([]byte("different attempted bytes"))
			}
			s.failedPublication(prior, attempted, receipt, data, config.ErrAtomicCommitted)
			if !s.frozen || !s.uncertain || !reflect.DeepEqual(s.state, before) || s.publish(c, next) == nil {
				t.Fatal("uncertainty promoted state or resumed publication")
			}
			if kind == "matched-receipt" {
				if s.attempted == nil || s.current(c) != nil || !s.frozen || !s.uncertain || !reflect.DeepEqual(s.state, before) {
					t.Fatal("exact candidate failed disclosure or cleared uncertainty")
				}
				// Replacing it again with identical bytes is not the receipt inode.
				if err := config.AtomicWritePrivate(resourceGroupStatePath(c.dir), data); err != nil {
					t.Fatal(err)
				}
				if s.current(c) == nil {
					t.Fatal("same-byte replacement adopted as attempted self-write")
				}
			} else if s.attempted != nil || s.current(c) == nil {
				t.Fatal("unproven attempted candidate authorized disclosure")
			}
		})
	}
}

func TestGroupOwnedStoreRejectsHistoryReplacementAndPinnedEviction(t *testing.T) {
	before := groupStoreEnvelopeFixture(t, 2, true)
	for _, kind := range []string{"delete-pinned", "rewrite-origin", "rewrite-request", "lower-sequence", "two-updates", "controller"} {
		t.Run(kind, func(t *testing.T) {
			next, _ := cloneResourceGroupEnvelope(before)
			switch kind {
			case "delete-pinned":
				next.Runs = next.Runs[1:]
			case "rewrite-origin":
				next.Runs[0].Origins[0].Relationship.PairBinding = strings.Repeat("f", 64)
				next.Runs[0].UpdateSequence++
			case "rewrite-request":
				next.Runs[0].AcceptedAt--
				next.Runs[0].UpdateSequence++
			case "lower-sequence":
				*next.HighWater--
			case "two-updates":
				next.Runs[0].UpdateSequence++
				next.Runs[1].UpdateSequence++
			case "controller":
				next.ControllerResourceID = strings.Repeat("b", 32)
			}
			if validResourceGroupPublication(before, next) {
				t.Fatal("non-model history publication accepted")
			}
		})
	}
}

func TestGroupOwnedStoreRejectsEmptyFirstPublication(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	if s.publish(c, s.state) == nil || !s.frozen {
		t.Fatal("empty first use allocated a journal")
	}
	if _, err := os.Lstat(filepath.Dir(resourceGroupStatePath(c.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected empty publication created state")
	}
}

func TestGroupOwnedStoreDoesNotUseGeneralWriterBypass(t *testing.T) {
	c, s := groupOwnedStoreFixture(t)
	c.atomicWrite = func(string, []byte) error {
		t.Fatal("group publication used arbitrary writer callback")
		return errors.New("unexpected callback")
	}
	groupOwnedAcceptedFixture(t, c, s)
	lease, err := config.AcquireAtomicWriteLease(resourceGroupStatePath(c.dir))
	if err != nil {
		t.Fatal("group publication retained its writer lease", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if s.current(c) != nil {
		t.Fatal("read-only certification requires a retained lease")
	}
}
