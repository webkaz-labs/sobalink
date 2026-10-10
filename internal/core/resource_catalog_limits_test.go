package core

import (
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func TestResourceCatalogFiniteFramingAndTinyBudget(t *testing.T) {
	p := capacity.Defaults()
	l, err := resourceCatalogPinnedLimits(p)
	if err != nil {
		t.Fatal(err)
	}
	overhead, err := resourceCatalogEnvelopeAllowance(l)
	if err != nil || int64(l.MaxBytes)+overhead > p.Number("resources", "pageBytes") || l.MaxRows != int(p.Number("resources", "pageEntries")) || l.MaxSources != 5 || l.MaxRemoteTargets != 1 || l.MaxPages != 1 {
		t.Fatal("incorrect finite envelope", err)
	}
	if _, err := resourceCatalogLimits(p, p.Number("resources", "pageBytes")); err == nil {
		t.Fatal("zero framing accepted")
	}
	if _, err := resourceCatalogLimits(p, -1); err == nil {
		t.Fatal("negative framing accepted")
	}
	tiny := l
	tiny.MaxBytes = 1
	tiny.MaxPageBytes = 1
	tiny.MaxStringBytes = 1
	if _, err := resourceCatalogBudgetFor("x", "y", []resourcecatalog.Selection{{SourceID: "a", Kind: resourcecatalog.LocalService, Epoch: "e"}}, tiny); err == nil {
		t.Fatal("tiny failure framing accepted")
	}
}

func TestResourceCatalogAggregateCaptureBudget(t *testing.T) {
	b := &resourceCatalogBudget{rows: 2, bytes: 2200}
	if !b.reserve("synthetic") || !b.reserve("synthetic") || b.reserve("third") {
		t.Fatal("aggregate row/byte budget reset")
	}
	b = &resourceCatalogBudget{rows: 4, bytes: 2000}
	before := *b
	if b.reserve(strings.Repeat("x", 1000)) || *b != before {
		t.Fatal("oversize copied or partially charged")
	}
	if b.reserveWithExtra(-1) || b.reserveWithExtra(int(^uint(0)>>1)) {
		t.Fatal("overflow or negative estimate accepted")
	}
}
