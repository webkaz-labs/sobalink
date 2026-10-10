package resourcecatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

const fixtureID = "0123456789abcdef0123456789abcdef"
const fixtureGrant = "abcdef0123456789abcdef0123456789"
const fixtureDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const fixtureTime int64 = 1700000000000

func fixtureLimits() Limits {
	return Limits{MaxSources: 16, MaxRemoteTargets: 16, MaxRows: 32, MaxPageRows: 8, MaxPages: 8, MaxBytes: 64 * 1024, MaxPageBytes: 16 * 1024, MaxStringBytes: 4096}
}
func fixtureSelection(kind string) Selection {
	s := Selection{SourceID: kind, Kind: kind, Epoch: "synthetic-session-1"}
	if oneOf(kind, LocalSettings, RemoteSettingsV1, RemoteSettingsV2) {
		s.Target = resource.Target{SchemaVersion: 1, ResourceID: fixtureID}
	}
	if oneOf(kind, RemoteSettingsV1, RemoteSettingsV2) {
		s.PeerKey, s.GrantID, s.GrantRevision = fixtureDigest, fixtureGrant, 1
	}
	if kind == RemoteService {
		s.PeerKey = "synthetic-peer"
	}
	if kind == TransferActivity {
		s.ProcessID = "synthetic-process-1"
	}
	return s
}
func fixtureValues() SettingsValues {
	return SettingsValues{Requested: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(2)}, Effective: resource.Effective{TransferConcurrentFiles: 4, TransferConcurrentPerPeer: 2}}
}
func fixtureDescriptor() resource.Descriptor {
	v := fixtureValues()
	return resource.Descriptor{Target: fixtureSelection(LocalSettings).Target, Type: resource.Type, Authority: "local", Provider: "local", Operations: []string{"list", "inspect", "preview", "apply", "operation.status"}, Revision: fixtureDigest, Requested: v.Requested, Effective: v.Effective}
}
func fixtureRow(t *testing.T, s Selection, id string) Row {
	t.Helper()
	l, v := fixtureLimits(), fixtureValues()
	var row Row
	var err error
	switch s.Kind {
	case LocalSettings:
		row, err = ProjectLocalSettings(s, fixtureDescriptor(), l)
	case RemoteSettingsV1:
		row, err = ProjectInspectionV1(s, InspectionV1{1, s.Target, v.Requested, v.Effective}, l)
	case RemoteSettingsV2:
		row, err = ProjectInspectionV2(s, InspectionV2{2, s.Target, s.GrantID, s.GrantRevision, "inspect", v}, l)
	case LocalService:
		row, err = ProjectSavedService(s, id, SavedService{Name: "Synthetic private service", Direction: "share", Network: "tcp", Ports: "8080", Lifetime: "finite", State: "saved", Application: "unverified"}, l)
	case RemoteService:
		row, err = ProjectSharedService(s, id, SharedService{Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "finite", ExpiresAt: fixtureTime + 60000, Application: "unverified", ReviewRevision: "synthetic-discovery-review"}, l)
	case TransferActivity:
		row, err = ProjectTransfer(s, id, "incoming", Transfer{PeerID: "synthetic-peer", TotalBytes: 20, CompletedBytes: 5, State: "transferring"}, l)
	}
	if err != nil {
		t.Fatalf("fixture projection: %v", err)
	}
	return row
}
func fixturePage(s Selection, rows ...Row) Page {
	return Page{Selection: s, State: "current", CheckedAt: fixtureTime, Revision: "synthetic-revision-1", Rows: append([]Row{}, rows...)}
}
func fixtureBuilder(t *testing.T, l Limits, selections ...Selection) *Builder {
	t.Helper()
	b, err := NewBuilder("synthetic-snapshot-1", "synthetic-scope", selections, l)
	if err != nil {
		t.Fatalf("new builder: %v", err)
	}
	return b
}
func finish(t *testing.T, b *Builder) Snapshot {
	t.Helper()
	v, err := b.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return v
}
func sourceByKind(t *testing.T, s Snapshot, kind string) SourceView {
	t.Helper()
	for _, source := range s.Sources {
		if source.Selection.Kind == kind {
			return source
		}
	}
	t.Fatalf("missing source %s", kind)
	return SourceView{}
}

