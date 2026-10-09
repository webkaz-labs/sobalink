package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// These fixtures never open Core, create managers, or start transport/processes.
// Atomic publication is injected and the optional manager is an inert value.
func capacityOutcomeFixture(t *testing.T) (*Core, capacity.Policy) {
	t.Helper()
	c := &Core{ctx: context.Background(), dir: t.TempDir(), capacity: capacity.Defaults()}
	proposed := capacity.Defaults()
	proposed.Logical["savedServices"] = capacity.Limited(7)
	return c, proposed
}

func TestCapacityApplyOutcomePublication(t *testing.T) {
	failed := errors.New("synthetic write failure")
	for _, tc := range []struct {
		name      string
		saveErr   error
		published bool
	}{
		{"durable", nil, true}, {"unpublished", failed, false}, {"uncertain", config.ErrAtomicCommitted, true},
	} {
		for _, manager := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/no-manager", true: "/manager"}[manager], func(t *testing.T) {
				c, proposed := capacityOutcomeFixture(t)
				original := c.capacity.Clone()
				if manager {
					c.transfers = &transfer.Manager{}
				}
				writes := 0
				c.atomicWrite = func(path string, data []byte) error {
					writes++
					if path != filepath.Join(c.dir, capacityPolicyFile) {
						t.Fatal("wrong publication target")
					}
					if c.mu.TryLock() {
						c.mu.Unlock()
						t.Fatal("publication did not hold Core lock")
					}
					var saved capacity.Policy
					if err := json.Unmarshal(data, &saved); err != nil || !capacityJSONEqual(saved, proposed) {
						t.Fatal("wrong policy publication", err)
					}
					return tc.saveErr
				}
				got := c.applyCapacityPolicy(proposed)
				if writes != 1 || !got.SaveAttempted || got.SaveErr != tc.saveErr || got.Published != tc.published || got.Err != nil {
					t.Fatalf("wrong publication result: %+v", got)
				}
				wantRuntime := manager && tc.published
				if got.AccountingAttempted != wantRuntime || got.TransferAttempted != wantRuntime || got.AccountingErr != nil || got.TransferErr != nil {
					t.Fatalf("wrong admission result: %+v", got)
				}
				want := original
				if tc.published {
					want = proposed
				}
				if !capacityJSONEqual(c.capacityPolicy(), want) {
					t.Fatal("memory disagrees with publication")
				}
				proposed.Logical["savedServices"] = capacity.Limited(8)
				if c.capacityPolicy().Number("logical", "savedServices") == 8 {
					t.Fatal("published policy aliases proposal")
				}
			})
		}
	}
}

func TestCapacityApplyOutcomeAccountingFailureRetainsSaveError(t *testing.T) {
	c, proposed := capacityOutcomeFixture(t)
	c.transfers = &transfer.Manager{}
	if err := c.transfers.Close(); err != nil {
		t.Fatal(err)
	}
	c.atomicWrite = func(string, []byte) error { return config.ErrAtomicCommitted }
	got := c.applyCapacityPolicy(proposed)
	if !got.Published || !got.SaveAttempted || !errors.Is(got.SaveErr, config.ErrAtomicCommitted) || !got.AccountingAttempted || !errors.Is(got.AccountingErr, transfer.ErrClosed) || got.TransferAttempted || got.TransferErr != nil || got.Err != nil {
		t.Fatalf("wrong partial outcome: %+v", got)
	}
	if got.legacyError() != transfer.ErrClosed {
		t.Fatal("legacy accounting error precedence changed")
	}
}

