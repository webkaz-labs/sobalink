package endpointmeta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func TestSnapshotFileRoundTripRetainsEvidence(t *testing.T) {
	s, e, _ := modelFixture(t)
	a := approvalFor(t, e, testNow())
	m, _, err := ProposeReceive(s, e.Update.Issuer, e, &a, testNow())
	if err != nil {
		t.Fatal(err)
	}
	fenced := stage(t, s, m, testNow())
	want, err := EncodeSnapshot(fenced, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "isolated", "snapshot.json")
	published, err := SaveSnapshotFile(path, fenced, len(want))
	if err != nil || !published {
		t.Fatalf("save: published=%v, err=%v", published, err)
	}
	got, err := ReadSnapshotFile(path, len(want))
	if err != nil || !reflect.DeepEqual(got, fenced) {
		t.Fatalf("read changed saved evidence: %v", err)
	}
	if got.PendingChange == nil || SavedEligibility(got, m.PairBinding, testNow()) {
		t.Fatal("read erased pending change or bypassed its model fence")
	}
	if err := got.ValidateAt(testNow().Add(-time.Second)); !errors.Is(err, ErrReview) {
		t.Fatalf("read refreshed saved times: %v", err)
	}
	actual, err := os.ReadFile(path) // Known small synthetic fixture only.
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("file was not canonical: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("snapshot permissions are not private: %v", info.Mode())
	}
}

func TestSnapshotFileReadRejectsMissingInvalidAndOversize(t *testing.T) {
	s, _, _ := modelFixture(t)
	b, err := EncodeSnapshot(s, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	if got, err := ReadSnapshotFile(missing, modelBudget); !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("missing file silently initialized: %v", err)
	}
	for _, budget := range []int{-1, 0} {
		if _, err := ReadSnapshotFile(missing, budget); !errors.Is(err, ErrCapacity) {
			t.Fatalf("budget %d did not fail before I/O: %v", budget, err)
		}
	}
	if _, err := ReadSnapshotFile(dir, modelBudget); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory accepted: %v", err)
	}
	cases := []struct {
		name   string
		data   []byte
		budget int
		want   error
	}{
		{"one-over-budget", b, len(b) - 1, ErrCapacity},
		{"empty", nil, modelBudget, ErrInvalid},
		{"newline", append(bytes.Clone(b), '\n'), modelBudget, ErrInvalid},
		{"trailing-value", append(bytes.Clone(b), []byte("{}")...), modelBudget, ErrInvalid},
		{"duplicate-field", bytes.Replace(b, []byte(`"version":3`), []byte(`"version":3,"version":3`), 1), modelBudget, ErrInvalid},
		{"unknown-field", bytes.Replace(b, []byte(`"version":3`), []byte(`"version":3,"application_grants":[]`), 1), modelBudget, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.json")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSnapshotFile(path, tc.budget)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatalf("read: got nonzero=%v, err=%v, want=%v", !reflect.DeepEqual(got, Snapshot{}), err, tc.want)
			}
		})
	}
	path := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSnapshotFile(path, math.MaxInt); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("maximum-int budget overflowed: %v", err)
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(path, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := ReadSnapshotFile(link, modelBudget); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink accepted: %v", err)
		}
	})
}

type snapshotCountingReader struct {
	io.Reader
	read int
}

func (r *snapshotCountingReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	r.read += n
	return n, err
}

type snapshotErrorReader struct{ err error }