func TestCatalogProjectionClosedArmsAndIdentityLifetime(t *testing.T) {
	l := fixtureLimits()
	for _, kind := range []string{LocalSettings, RemoteSettingsV1, RemoteSettingsV2, LocalService, RemoteService, TransferActivity} {
		t.Run(kind, func(t *testing.T) {
			s := fixtureSelection(kind)
			r := fixtureRow(t, s, "synthetic-resource")
			expected := Persistent
			if kind == RemoteService {
				expected = Activation
			}
			if kind == TransferActivity {
				expected = Process
			}
			if r.Identity.Lifetime != expected || r.Validate(s, l) != nil {
				t.Fatal("wrong identity lifetime")
			}
			for _, other := range []string{LocalSettings, RemoteSettingsV1, RemoteSettingsV2, LocalService, RemoteService, TransferActivity} {
				if other != kind && r.Validate(fixtureSelection(other), l) == nil {
					t.Fatalf("arm reused as %s", other)
				}
			}
			r.Identity.Lifetime = "unknown"
			if r.Validate(s, l) == nil {
				t.Fatal("unknown lifetime accepted")
			}
		})
	}
	s := fixtureSelection(LocalService)
	r := fixtureRow(t, s, "synthetic-resource")
	v := fixtureValues()
	r.RemoteSettingsV2 = &v
	if r.Validate(s, l) == nil {
		t.Fatal("mixed arms accepted")
	}
	s.Kind = "arbitrary_provider"
	if s.Validate(l) == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestCatalogRemoteProjectionAllowlistAndBinding(t *testing.T) {
	l, v := fixtureLimits(), fixtureValues()
	s := fixtureSelection(RemoteSettingsV2)
	response := InspectionV2{2, s.Target, s.GrantID, s.GrantRevision, "inspect", v}
	for _, change := range []func(*InspectionV2){
		func(r *InspectionV2) { r.ProtocolVersion = 1 },
		func(r *InspectionV2) { r.Target.ResourceID = fixtureGrant },
		func(r *InspectionV2) { r.GrantID = fixtureID },
		func(r *InspectionV2) { r.GrantRevision++ },
		func(r *InspectionV2) { r.Action = "apply" },
	} {
		bad := response
		change(&bad)
		if _, err := ProjectInspectionV2(s, bad, l); err == nil {
			t.Fatal("mismatched reply accepted")
		}
	}
	remote := fixtureRow(t, fixtureSelection(RemoteService), "activation-id")
	data, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Synthetic private service", `"name"`, `"target"`, `"owner"`, `"peerIds"`, `"storedPath"`, `"authority"`, `"provider"`, `"journal"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("remote projection contains %s", forbidden)
		}
	}
	local := fixtureRow(t, fixtureSelection(LocalService), "saved-id")
	if !strings.Contains(string(mustJSON(t, local)), "Synthetic private service") {
		t.Fatal("local label missing")
	}
	remoteSettings := fixtureRow(t, s, "unused")
	for _, forbidden := range []string{`"authority"`, `"provider"`, `"revision"`, `"operations"`, `"journal"`} {
		if strings.Contains(string(mustJSON(t, remoteSettings)), forbidden) {
			t.Fatalf("private settings field %s", forbidden)
		}
	}
}

func TestCatalogSettingsValuesAndInputImmutability(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalSettings)
	original := fixtureDescriptor()
	row, err := ProjectLocalSettings(s, original, l)
	if err != nil {
		t.Fatal(err)
	}
	*original.Requested.TransferConcurrentPerPeer.Value = 9
	original.Operations[0] = "mutated"
	if *row.LocalSettings.Requested.TransferConcurrentPerPeer.Value != 2 || row.LocalSettings.Operations[0] != "list" {
		t.Fatal("projection aliases mutable input")
	}
	for _, choice := range []capacity.Choice{capacity.Unlimited(), capacity.Limited(0), capacity.Limited(-1), capacity.Limited(capacity.MaxJSONInteger + 1), {Mode: "default", Value: new(int64)}} {
		bad := fixtureDescriptor()
		bad.Requested.TransferConcurrentFiles = choice
		if _, err := ProjectLocalSettings(s, bad, l); err == nil {
			t.Fatal("invalid transfer choice accepted")
		}
	}
	bad := fixtureDescriptor()
	bad.Effective.TransferConcurrentFiles = 0
	if _, err := ProjectLocalSettings(s, bad, l); err == nil {
		t.Fatal("zero effective value accepted")
	}
}

