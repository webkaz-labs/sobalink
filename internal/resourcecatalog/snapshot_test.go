package resourcecatalog

import (
	"bytes"
	"strings"
	"testing"
)

func TestCatalogSnapshotDeterministicReplacementAndCache(t *testing.T) {
	l := fixtureLimits()
	services, transfers := fixtureSelection(LocalService), fixtureSelection(TransferActivity)
	serviceRow, transferRow := fixtureRow(t, services, "saved-id"), fixtureRow(t, transfers, "batch-id")
	first := fixtureBuilder(t, l, services, transfers)
	second := fixtureBuilder(t, l, transfers, services)
	for _, page := range []Page{fixturePage(services, serviceRow), fixturePage(transfers, transferRow)} {
		if err := first.AddPage(page); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range []Page{fixturePage(transfers, transferRow), fixturePage(services, serviceRow)} {
		if err := second.AddPage(page); err != nil {
			t.Fatal(err)
		}
	}
	a, b := finish(t, first), finish(t, second)
	if !a.Complete || a.Revision != b.Revision || !bytes.Equal(mustJSON(t, a), mustJSON(t, b)) {
		t.Fatal("snapshot depends on source completion order")
	}
	candidateBuilder := fixtureBuilder(t, l, services, transfers)
	if err := candidateBuilder.AddPage(fixturePage(services)); err != nil {
		t.Fatal(err)
	}
	if err := candidateBuilder.Fail(transfers, "unavailable", fixtureTime); err != nil {
		t.Fatal(err)
	}
	candidate := finish(t, candidateBuilder)
	cache, err := UpdateCache(&a, candidate, l)
	if err != nil || cache.LastComplete == nil || cache.Candidate.Complete {
		t.Fatal("incomplete candidate replaced complete data")
	}
	cache.Candidate.Sources[0].Rows = append(cache.Candidate.Sources[0].Rows, serviceRow)
	if len(candidate.Sources[0].Rows) != 0 {
		t.Fatal("cache aliases candidate")
	}
	changed := transfers
	changed.Epoch = "synthetic-session-2"
	changedBuilder := fixtureBuilder(t, l, services, changed)
	changedCache, err := UpdateCache(&a, finish(t, changedBuilder), l)
	if err != nil || changedCache.LastComplete != nil {
		t.Fatal("old authentication/selection epoch retained")
	}
	completeEmpty := fixtureBuilder(t, l, services, transfers)
	if err := completeEmpty.AddPage(fixturePage(services)); err != nil {
		t.Fatal(err)
	}
	if err := completeEmpty.AddPage(fixturePage(transfers)); err != nil {
		t.Fatal(err)
	}
	replacement, err := UpdateCache(&a, finish(t, completeEmpty), l)
	if err != nil || replacement.LastComplete == nil || len(replacement.LastComplete.Sources[0].Rows) != 0 {
		t.Fatal("full replacement accumulated old rows")
	}
}

func TestCatalogPageRejectsMixedSelectionRevisionAndCursorLoops(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	for name, change := range map[string]func(*Page){
		"revision": func(p *Page) { p.Revision = "different-revision" },
		"epoch":    func(p *Page) { p.Selection.Epoch = "different-epoch" },
		"time":     func(p *Page) { p.CheckedAt++ },
		"state":    func(p *Page) { p.State = "stale" },
		"cursor":   func(p *Page) { p.Cursor = "unknown-cursor" },
		"loop":     func(p *Page) { p.NextCursor = p.Cursor },
		"total":    func(p *Page) { n := int64(2); p.Total = &n },
	} {
		t.Run(name, func(t *testing.T) {
			b := fixtureBuilder(t, l, s)
			first := fixturePage(s, fixtureRow(t, s, "first"))
			first.NextCursor = "first-cursor"
			if err := b.AddPage(first); err != nil {
				t.Fatal(err)
			}
			next := fixturePage(s, fixtureRow(t, s, "second"))
			next.Cursor = first.NextCursor
			change(&next)
			if err := b.AddPage(next); err == nil {
				t.Fatal("mixed page accepted")
			}
			v := finish(t, b).Sources[0]
			if v.State != "invalid" || len(v.Rows) != 0 || v.Complete {
				t.Fatal("invalid source remained selectable")
			}
			if err := b.AddPage(fixturePage(s)); err == nil {
				t.Fatal("invalid source reused")
			}
		})
	}
}

func TestCatalogDuplicateIdentityNamespaces(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	row := fixtureRow(t, s, "same-id")
	b := fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s, row, row)); err == nil {
		t.Fatal("duplicate within page accepted")
	}
	b = fixtureBuilder(t, l, s)
	page := fixturePage(s, row)
	page.NextCursor = "next"
	if err := b.AddPage(page); err != nil {
		t.Fatal(err)
	}
	page.Cursor, page.NextCursor = "next", ""
	if err := b.AddPage(page); err == nil {
		t.Fatal("duplicate across pages accepted")
	}
	alias := s
	alias.SourceID, alias.Epoch = "alias", "different-epoch"
	if _, err := NewBuilder("snapshot", "scope", []Selection{s, alias}, l); err == nil {
		t.Fatal("same source stitched through an alias/epoch")
	}
	remote := fixtureSelection(RemoteService)
	b = fixtureBuilder(t, l, s, remote)
	if err := b.AddPage(fixturePage(s, row)); err != nil {
		t.Fatal(err)
	}
	if err := b.AddPage(fixturePage(remote, fixtureRow(t, remote, "same-id"))); err != nil {
		t.Fatal(err)
	}
	if !finish(t, b).Complete {
		t.Fatal("independent saved/discovery namespaces collided")
	}
	transfer := fixtureSelection(TransferActivity)
	incoming := fixtureRow(t, transfer, "same-batch")
	outgoing, err := ProjectTransfer(transfer, "same-batch", "outgoing", *incoming.TransferActivity, l)
	if err != nil {
		t.Fatal(err)
	}
	b = fixtureBuilder(t, l, transfer)
	if err := b.AddPage(fixturePage(transfer, incoming, outgoing)); err != nil {
		t.Fatal("direction did not scope transfer identity")
	}
}

