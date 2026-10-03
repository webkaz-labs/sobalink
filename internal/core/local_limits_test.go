package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestLocalBudgetsUseSelectedResources(t *testing.T) {
	p := capacity.Defaults()
	p.Logical["messageBytes"] = capacity.Unlimited()
	p.Resources["messageTextBytes"] = capacity.Limited(16 << 20)
	p.Resources["profileBytes"] = capacity.Limited(16 << 20)
	p.Resources["transferMetadataBytes"] = capacity.Limited(32 << 20)
	c := &Core{capacity: p}
	limits := c.LocalControlLimits()
	bounds, err := messageframe.ForText(c.MessageTextBytes())
	if err != nil || limits.CommandBytes < bounds.CommandBytes || limits.RequestBytes < bounds.ControlRequestBytes || limits.ResponseBytes < 12*(32<<20) || limits.Validate() != nil {
		t.Fatalf("selected budgets not covered: %+v %v", limits, err)
	}
	for key := range p.Resources {
		p.Resources[key] = capacity.Limited(capacity.MaxJSONInteger)
	}
	p.Logical["messageBytes"] = capacity.Unlimited()
	limits, err = localLimitsFor(p)
	if err != nil || limits.Validate() != nil {
		t.Fatalf("largest representable resource settings overflow: %+v %v", limits, err)
	}
}

func TestLocalBudgetRetainsOutgoingHistoryAfterAdmissionReduction(t *testing.T) {
	p := capacity.Defaults()
	p.Resources["transferMetadataBytes"] = capacity.Limited(1)
	manifest := transfer.Manifest{ID: "retained", Entries: []transfer.Entry{{ID: "file", Path: strings.Repeat("&", 100<<10)}}}
	c := &Core{capacity: p, outgoing: map[string]*outgoingBatch{"retained": {Manifest: manifest}}}
	empty, err := localLimitsFor(p)
	if err != nil {
		t.Fatal(err)
	}
	got := c.LocalControlLimits()
	if got.ResponseBytes-empty.ResponseBytes < 12*transfer.ManifestMetadataBytes(manifest) {
		t.Fatal("lowered admission hid retained escaped status paths")
	}
	// Status repeats paths in stored names, so sixfold JSON expansion alone
	// would be insufficient for a retained incoming file.
	status, _ := json.Marshal(transfer.FileStatus{Entry: manifest.Entries[0], StoredName: manifest.Entries[0].Path})
	if int64(len(status)) >= got.ResponseBytes {
		t.Fatal("status no longer fits retained response envelope")
	}
}

func TestReadLocalBudgetsReadsOnlySmallPolicyFile(t *testing.T) {
	dir := t.TempDir()
	p := capacity.Defaults()
	p.Resources["profileBytes"] = capacity.Limited(8 << 20)
	if err := config.WriteJSON(filepath.Join(dir, capacityPolicyFile), p); err != nil {
		t.Fatal(err)
	}
	// The saved profile is deliberately unreadable JSON; it must not be read
	// merely to obtain transport budgets, nor may its content leak in errors.
	if err := os.WriteFile(filepath.Join(dir, "sobalink.json"), []byte("private-invalid-profile"), 0600); err != nil {
		t.Fatal(err)
	}
	if limits, err := ReadLocalControlLimits(dir); err != nil || limits.CommandBytes < 6*(8<<20) {
		t.Fatalf("selected profile budget not usable independently: %+v %v", limits, err)
	}
	if err := os.WriteFile(filepath.Join(dir, capacityPolicyFile), []byte(strings.Repeat(" ", 65<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocalControlLimits(dir); err == nil {
		t.Fatal("unbounded or invalid capacity file accepted")
	}
}