func TestCatalogTransferOriginalIdentityAndWorkflow(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(TransferActivity)
	row := fixtureRow(t, s, "original-batch")
	b := fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s, row)); err != nil {
		t.Fatal(err)
	}
	v := finish(t, b).Sources[0]
	workflows := Workflows(v, row, fixtureTime, l)
	if len(workflows) != 1 || workflows[0].Kind != "open_transfer" || workflows[0].ID != "original-batch" || workflows[0].Direction != "incoming" {
		t.Fatal("wrong transfer workflow identity")
	}
	if _, err := ProjectTransfer(s, "incoming:original-batch", "incoming", *row.TransferActivity, l); err == nil {
		t.Fatal("compound page ID accepted as transfer ID")
	}
	if _, err := ProjectTransfer(s, "original-batch", "other", *row.TransferActivity, l); err == nil {
		t.Fatal("unknown transfer direction accepted")
	}
	if strings.Contains(string(mustJSON(t, row)), "path") {
		t.Fatal("activity leaks file paths")
	}
}

func TestCatalogWorkflowFreshnessAndReadOnlyInspection(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(RemoteService)
	row := fixtureRow(t, s, "activation-id")
	b := fixtureBuilder(t, l, s)
	if err := b.AddPage(fixturePage(s, row)); err != nil {
		t.Fatal(err)
	}
	v := finish(t, b).Sources[0]
	for _, now := range []int64{fixtureTime - 5000, fixtureTime, fixtureTime + 15000} {
		if refs := Workflows(v, row, now, l); len(refs) != 1 || refs[0].ReviewRevision != row.RemoteService.ReviewRevision {
			t.Fatal("fresh review unavailable")
		}
	}
	for _, now := range []int64{fixtureTime - 5001, fixtureTime + 15001, fixtureTime + 60000} {
		if len(Workflows(v, row, now, l)) != 0 {
			t.Fatal("stale/future/expired discovery gained action")
		}
	}
	v.State = "stale"
	if len(Workflows(v, row, fixtureTime, l)) != 0 {
		t.Fatal("stale action enabled")
	}
	for _, kind := range []string{RemoteSettingsV1, RemoteSettingsV2, LocalSettings} {
		s := fixtureSelection(kind)
		row := fixtureRow(t, s, "unused")
		b := fixtureBuilder(t, l, s)
		if err := b.AddPage(fixturePage(s, row)); err != nil {
			t.Fatal(err)
		}
		refs := Workflows(finish(t, b).Sources[0], row, fixtureTime, l)
		expected := 2
		if kind == RemoteSettingsV1 {
			expected = 1
		}
		if len(refs) != expected || refs[0].Kind != "inspect_settings" {
			t.Fatal("wrong settings workflow scope")
		}
		for _, ref := range refs {
			if strings.Contains(ref.Kind, "apply") {
				t.Fatal("catalog invented apply capability")
			}
		}
	}
}

