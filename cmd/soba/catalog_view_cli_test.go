package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestCatalogViewCLILocalValidationNeverCallsForDryRunOrInvalidSelection(t *testing.T) {
	id, peer := strings.Repeat("a", 32), strings.Repeat("b", 64)
	noCall := func(context.Context, string, string, any) error { t.Fatal("unexpected agent call"); return nil }
	for _, ja := range []bool{false, true} {
		for _, args := range [][]string{
			{"preview", "--id", id, "--peer", peer, "--service-ids", strings.Repeat("c", 32), "--expires-at", "2030-01-01T00:00:00Z"},
			{"list", "--id", id, "--peer", peer, "--grant-id", strings.Repeat("d", 32), "--grant-revision", "1"},
			{"revoke", "--id", id, "--grant-id", strings.Repeat("d", 32), "--grant-revision", "1", "--confirm"},
		} {
			var out bytes.Buffer
			if err := resourceCatalogCLI(t.Context(), append([]string{"view"}, args...), "synthetic-profile", ja, true, &out, noCall); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"validation": "local-input-only"`) {
				t.Fatal(out.String())
			}
		}
		for _, args := range [][]string{
			{"preview", "--id", id, "--peer", peer, "--service-ids", strings.Repeat("c", 32) + "," + strings.Repeat("c", 32), "--expires-at", "2030-01-01T00:00:00Z"},
			{"list", "--id", id, "--peer", peer, "--grant-id", strings.Repeat("d", 32), "--grant-revision", "01"},
			{"revoke", "--id", id, "--grant-id", strings.Repeat("d", 32), "--grant-revision", "1"},
			{"confirm", "--confirm"},
		} {
			var out bytes.Buffer
			if err := resourceCatalogViewCLI(t.Context(), args, "synthetic-profile", ja, false, &out, noCall); err == nil {
				t.Fatal("accepted incomplete scope", args)
			}
		}
	}
}

func TestCatalogViewCLIExplicitListKeepsPermissionAndMachineLanguageIndependent(t *testing.T) {
	id, peer, grant, review := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 32), strings.Repeat("d", 32)
	selector := resourcegrant.CatalogViewSelector{ProtocolVersion: 1, Target: resource.Target{SchemaVersion: 1, ResourceID: id}, GrantID: grant, GrantRevision: 1}
	observation := catalogViewCLIObservation{Selector: selector, PeerKey: peer, ReviewRevision: review, CheckedAt: time.Unix(1735689600, 0).UTC()}
	row := struct {
		Service        core.RemoteService `json:"service"`
		ConnectAllowed bool               `json:"connectAllowed"`
	}{Service: core.RemoteService{ID: strings.Repeat("e", 32), Purpose: "web", Network: "tcp", Ports: "8080", Lifetime: "until-revoked", Application: "unverified"}}
	observation.Services = append(observation.Services, row)
	args := []string{"list", "--id", id, "--peer", peer, "--grant-id", grant, "--grant-revision", "1"}
	var machine []string
	for _, ja := range []bool{false, true} {
		calls := 0
		client := func(_ context.Context, dir, raw string, target any) error {
			calls++
			var command webui.Command
			if dir != "synthetic-profile" || json.Unmarshal([]byte(raw), &command) != nil || command.Name != "resource.catalog.view.list" {
				t.Fatal(raw)
			}
			var input resourcegrant.RemoteCatalogViewInput
			if json.Unmarshal(command.Payload, &input) != nil || input.PeerKey != peer || input.Request != selector {
				t.Fatal(string(command.Payload))
			}
			data, _ := json.Marshal(observation)
			return json.Unmarshal(data, target)
		}
		var out bytes.Buffer
		if err := resourceCatalogViewCLI(t.Context(), args, "synthetic-profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(out.String(), text(ja, "Connection allowed: false", "接続許可: false")) {
			t.Fatal(out.String())
		}
		out.Reset()
		if err := resourceCatalogViewCLI(t.Context(), append(args, "--json"), "synthetic-profile", ja, false, &out, client); err != nil {
			t.Fatal(err)
		}
		machine = append(machine, out.String())
	}
	if machine[0] != machine[1] {
		t.Fatal("machine format depends on language")
	}
	observation.PeerKey = strings.Repeat("f", 64)
	var out bytes.Buffer
	client := func(_ context.Context, _ string, _ string, target any) error {
		data, _ := json.Marshal(observation)
		return json.Unmarshal(data, target)
	}
	if err := resourceCatalogViewCLI(t.Context(), append(args, "--json"), "synthetic-profile", false, false, &out, client); err == nil || out.Len() != 0 {
		t.Fatal("foreign response was emitted")
	}
}

func TestCatalogViewCLIConnectionUsesExistingPayloadAndRejectsMixedReviews(t *testing.T) {
	review, id, peer := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	args := []string{"--peer", peer, "--catalog-review", review, "--catalog-service", id, "--name", "synthetic-connect", "--network", "tcp", "--ports", "8080", "--purpose", "web", "--local-port", "18080", "--lifetime", "until-stopped"}
	for _, ja := range []bool{false, true} {
		payload, err := servicePayload("connect", args, ja, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		ref, ok := payload["catalogView"].(map[string]string)
		if !ok || ref["reviewRevision"] != review || ref["serviceId"] != id || payload["serviceRevision"] != nil || payload["peerId"] != peer {
			t.Fatal(payload)
		}
		if _, err := servicePayload("connect", append(args, "--service-id", id, "--service-revision", "legacy-review"), ja, &bytes.Buffer{}); err == nil {
			t.Fatal("mixed reviews accepted")
		}
	}
}

func TestCatalogViewCLIErrorsRemainLocalizedAndPrivate(t *testing.T) {
	for _, code := range []string{"catalog_view_invalid", "catalog_view_stale", "catalog_view_revoked", "catalog_view_expired", "catalog_view_unavailable", "catalog_view_limited", "catalog_view_conflict"} {
		original := &resourceCLIError{code: code, message: "synthetic private path", cause: errors.New("private")}
		for _, ja := range []bool{false, true} {
			err := localizeResourceError(ja, resourceControlError(original))
			if strings.Contains(err.Error(), "private") || err.Error() == "" {
				t.Fatal(code, err)
			}
		}
	}
}