func (r snapshotErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestSnapshotBoundedReadDoesNotTrustStatOrOverflowBudget(t *testing.T) {
	s, _, _ := modelFixture(t)
	b, err := EncodeSnapshot(s, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	r := &snapshotCountingReader{Reader: bytes.NewReader(append(bytes.Clone(b), bytes.Repeat([]byte("x"), 4096)...))}
	if _, err := readSnapshotBounded(r, len(b)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("over-budget stream accepted: %v", err)
	}
	if r.read != len(b)+1 {
		t.Fatalf("read %d bytes, expected budget plus one probe", r.read)
	}
	for _, budget := range []int{-1, 0} {
		r := &snapshotCountingReader{Reader: bytes.NewReader(b)}
		if _, err := readSnapshotBounded(r, budget); !errors.Is(err, ErrCapacity) || r.read != 0 {
			t.Fatalf("invalid budget read input: budget=%d, read=%d, err=%v", budget, r.read, err)
		}
	}
	for _, budget := range []int{len(b), math.MaxInt} {
		if got, err := readSnapshotBounded(bytes.NewReader(b), budget); err != nil || !reflect.DeepEqual(got, s) {
			t.Fatalf("valid bounded read failed for budget %d: %v", budget, err)
		}
	}
	readFailure := errors.New("synthetic read failure")
	for _, prefix := range [][]byte{b[:len(b)/2], b} {
		r := io.MultiReader(bytes.NewReader(prefix), snapshotErrorReader{readFailure})
		if got, err := readSnapshotBounded(r, len(b)); !errors.Is(err, readFailure) || !reflect.DeepEqual(got, Snapshot{}) {
			t.Fatalf("read failure was ignored: %v", err)
		}
	}
}

func TestSnapshotFileSaveValidationDoesNotPublish(t *testing.T) {
	s, _, _ := modelFixture(t)
	b, err := EncodeSnapshot(s, modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if _, err := SaveSnapshotFile(path, s, modelBudget); err != nil {
		t.Fatal(err)
	}
	invalid := cloneSnapshot(s)
	invalid.Version = 2
	for _, tc := range []struct {
		candidate Snapshot
		budget    int
		want      error
	}{{s, 0, ErrCapacity}, {s, -1, ErrCapacity}, {s, len(b) - 1, ErrCapacity}, {invalid, modelBudget, ErrInvalid}} {
		if published, err := SaveSnapshotFile(path, tc.candidate, tc.budget); published || !errors.Is(err, tc.want) {
			t.Fatalf("invalid save: published=%v, err=%v, want=%v", published, err, tc.want)
		}
		got, err := os.ReadFile(path) // Known small synthetic fixture only.
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("invalid save changed destination: %v", err)
		}
		if _, err := saveSnapshotFile(path, tc.candidate, tc.budget, func(string, []byte) error {
			t.Fatal("validation failure reached writer")
			return nil
		}); !errors.Is(err, tc.want) {
			t.Fatalf("validation error changed: %v", err)
		}
	}
	missingParent := filepath.Join(t.TempDir(), "uncreated", "snapshot.json")
	if _, err := SaveSnapshotFile(missingParent, s, 0); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(missingParent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid save created a parent directory: %v", err)
	}
}

func TestSnapshotFileSaveOutcomesPreserveRecovery(t *testing.T) {
	before, _, _ := modelFixture(t)
	candidate := cloneSnapshot(before)
	candidate.Revision = "2"
	cause := errors.New("synthetic sync failure")
	for _, tc := range []struct {
		name      string
		err       error
		published bool
	}{
		{"durable", nil, true},
		{"pre-publication", cause, false},
		{"busy", config.ErrAtomicBusy, false},
		{"inventory-recovery", config.ErrAtomicRecovery, false},
		{"committed", config.ErrAtomicCommitted, true},
		{"wrapped-committed", fmt.Errorf("synthetic wrapper: %w: %w", config.ErrAtomicCommitted, cause), true},
		{"same-error-text-only", errors.New(config.ErrAtomicCommitted.Error()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			published, err := saveSnapshotFile("unused-synthetic-path", candidate, modelBudget, func(path string, b []byte) error {
				calls++
				parsed, parseErr := ParseSnapshot(b, modelBudget)
				if path != "unused-synthetic-path" || parseErr != nil || !reflect.DeepEqual(parsed, candidate) {
					t.Fatalf("writer did not receive the canonical candidate: %v", parseErr)
				}
				return tc.err
			})
			if calls != 1 || published != tc.published || err != tc.err {
				t.Fatalf("outcome changed: calls=%d, published=%v, err=%v", calls, published, err)
			}
			resolved := ResolveSave(before, candidate, published, err)
			if resolved.Published != tc.published || resolved.Recovery != (err != nil) || resolved.Durable != (err == nil) {
				t.Fatalf("incorrect recovery outcome: %+v", resolved)
			}
			want := before
			if published {
				want = candidate
			}
			if !reflect.DeepEqual(resolved.Snapshot, want) {
				t.Fatal("resolver adopted the wrong model")
			}
		})
	}
}

func TestSnapshotFileReopenDoesNotResolveUncertainSave(t *testing.T) {
	before, _, _ := modelFixture(t)
	candidate := cloneSnapshot(before)
	candidate.Revision = "2"
	path := filepath.Join(t.TempDir(), "snapshot.json")
	published, err := saveSnapshotFile(path, candidate, modelBudget, func(path string, b []byte) error {
		if err := config.AtomicWritePrivate(path, b); err != nil {
			t.Fatal(err)
		}
		// Synthetic outcome only; this does not simulate a crash or fsync failure.
		return fmt.Errorf("synthetic uncertain publication: %w", config.ErrAtomicCommitted)
	})
	if !published || !errors.Is(err, config.ErrAtomicCommitted) {
		t.Fatalf("uncertain publication misreported: %v", err)
	}
	resolved := ResolveSave(before, candidate, published, err)
	evidence, err := ReadSnapshotFile(path, modelBudget)
	if err != nil || !reflect.DeepEqual(evidence, candidate) {
		t.Fatalf("candidate evidence unavailable: %v", err)
	}
	if !resolved.Recovery || resolved.Durable {
		t.Fatal("read cleared recovery or asserted durability")
	}
	if _, err := ReexportModel(resolved, candidate.Peers[0].Peer.Key); !errors.Is(err, ErrRecovery) {
		t.Fatalf("uncertain model allowed re-export after reopen: %v", err)
	}
}