func TestCatalogHistoricalResultsStaySeparate(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalSettings)
	outcome := resource.UnknownOutcome()
	localID := resource.OperationID(fixtureID, fixtureGrant, 1)
	local, err := ProjectLocalResult(s, resource.Operation{Target: s.Target, OperationID: localID, Outcome: outcome, EvidenceDurable: false, Current: fixtureDescriptor(), Journal: resource.JournalUsage{Records: 3}}, l)
	if err != nil {
		t.Fatal(err)
	}
	encoded := mustJSON(t, local)
	for _, forbidden := range []string{`"current"`, `"journal"`, `"requested"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("current state mixed into evidence")
		}
	}
	s = fixtureSelection(RemoteSettingsV2)
	durable := false
	remote, err := ProjectRemoteResult(s, fixtureDigest, fixtureDigest, outcome, &durable, l)
	if err != nil || remote.EvidenceDurable == nil || *remote.EvidenceDurable {
		t.Fatal("false durability lost")
	}
	durable = true
	if *remote.EvidenceDurable {
		t.Fatal("result aliases caller durability")
	}
	if _, err := ProjectRemoteResult(s, fixtureDigest, localID, outcome, &durable, l); err == nil {
		t.Fatal("local ID accepted remotely")
	}
	if _, err := ProjectRemoteResult(s, fixtureDigest, fixtureDigest, outcome, nil, l); err == nil {
		t.Fatal("missing durability accepted")
	}
	for _, bad := range []string{strings.Replace(string(encoded), `"evidenceDurable":false`, `"evidenceDurable":null`, 1), strings.Replace(string(encoded), `,"evidenceDurable":false`, ``, 1)} {
		if _, err := DecodeResult([]byte(bad), l); err == nil {
			t.Fatal("ambiguous durability decoded")
		}
	}
	if !reflect.DeepEqual(remote.Outcome, resource.UnknownOutcome()) {
		t.Fatal("unknown attributed from settings")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCatalogUnavailableEvidenceNeverBecomesOutcome(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(RemoteSettingsV2)
	value := ResultObservation{Selection: s, OperationID: fixtureDigest, CheckedAt: fixtureTime, State: "unavailable"}
	read, err := DecodeResultObservation(mustJSON(t, value), l)
	if err != nil || read.Result != nil || read.State != "unavailable" {
		t.Fatal("unavailable evidence changed meaning")
	}
	for _, state := range []string{"absent", "evicted", "failed", "not_executed", "observed"} {
		bad := value
		bad.State = state
		if bad.Validate(l) == nil {
			t.Fatal("unavailable disclosed or fabricated outcome")
		}
	}
	value.Selection = fixtureSelection(RemoteSettingsV1)
	if value.Validate(l) == nil {
		t.Fatal("inspection-v1 acquired history scope")
	}
	value.Selection = s
	value.State = "observed"
	durable := false
	result, err := ProjectRemoteResult(s, fixtureDigest, fixtureDigest, resource.UnknownOutcome(), &durable, l)
	if err != nil {
		t.Fatal(err)
	}
	value.Result = &result
	if value.Validate(l) != nil {
		t.Fatal("observed unknown evidence rejected")
	}
	value.Result.Selection.Epoch = "different-epoch"
	if value.Validate(l) == nil {
		t.Fatal("result from another selection accepted")
	}
}

func TestCatalogFailedServiceDoesNotGuessActiveState(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	for _, state := range []string{"saved", "stopped", "expired", "starting", "active", "reconnecting", "failed"} {
		row := fixtureRow(t, s, "saved-id")
		row.LocalService.State = state
		b := fixtureBuilder(t, l, s)
		if err := b.AddPage(fixturePage(s, row)); err != nil {
			t.Fatal(err)
		}
		refs := Workflows(finish(t, b).Sources[0], row, fixtureTime, l)
		if len(refs) == 0 || refs[0].Kind != "review_saved_service" {
			t.Fatal("generic review missing")
		}
		if state == "failed" {
			if len(refs) != 1 {
				t.Fatal("failed state guessed active/inactive")
			}
		} else {
			kind := "review_service_stop"
			if oneOf(state, "saved", "stopped", "expired") {
				kind = "review_service_start"
			}
			if len(refs) != 2 || refs[1].Kind != kind {
				t.Fatalf("wrong workflow for %s", state)
			}
		}
	}
}

func TestCatalogLocalNonExpiringLifetimeDirection(t *testing.T) {
	l, s := fixtureLimits(), fixtureSelection(LocalService)
	for _, tc := range []struct {
		direction, lifetime string
		valid               bool
	}{
		{"forward", "until-stopped", true}, {"share", "until-revoked", true},
		{"share", "until-stopped", false}, {"forward", "until-revoked", false},
	} {
		_, err := ProjectSavedService(s, "synthetic-service", SavedService{Name: "Synthetic service", Direction: tc.direction, Network: "tcp", Ports: "8080", Lifetime: tc.lifetime, State: "saved", Application: "unverified"}, l)
		if (err == nil) != tc.valid {
			t.Fatal("incorrect non-expiring local lifetime", tc)
		}
	}
	remote := fixtureSelection(RemoteService)
	if _, err := ProjectSharedService(remote, "synthetic-activation", SharedService{Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "until-stopped", Application: "unverified", ReviewRevision: "synthetic-review"}, l); err == nil {
		t.Fatal("local lifetime widened remote schema")
	}
}