func TestCapacityApplyOutcomePreSaveRejections(t *testing.T) {
	for _, tc := range []string{"invalid-policy", "closing", "cancelled", "private-settings", "profile", "messages", "lan", "direct-lan"} {
		t.Run(tc, func(t *testing.T) {
			c, proposed := capacityOutcomeFixture(t)
			switch tc {
			case "invalid-policy":
				proposed.Version = -1
			case "closing":
				c.closing = true
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				c.ctx = ctx
			case "private-settings":
				if err := os.Mkdir(filepath.Join(c.dir, "favorites.json"), 0700); err != nil {
					t.Fatal(err)
				}
			case "profile":
				proposed.Resources["profileBytes"] = capacity.Limited(1)
			case "messages":
				proposed.Resources["messageStorageBytes"] = capacity.Limited(1)
				if err := os.WriteFile(filepath.Join(c.dir, "messages.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lan":
				proposed.Resources["lanStateBytes"] = capacity.Limited(1)
				c.lan = &lanStore{path: filepath.Join(c.dir, "lan.json")}
			case "direct-lan":
				proposed.Resources["lanStateBytes"] = capacity.Limited(1)
				c.directLAN = &directLANStore{path: filepath.Join(c.dir, "direct-lan.json"), state: directLANState{Version: directLANStateVersion}}
			}
			c.transfers = &transfer.Manager{}
			c.atomicWrite = func(string, []byte) error { t.Fatal("rejected policy reached write"); return nil }
			got := c.applyCapacityPolicy(proposed)
			if got.Err == nil || got.SaveAttempted || got.SaveErr != nil || got.Published || got.AccountingAttempted || got.TransferAttempted || got.AccountingErr != nil || got.TransferErr != nil {
				t.Fatalf("wrong rejected outcome: %+v", got)
			}
			switch tc {
			case "profile", "messages", "lan", "direct-lan":
				if networkErrorCode(got.Err) != "policy_in_use" {
					t.Fatal("expected retained-state guard", got.Err)
				}
			case "private-settings":
				if networkErrorCode(got.Err) != "private_settings_unavailable" {
					t.Fatal("expected private-settings guard", got.Err)
				}
			}
			if !capacityJSONEqual(c.capacityPolicy(), capacity.Defaults()) {
				t.Fatal("rejected proposal changed memory")
			}
		})
	}
}

func TestCapacityApplyOutcomeLegacyErrorPrecedence(t *testing.T) {
	pre := errors.New("pre-save rejection")
	save := config.ErrAtomicCommitted
	runtime := transfer.ErrClosed
	for _, tc := range []struct {
		name    string
		outcome capacityApplyOutcome
		want    []error
		absent  []error
	}{
		{"rejected", capacityApplyOutcome{Err: pre}, []error{pre}, nil},
		{"unpublished", capacityApplyOutcome{SaveAttempted: true, SaveErr: config.ErrAtomicBusy}, []error{config.ErrAtomicBusy}, nil},
		{"accounting", capacityApplyOutcome{Published: true, SaveErr: save, AccountingErr: runtime}, []error{runtime}, []error{save}},
		{"transfer", capacityApplyOutcome{Published: true, SaveErr: save, TransferErr: runtime}, []error{runtime, save}, nil},
		{"uncertain", capacityApplyOutcome{Published: true, SaveErr: save}, []error{save}, nil},
		{"durable", capacityApplyOutcome{Published: true}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.outcome.legacyError()
			if len(tc.want) == 0 && err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Fatal("missing legacy error", want, err)
				}
			}
			for _, absent := range tc.absent {
				if errors.Is(err, absent) {
					t.Fatal("unexpected legacy error", absent, err)
				}
			}
		})
	}
}

func TestCapacityApplyOutcomeLegacyCommand(t *testing.T) {
	for _, tc := range []string{"durable", "uncertain", "unpublished", "accounting-failed"} {
		t.Run(tc, func(t *testing.T) {
			c, proposed := capacityOutcomeFixture(t)
			var saveErr error
			if tc == "uncertain" || tc == "accounting-failed" {
				saveErr = config.ErrAtomicCommitted
			}
			if tc == "unpublished" {
				saveErr = config.ErrAtomicBusy
			}
			if tc == "accounting-failed" {
				c.transfers = &transfer.Manager{}
				_ = c.transfers.Close()
			}
			writes := 0
			c.atomicWrite = func(string, []byte) error { writes++; return saveErr }
			raw, _ := json.Marshal(map[string]any{"policy": proposed})
			preview, err := c.capacityCommand("policy.preview", raw)
			if err != nil || writes != 0 {
				t.Fatal("preview changed behavior", err)
			}
			raw, _ = json.Marshal(map[string]any{"policy": proposed, "expectedRevision": preview.(map[string]any)["revision"]})
			got, err := c.capacityCommand("policy.apply", raw)
			wantErr := saveErr
			if tc == "accounting-failed" {
				wantErr = transfer.ErrClosed
			}
			if writes != 1 || err != wantErr {
				t.Fatal("legacy apply error changed", writes, err, wantErr)
			}
			wantView := tc == "durable" || tc == "uncertain"
			if (got != nil) != wantView {
				t.Fatal("legacy apply view changed")
			}
		})
	}
}

func TestCapacityApplyOutcomeLANPublicationOrdering(t *testing.T) {
	for _, saveErr := range []error{nil, config.ErrAtomicBusy, config.ErrAtomicCommitted} {
		t.Run(map[error]string{nil: "durable", config.ErrAtomicBusy: "unpublished", config.ErrAtomicCommitted: "uncertain"}[saveErr], func(t *testing.T) {
			c, proposed := capacityOutcomeFixture(t)
			proposed.Logical["trustedPeers"] = capacity.Limited(7)
			c.lan = &lanStore{path: filepath.Join(c.dir, "lan.json")}
			c.directLAN = &directLANStore{path: filepath.Join(c.dir, "direct-lan.json"), state: directLANState{Version: directLANStateVersion}}
			c.atomicWrite = func(string, []byte) error {
				if c.lan.mu.TryLock() {
					c.lan.mu.Unlock()
					t.Fatal("relay LAN lock missing during publication")
				}
				if c.directLAN.mu.TryLock() {
					c.directLAN.mu.Unlock()
					t.Fatal("direct LAN lock missing during publication")
				}
				if c.lan.limits.Load() != nil || c.directLAN.limits.Load() != nil {
					t.Fatal("LAN limits changed before publication")
				}
				return saveErr
			}
			got := c.applyCapacityPolicy(proposed)
			if got.Err != nil || got.SaveErr != saveErr || !got.SaveAttempted {
				t.Fatalf("wrong publication outcome: %+v", got)
			}
			for _, limits := range []*lanStoreLimits{c.lan.limits.Load(), c.directLAN.limits.Load()} {
				if got.Published {
					if limits == nil || limits.peers != 7 {
						t.Fatal("published LAN limits missing")
					}
				} else if limits != nil {
					t.Fatal("unpublished LAN limits changed")
				}
			}
		})
	}
}