func TestCatalogIndependentFailureAndHonestEmpty(t *testing.T) {
	l := fixtureLimits()
	services, remote := fixtureSelection(LocalService), fixtureSelection(RemoteSettingsV2)
	for _, outcome := range []string{"unavailable", "unsupported", "invalid", "limited"} {
		b := fixtureBuilder(t, l, services, remote)
		if err := b.AddPage(fixturePage(services)); err != nil {
			t.Fatal(err)
		}
		err := b.Fail(remote, outcome, fixtureTime)
		if err != nil && outcome != "limited" {
			t.Fatal(err)
		}
		snapshot := finish(t, b)
		complete := sourceByKind(t, snapshot, LocalService)
		failed := sourceByKind(t, snapshot, RemoteSettingsV2)
		if snapshot.Complete || !complete.Complete || complete.State != "current" || len(complete.Rows) != 0 || failed.State != outcome || failed.Complete || failed.Total != nil {
			t.Fatal("failure erased completed empty source or invented absence")
		}
	}
	b := fixtureBuilder(t, l, remote)
	if err := b.AddPage(fixturePage(remote)); err == nil {
		t.Fatal("exact settings inspect fabricated a successful empty list")
	}
}

func TestCatalogFiniteBoundsAndFailureReserve(t *testing.T) {
	s := fixtureSelection(LocalService)
	row := fixtureRow(t, s, "first")
	l := fixtureLimits()
	l.MaxRows, l.MaxPageRows = 1, 1
	b := fixtureBuilder(t, l, s)
	page := fixturePage(s, row)
	page.NextCursor = "next"
	if err := b.AddPage(page); err != nil {
		t.Fatal(err)
	}
	next := fixturePage(s, fixtureRow(t, s, "second"))
	next.Cursor = "next"
	if err := b.AddPage(next); err != ErrLimited {
		t.Fatalf("row bound: %v", err)
	}
	v := finish(t, b).Sources[0]
	if v.State != "limited" || len(v.Rows) != 1 || v.Complete || len(Workflows(v, row, fixtureTime, l)) != 0 {
		t.Fatal("row overflow not an explicit bounded prefix")
	}
	l = fixtureLimits()
	l.MaxPages = 1
	b = fixtureBuilder(t, l, s)
	if err := b.AddPage(page); err != ErrLimited {
		t.Fatal("page budget ignored")
	}
	if finish(t, b).Sources[0].State != "limited" {
		t.Fatal("page exhaustion hidden")
	}
	l = fixtureLimits()
	l.MaxPageBytes = 256
	l.MaxStringBytes = 128
	b = fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s, row)); err != ErrLimited {
		t.Fatal("page byte budget ignored")
	}
	if !oneOf(finish(t, b).Sources[0].State, "limited") {
		t.Fatal("byte overflow hidden")
	}
	l = fixtureLimits()
	l.MaxSources = 1
	if _, err := NewBuilder("snapshot", "scope", []Selection{s, fixtureSelection(TransferActivity)}, l); err == nil {
		t.Fatal("source count unbounded")
	}
	l = fixtureLimits()
	l.MaxRemoteTargets = 1
	remote1, remote2 := fixtureSelection(RemoteSettingsV1), fixtureSelection(RemoteSettingsV2)
	remote2.PeerKey = strings.Repeat("a", 64)
	if _, err := NewBuilder("snapshot", "scope", []Selection{remote1, remote2}, l); err == nil {
		t.Fatal("remote target count unbounded")
	}
	l = fixtureLimits()
	l.MaxBytes, l.MaxPageBytes, l.MaxStringBytes = 128, 128, 64
	if _, err := NewBuilder("snapshot", "scope", []Selection{s}, l); err == nil {
		t.Fatal("unreportable framing budget accepted")
	}
	l = fixtureLimits()
	l.MaxStringBytes = 64
	long := fixtureRow(t, s, "long")
	long.LocalService.Name = strings.Repeat("x", 65)
	if _, err := ProjectSavedService(s, "id", *long.LocalService, l); err == nil {
		t.Fatal("unbounded string accepted")
	}
	// Fit exactly the reserved worst-case failure framing plus one source row.
	other := fixtureSelection(TransferActivity)
	probe := fixtureBuilder(t, fixtureLimits(), s, other)
	if err := probe.AddPage(fixturePage(s, row)); err != nil {
		t.Fatal(err)
	}
	budget, err := encodeBounded(probe.budgetSnapshot(), fixtureLimits().MaxBytes, fixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	l = fixtureLimits()
	l.MaxBytes, l.MaxPageBytes, l.MaxStringBytes = len(budget), len(budget), 128
	b = fixtureBuilder(t, l, s, other)
	if err := b.AddPage(fixturePage(s, row)); err != nil {
		t.Fatal(err)
	}
	if err := b.Fail(other, "unavailable", 253402300799000); err != nil {
		t.Fatal(err)
	}
	if got := finish(t, b); len(mustJSON(t, got)) > l.MaxBytes || len(sourceByKind(t, got, LocalService).Rows) != 1 {
		t.Fatal("failure reporting evicted independent completed row")
	}
}

