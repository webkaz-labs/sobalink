package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func catalogLocalFixture(t *testing.T, kind string) (resourcecatalog.Selection, resourcecatalog.Limits, *resourceCatalogBudget) {
	t.Helper()
	l, err := resourceCatalogPinnedLimits(capacity.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	s := resourcecatalog.Selection{SourceID: kind, Kind: kind, Epoch: "synthetic-epoch"}
	if kind == resourcecatalog.TransferActivity {
		s.ProcessID = "synthetic-process"
	}
	return s, l, &resourceCatalogBudget{rows: l.MaxRows, bytes: l.MaxBytes}
}

func TestResourceCatalogLocalLifetimePairsAndTTL(t *testing.T) {
	for _, tc := range []struct {
		direction, lifetime string
		ttl                 int
		valid               bool
	}{
		{"forward", "until-stopped", 0, true}, {"share", "until-revoked", 0, true},
		{"share", "until-stopped", 0, false}, {"forward", "until-revoked", 0, false},
		{"forward", "until-stopped", 1, false}, {"share", "until-revoked", 1, false},
		{"forward", "finite", 60, true}, {"share", "", 60, true}, {"share", "finite", 0, false},
	} {
		if catalogServiceLifetime(tc.direction, tc.lifetime, tc.ttl) != tc.valid {
			t.Fatal("lifetime/TTL mismatch", tc)
		}
	}
}

func TestResourceCatalogSavedServiceAllowlistAndRuntimeState(t *testing.T) {
	s, l, budget := catalogLocalFixture(t, resourcecatalog.LocalService)
	spec := ServiceSpec{ID: "synthetic-service", Name: "Synthetic service", Direction: "forward", Network: "tcp", Ports: "8080", Lifetime: "until-stopped", TTLSeconds: 0, PeerID: "synthetic-private-peer", LoopbackHost: "synthetic-private-host", ServiceRevision: "synthetic-private-review"}
	c := &Core{ctx: context.Background(), profile: Profile{Services: []ServiceSpec{spec}}, serviceStates: map[string]string{spec.ID: "failed"}}
	captured := c.resourceCatalogSavedServices(context.Background(), s, l, budget)
	if captured.State != "current" || len(captured.Rows) != 1 || captured.Rows[0].LocalService.State != "failed" || captured.Rows[0].LocalService.Lifetime != "until-stopped" {
		t.Fatal("saved source was not represented")
	}
	raw, err := json.Marshal(captured.Rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"synthetic-private-", "peerId", "loopbackHost", "endpoint", "owner", "ttlSeconds", "serviceRevision"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("private field escaped", forbidden)
		}
	}
	effective, err := ranges.Parse("8080-8081")
	if err != nil {
		t.Fatal(err)
	}
	a := &activeService{spec: spec, effective: effective, error: "synthetic-private-error"}
	c.active = map[string]*activeService{spec.ID: a}
	captured = c.resourceCatalogSavedServices(context.Background(), s, l, &resourceCatalogBudget{rows: 10, bytes: l.MaxBytes})
	if captured.State != "current" || captured.Rows[0].LocalService.Ports != "8080-8081" || captured.Rows[0].LocalService.State != "failed" {
		t.Fatal("runtime scalar precedence changed")
	}
}

func TestResourceCatalogSavedServicePreallocationLimits(t *testing.T) {
	s, l, _ := catalogLocalFixture(t, resourcecatalog.LocalService)
	spec := ServiceSpec{ID: "synthetic", Name: strings.Repeat("x", 2000), Direction: "share", Network: "tcp", Ports: "80", Lifetime: "finite", TTLSeconds: 60}
	c := &Core{ctx: context.Background(), profile: Profile{Services: []ServiceSpec{spec}}}
	for _, b := range []*resourceCatalogBudget{{rows: 0, bytes: 10000}, {rows: 1, bytes: 1100}} {
		got := c.resourceCatalogSavedServices(context.Background(), s, l, b)
		if got.State != "limited" || len(got.Rows) != 0 {
			t.Fatal("oversize source was copied")
		}
	}
	c.profile.Services = nil
	got := c.resourceCatalogSavedServices(context.Background(), s, l, &resourceCatalogBudget{rows: 0, bytes: 0})
	if got.State != "current" || got.Total == nil || *got.Total != 0 || got.Rows == nil {
		t.Fatal("genuine empty source misclassified")
	}
}

func TestResourceCatalogTransferStateMapping(t *testing.T) {
	for _, state := range []transfer.BatchState{transfer.Pending, transfer.Accepted, transfer.Receiving, transfer.Partial, transfer.Completed, transfer.Cancelled, transfer.Rejected} {
		if _, ok := resourceCatalogIncomingState(state); !ok {
			t.Fatal("known incoming state rejected", state)
		}
	}
	if _, ok := resourceCatalogIncomingState("new-unknown-state"); ok {
		t.Fatal("unknown state guessed complete")
	}
	if resourceCatalogOutgoingState("new-unknown-state") {
		t.Fatal("unknown outgoing state accepted")
	}
}

// This constructor has nil policy/accounting stores. Source inspection of
// NewManager confirms it creates only in-memory state, without I/O or goroutines.
func TestResourceCatalogOutgoingOriginalIDsAndScalarBytes(t *testing.T) {
	s, l, budget := catalogLocalFixture(t, resourcecatalog.TransferActivity)
	manager, err := transfer.NewManager(transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := &outgoingBatch{ID: "synthetic-batch", PeerID: "synthetic-peer", State: "transferring", Completed: 3, Manifest: transfer.Manifest{Entries: []transfer.Entry{{Path: "synthetic-private-path", Size: 7}}}, Spool: "synthetic-private-spool", Error: "synthetic-private-error"}
	c := &Core{ctx: context.Background(), transfers: manager, outgoing: map[string]*outgoingBatch{b.ID: b}}
	got := c.resourceCatalogTransfers(context.Background(), s, l, budget)
	if got.State != "current" || len(got.Rows) != 1 || got.Rows[0].Identity.ID != b.ID || got.Rows[0].Identity.Direction != "outgoing" || got.Rows[0].TransferActivity.TotalBytes != 7 || got.Rows[0].TransferActivity.CompletedBytes != 3 {
		t.Fatal("original transfer identity or scalar bytes changed")
	}
	raw, _ := json.Marshal(got.Rows)
	if strings.Contains(string(raw), "synthetic-private") || strings.Contains(string(raw), "outgoing:") {
		t.Fatal("private metadata or compound key escaped")
	}
	b.Manifest.Entries[0].Size = capacity.MaxJSONInteger
	b.Manifest.Entries = append(b.Manifest.Entries, transfer.Entry{Size: 1})
	got = c.resourceCatalogTransfers(context.Background(), s, l, &resourceCatalogBudget{rows: 10, bytes: l.MaxBytes})
	if got.State != "invalid" || len(got.Rows) != 0 {
		t.Fatal("unsafe byte sum accepted")
	}
}
