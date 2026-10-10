package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func resourceCatalogCLITestDescriptor() resource.Descriptor {
	return resource.Descriptor{Target: resource.Target{SchemaVersion: 1, ResourceID: strings.Repeat("a", 32)}, Type: resource.Type, Authority: "local", Provider: "local", Operations: []string{"list", "inspect", "preview", "apply", "operation.status"}, Revision: strings.Repeat("b", 64), Requested: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(2)}, Effective: resource.Effective{TransferConcurrentFiles: 8, TransferConcurrentPerPeer: 2}}
}
func resourceCatalogCLITestResponse(t *testing.T, request resourceCatalogCLIRequest, failKind string) core.ResourceCatalogResponse {
	t.Helper()
	limits := resourcecatalog.Limits{MaxSources: 5, MaxRemoteTargets: 1, MaxRows: 16, MaxPageRows: 16, MaxPages: 1, MaxBytes: 65536, MaxPageBytes: 65536, MaxStringBytes: 4096}
	selections := []resourcecatalog.Selection{}
	for _, source := range request.Sources {
		s := resourcecatalog.Selection{SourceID: source.Kind, Kind: source.Kind, Epoch: "synthetic-epoch", PeerKey: source.PeerKey, GrantID: source.GrantID, GrantRevision: source.GrantRevision}
		if source.Target != nil {
			s.Target = *source.Target
		}
		if source.Kind == resourcecatalog.RemoteService {
			s.PeerKey = source.PeerID
		}
		if source.Kind == resourcecatalog.TransferActivity {
			s.ProcessID = strings.Repeat("c", 64)
		}
		selections = append(selections, s)
	}
	builder, err := resourcecatalog.NewBuilder("synthetic-observation", "synthetic-scope", selections, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range selections {
		if s.Kind == failKind {
			if err := builder.Fail(s, "unavailable", 1735689600000); err != nil {
				t.Fatal(err)
			}
			continue
		}
		rows := []resourcecatalog.Row{}
		switch s.Kind {
		case resourcecatalog.LocalSettings:
			descriptor := resourceCatalogCLITestDescriptor()
			descriptor.Target = s.Target
			row, err := resourcecatalog.ProjectLocalSettings(s, descriptor, limits)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		case resourcecatalog.RemoteSettingsV1, resourcecatalog.RemoteSettingsV2:
			values := resourcecatalog.SettingsValues{Requested: resourceCatalogCLITestDescriptor().Requested, Effective: resourceCatalogCLITestDescriptor().Effective}
			row := resourcecatalog.Row{Identity: resourcecatalog.Identity{ID: s.Target.ResourceID, Lifetime: resourcecatalog.Persistent}}
			if s.Kind == resourcecatalog.RemoteSettingsV1 {
				row.RemoteSettingsV1 = &values
			} else {
				row.RemoteSettingsV2 = &values
			}
			rows = append(rows, row)
		}
		total := int64(len(rows))
		if err := builder.AddPage(resourcecatalog.Page{Selection: s, State: "current", CheckedAt: 1735689600000, Revision: "synthetic-source-revision", Total: &total, Rows: rows}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return core.ResourceCatalogResponse{SchemaVersion: 1, Limits: core.ResourceCatalogViewLimits{MaxSources: limits.MaxSources, MaxRemoteTargets: limits.MaxRemoteTargets, MaxRows: limits.MaxRows, MaxPageRows: limits.MaxPageRows, MaxPages: limits.MaxPages, MaxBytes: limits.MaxBytes, MaxPageBytes: limits.MaxPageBytes, MaxStringBytes: limits.MaxStringBytes}, Snapshot: snapshot}
}
func TestResourceCatalogCLIDefaultResolvesThenRequestsExactLocalSources(t *testing.T) {
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		calls := []string{}
		client := func(_ context.Context, dir, raw string, target any) error {
			if dir != "synthetic-profile" {
				t.Fatal("profile changed")
			}
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil {
				t.Fatal("invalid command")
			}
			calls = append(calls, cmd.Name)
			if cmd.Name == "resource.list" {
				return json.Unmarshal(resourceCollectionTestJSON(t, resource.Catalog{SchemaVersion: 1, Resources: []resource.Descriptor{resourceCatalogCLITestDescriptor()}}), target)
			}
			if cmd.Name != "resource.catalog.snapshot" {
				t.Fatal("unexpected command", cmd.Name)
			}
			var request resourceCatalogCLIRequest
			if json.Unmarshal(cmd.Payload, &request) != nil || core.ValidateResourceCatalogRequest(cmd.Payload) != nil {
				t.Fatal("invalid selectors")
			}
			if len(request.Sources) != 3 || request.Sources[0].Kind != resourcecatalog.LocalService || request.Sources[1].Kind != resourcecatalog.LocalSettings || request.Sources[2].Kind != resourcecatalog.TransferActivity || request.Sources[1].Target.ResourceID != resourceCatalogCLITestDescriptor().ResourceID {
				t.Fatal(request)
			}
			return json.Unmarshal(resourceCollectionTestJSON(t, resourceCatalogCLITestResponse(t, request, "")), target)
		}
		if err := resourceCatalogCLI(t.Context(), []string{"--json"}, "synthetic-profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"resource.list", "resource.catalog.snapshot"}) {
			t.Fatal(calls)
		}
		if response, err := core.DecodeResourceCatalogResponse(out.Bytes()); err != nil || !response.Snapshot.Complete {
			t.Fatal(err, out.String())
		}
	}
}
func TestResourceCatalogCLIDryRunLeavesSettingsUnresolvedWithoutCalls(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("dry run contacted agent"); return nil }
	for _, args := range [][]string{nil, {"--settings"}, {"--settings", "--services"}} {
		var out bytes.Buffer
		if err := resourceCatalogCLI(t.Context(), args, "unused", false, true, &out, noCall); err != nil {
			t.Fatal(err)
		}
		var plan map[string]json.RawMessage
		if json.Unmarshal(out.Bytes(), &plan) != nil || string(plan["requiresResourceList"]) != "true" || plan["payload"] != nil || strings.Contains(out.String(), "resourceId") {
			t.Fatal(out.String())
		}
		var unresolved []string
		if json.Unmarshal(plan["unresolvedSources"], &unresolved) != nil || len(unresolved) != 1 || unresolved[0] != resourcecatalog.LocalSettings {
			t.Fatal(out.String())
		}
	}
	var out bytes.Buffer
	if err := resourceCatalogCLI(t.Context(), []string{"--settings-id", resourceCatalogCLITestDescriptor().ResourceID}, "unused", false, true, &out, noCall); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"payload"`) || strings.Contains(out.String(), "unresolvedSources") {
		t.Fatal(out.String())
	}
}
func TestResourceCatalogCLIResolutionFailureNeverSilentlyNarrows(t *testing.T) {
	for _, response := range [][]byte{[]byte(`{}`), resourceCollectionTestJSON(t, resource.Catalog{SchemaVersion: 1, Resources: []resource.Descriptor{}}), resourceCollectionTestJSON(t, resource.Catalog{SchemaVersion: 1, Resources: []resource.Descriptor{resourceCatalogCLITestDescriptor(), resourceCatalogCLITestDescriptor()}})} {
		calls := 0
		var out bytes.Buffer
		client := func(_ context.Context, _ string, raw string, target any) error {
			calls++
			var cmd webui.Command
			_ = json.Unmarshal([]byte(raw), &cmd)
			if cmd.Name != "resource.list" {
				t.Fatal("silently narrowed")
			}
			return json.Unmarshal(response, target)
		}
		err := resourceCatalogCLI(t.Context(), nil, "unused", false, false, &out, client)
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "resource_catalog_resolution_unavailable" || calls != 1 || out.Len() != 0 || !strings.Contains(err.Error(), "--services --transfers") {
			t.Fatal(err, calls, out.String())
		}
	}
	calls := 0
	client := func(_ context.Context, _ string, raw string, target any) error {
		calls++
		var cmd webui.Command
		_ = json.Unmarshal([]byte(raw), &cmd)
		if cmd.Name != "resource.catalog.snapshot" {
			t.Fatal("narrow selection resolved settings")
		}
		var request resourceCatalogCLIRequest
		_ = json.Unmarshal(cmd.Payload, &request)
		return json.Unmarshal(resourceCollectionTestJSON(t, resourceCatalogCLITestResponse(t, request, "")), target)
	}
	if err := resourceCatalogCLI(t.Context(), []string{"--services", "--transfers"}, "unused", false, false, io.Discard, client); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
func TestResourceCatalogCLIRemoteSelectionHasOneInspectionNoFallback(t *testing.T) {
	peer := strings.Repeat("a", 64)
	for _, version := range []string{"1", "2"} {
		args := []string{"--remote-version", version, "--peer", peer, "--id", strings.Repeat("b", 32), "--grant-id", strings.Repeat("c", 32), "--grant-revision", "3", "--discovery-peer", peer, "--json"}
		calls := 0
		client := func(_ context.Context, _ string, raw string, target any) error {
			calls++
			var cmd webui.Command
			if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Name != "resource.catalog.snapshot" {
				t.Fatal("unexpected dispatch")
			}
			var request resourceCatalogCLIRequest
			_ = json.Unmarshal(cmd.Payload, &request)
			if len(request.Sources) != 2 {
				t.Fatal("extra sources")
			}
			return json.Unmarshal(resourceCollectionTestJSON(t, resourceCatalogCLITestResponse(t, request, resourcecatalog.RemoteService)), target)
		}
		var out bytes.Buffer
		if err := resourceCatalogCLI(t.Context(), args, "unused", false, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal("fallback or repeated calls", calls)
		}
		response, err := core.DecodeResourceCatalogResponse(out.Bytes())
		if err != nil || response.Snapshot.Complete {
			t.Fatal("partial hidden")
		}
	}
}
func TestResourceCatalogCLIInvalidSelectorsMakeZeroCalls(t *testing.T) {
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("invalid selector contacted agent")
		return nil
	}
	remote := []string{"--remote-version", "1", "--peer", strings.Repeat("a", 64), "--id", strings.Repeat("b", 32), "--grant-id", strings.Repeat("c", 32), "--grant-revision", "1"}
	cases := [][]string{{"--settings-id", "bad"}, {"--settings=false"}, {"--services=false"}, {"--settings-id", strings.Repeat("a", 32), "--settings=false"}, {"--peer", strings.Repeat("a", 64)}, {"--remote-version", "3"}, {"--discovery-peer", ""}, {"--selection-file", "synthetic.json"}, {"--command", "service.start"}, {"--offline"}, append(append([]string{}, remote...), "--discovery-peer", strings.Repeat("d", 64)), append(append([]string{}, remote...), "--grant-revision", "0")}
	for _, args := range cases {
		if err := resourceCatalogCLI(t.Context(), args, "unused", false, false, io.Discard, noCall); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestResourceCatalogCLIRejectsDifferentValidSourceResponse(t *testing.T) {
	requested := resourceCatalogCLIRequest{SchemaVersion: 1, Sources: []resourceCatalogCLISource{{Kind: resourcecatalog.LocalService}}}
	other := resourceCatalogCLIRequest{SchemaVersion: 1, Sources: []resourceCatalogCLISource{{Kind: resourcecatalog.TransferActivity}}}
	valid := resourceCatalogCLITestResponse(t, requested, "")
	different := resourceCatalogCLITestResponse(t, other, "")
	malformed := strings.Replace(string(resourceCollectionTestJSON(t, valid)), `"schemaVersion":1`, `"SchemaVersion":1`, 1)
	for _, raw := range [][]byte{resourceCollectionTestJSON(t, different), []byte(malformed), []byte(`{}`)} {
		client := func(_ context.Context, _ string, _ string, target any) error {
			p := target.(*json.RawMessage)
			*p = append((*p)[:0], raw...)
			return nil
		}
		var out bytes.Buffer
		if err := resourceCatalogCLI(t.Context(), []string{"--services", "--json"}, "unused", false, false, &out, client); err == nil || out.Len() != 0 {
			t.Fatal("mismatched response rendered")
		}
	}
}
func TestResourceCatalogCLIHumanScopeLifetimePartialAndProfile(t *testing.T) {
	target := resourceCatalogCLITestDescriptor().Target
	request := resourceCatalogCLIRequest{SchemaVersion: 1, Sources: []resourceCatalogCLISource{{Kind: resourcecatalog.LocalService}, {Kind: resourcecatalog.LocalSettings, Target: &target}, {Kind: resourcecatalog.TransferActivity}}}
	response := resourceCatalogCLITestResponse(t, request, resourcecatalog.TransferActivity)
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeResourceCatalogHumanAt(&out, "/synthetic/profile path", ja, response, 1735689600001); err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{"synthetic-scope", "2025-01-01T00:00:00Z", text(ja, "Partial", "一部取得"), text(ja, "persistent", "保存中のみ有効"), text(ja, "empty source", "空の情報源"), `argv: ["soba","--state-dir","/synthetic/profile path"`, text(ja, "not continuous monitoring", "常時監視")} {
			if !strings.Contains(out.String(), part) {
				t.Fatal(part, out.String())
			}
		}
	}
}
func TestResourceCollectionHelpAutomaticLocaleAndOverride(t *testing.T) {
	noCall := func(context.Context, string, string, any) error { t.Fatal("help contacted agent"); return nil }
	for _, env := range []string{"ja_JP.UTF-8", "en_US.UTF-8", "unknown_LOCALE"} {
		t.Setenv("LC_ALL", env)
		for _, locale := range []string{"auto", "en", "ja"} {
			for _, command := range []string{"group", "catalog"} {
				var out bytes.Buffer
				if err := runWith(t.Context(), []string{"--locale", locale, "resource", command, "--help"}, &out, strings.NewReader(""), noCall); err != nil {
					t.Fatal(err)
				}
				wantJA := locale == "ja" || locale == "auto" && strings.HasPrefix(env, "ja")
				if strings.Contains(out.String(), "ソースビルド") != wantJA || !strings.Contains(out.String(), "--dry-run") {
					t.Fatal(env, locale, command, out.String())
				}
			}
		}
	}
}

func TestResourceCollectionDispatcherAndMachineErrorLanguage(t *testing.T) {
	noCall := func(context.Context, string, string, any) error {
		t.Fatal("dry-run dispatcher contacted agent")
		return nil
	}
	for _, args := range [][]string{{"catalog", "--services"}, append([]string{"group"}, resourceGroupCLITestArgs(t, 1)...)} {
		var out bytes.Buffer
		if err := resourceCLI(t.Context(), args, "synthetic-profile", false, true, &out, noCall); err != nil || !strings.Contains(out.String(), "local-input-only") {
			t.Fatal(err, out.String())
		}
	}
	for _, machine := range []bool{false, true} {
		args := []string{"--locale", "ja", "--state-dir", "synthetic-profile"}
		if machine {
			args = append(args, "--json-errors")
		}
		args = append(args, "resource", "catalog", "--services")
		err := runWith(t.Context(), args, io.Discard, strings.NewReader(""), func(context.Context, string, string, any) error {
			return resourceCollectionError("resource_catalog_unavailable", nil)
		})
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != "resource_catalog_unavailable" {
			t.Fatal(err)
		}
		if strings.Contains(err.Error(), "ローカル") == machine {
			t.Fatal("error locale changed machine contract", err)
		}
	}
}
func TestResourceCatalogCLICachedDiscoveryRemainsNonActionable(t *testing.T) {
	request := resourceCatalogCLIRequest{SchemaVersion: 1, Sources: []resourceCatalogCLISource{{Kind: resourcecatalog.RemoteService, PeerID: "synthetic-peer"}}}
	response := resourceCatalogCLITestResponse(t, request, "")
	limits := response.Limits.CatalogLimits()
	selection := response.Snapshot.Sources[0].Selection
	builder, err := resourcecatalog.NewBuilder("synthetic-observation", "synthetic-scope", []resourcecatalog.Selection{selection}, limits)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(0)
	if err := builder.AddPage(resourcecatalog.Page{Selection: selection, State: "stale", CheckedAt: 1735689600000, Revision: "synthetic-revision", Total: &total, Rows: []resourcecatalog.Row{}}); err != nil {
		t.Fatal(err)
	}
	response.Snapshot, err = builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	for _, ja := range []bool{false, true} {
		var out bytes.Buffer
		if err := writeResourceCatalogHumanAt(&out, "synthetic-profile", ja, response, 1735689600001); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), text(ja, "non-actionable", "操作できません")) || !strings.Contains(out.String(), "discover --peer synthetic-peer") || strings.Contains(out.String(), "soba --state-dir synthetic-profile connect") {
			t.Fatal(out.String())
		}
	}
}