func TestCatalogKnownTotalsAreNotInvented(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	b := fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s)); err != nil {
		t.Fatal(err)
	}
	if finish(t, b).Sources[0].Total != nil {
		t.Fatal("unknown total fabricated")
	}
	for _, total := range []int64{-1, 0, 2, 1 << 53} {
		b := fixtureBuilder(t, l, s)
		page := fixturePage(s, fixtureRow(t, s, "saved-id"))
		page.Total = &total
		if err := b.AddPage(page); err == nil {
			t.Fatal("inconsistent/unsafe total accepted")
		}
	}
	total := int64(1)
	b = fixtureBuilder(t, l, s)
	page := fixturePage(s, fixtureRow(t, s, "saved-id"))
	page.Total = &total
	if err := b.AddPage(page); err != nil {
		t.Fatal(err)
	}
	total = 99
	if got := finish(t, b).Sources[0].Total; got == nil || *got != 1 {
		t.Fatal("total aliases input")
	}
}

func TestCatalogInvalidObservationDoesNotInventTimestamp(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	b := fixtureBuilder(t, l, s)
	page := fixturePage(s)
	page.CheckedAt = 0
	if err := b.AddPage(page); err == nil {
		t.Fatal("malformed observation accepted")
	}
	v := finish(t, b).Sources[0]
	if v.State != "invalid" || v.CheckedAt != 0 {
		t.Fatal("invalid input manufactured a read timestamp")
	}
}

func TestCatalogRetainedHistoryIsStaleAndImmutable(t *testing.T) {
	l := fixtureLimits()
	selections := []Selection{}
	for _, kind := range []string{LocalSettings, RemoteSettingsV1, RemoteSettingsV2, LocalService, RemoteService, TransferActivity} {
		selections = append(selections, fixtureSelection(kind))
	}
	b := fixtureBuilder(t, l, selections...)
	for _, s := range selections {
		page := fixturePage(s, fixtureRow(t, s, "synthetic-resource"))
		total := int64(1)
		page.Total = &total
		if err := b.AddPage(page); err != nil {
			t.Fatal(err)
		}
	}
	previous := finish(t, b)
	previousBytes := mustJSON(t, previous)
	candidate := finish(t, fixtureBuilder(t, l, selections...))
	candidateBytes := mustJSON(t, candidate)
	cache, err := UpdateCache(&previous, candidate, l)
	if err != nil || cache.LastComplete == nil || cache.LastComplete.Validate(l) != nil {
		t.Fatal("historical retention failed")
	}
	if cache.LastComplete.Revision == previous.Revision {
		t.Fatal("derived cache state retained old digest")
	}
	for i, view := range cache.LastComplete.Sources {
		old := previous.Sources[i]
		if view.State != "stale" || view.Revision != old.Revision || view.CheckedAt != old.CheckedAt || view.Complete != old.Complete || !sameTotal(view.Total, old.Total) || !bytes.Equal(mustJSON(t, view.Rows), mustJSON(t, old.Rows)) {
			t.Fatal("historical source observation changed")
		}
		for _, row := range view.Rows {
			if len(Workflows(view, row, fixtureTime, l)) != 0 {
				t.Fatal("retained historical row offered actions")
			}
		}
	}
	if !bytes.Equal(previousBytes, mustJSON(t, previous)) || !bytes.Equal(candidateBytes, mustJSON(t, candidate)) || !bytes.Equal(candidateBytes, mustJSON(t, cache.Candidate)) {
		t.Fatal("retention changed original or candidate")
	}
}
