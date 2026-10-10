package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/resourcecatalog"
)

func TestResourceCatalogSelectorArmEncodingRoundTrip(t *testing.T) {
	target := resource.Target{SchemaVersion: 1, ResourceID: strings.Repeat("a", 32)}
	for _, s := range []resourceCatalogSource{
		{Kind: resourcecatalog.LocalSettings, Target: target},
		{Kind: resourcecatalog.LocalService},
		{Kind: resourcecatalog.TransferActivity},
		{Kind: resourcecatalog.RemoteService, PeerID: "synthetic-peer"},
		{Kind: resourcecatalog.RemoteSettingsV1, PeerKey: strings.Repeat("b", 64), Target: target, GrantID: strings.Repeat("c", 32), GrantRevision: 1},
		{Kind: resourcecatalog.RemoteSettingsV2, PeerKey: strings.Repeat("b", 64), Target: target, GrantID: strings.Repeat("c", 32), GrantRevision: 1},
	} {
		raw, err := json.Marshal(resourceCatalogRequest{SchemaVersion: 1, Sources: []resourceCatalogSource{s}})
		if err != nil {
			t.Fatal("exact arm could not encode", s.Kind, err)
		}
		decoded, err := decodeResourceCatalogRequest(raw)
		if err != nil || len(decoded.Sources) != 1 || decoded.Sources[0] != s {
			t.Fatal("encode/decode changed selector arm", s.Kind, err)
		}
		if s.Target == (resource.Target{}) && strings.Contains(string(raw), `"target"`) {
			t.Fatal("value-struct omitempty leaked another arm")
		}
	}
	if _, err := json.Marshal(resourceCatalogSource{Kind: resourcecatalog.LocalService, Target: target}); err == nil {
		t.Fatal("nonzero foreign-arm field silently dropped")
	}
}

func TestResourceCatalogClosedRequest(t *testing.T) {
	good := `{"schemaVersion":1,"sources":[{"kind":"local_service"},{"kind":"transfer_activity"}]}`
	if ValidateResourceCatalogRequest([]byte(good)) != nil {
		t.Fatal("valid selection rejected")
	}
	for _, raw := range []string{
		`null`, `{"schemaVersion":1,"sources":[]}`, `{"schemaVersion":2,"sources":[{"kind":"local_service"}]}`,
		`{"schemaVersion":1,"schemaVersion":1,"sources":[{"kind":"local_service"}]}`,
		`{"SchemaVersion":1,"sources":[{"kind":"local_service"}]}`,
		`{"schemaVersion":1,"sources":[{"kind":"local_service","peerId":""}]}`,
		`{"schemaVersion":1,"sources":[{"kind":"local_service","target":null}]}`,
		`{"schemaVersion":1,"sources":[{"kind":"local_service"},{"kind":"local_service"}]}`,
		`{"schemaVersion":1,"sources":[{"kind":"unknown"}]}`, good + ` {}`,
		`{"schemaVersion":1,"sources":[{"kind":"remote_service","peerId":""}]}`,
	} {
		if ValidateResourceCatalogRequest([]byte(raw)) == nil {
			t.Fatal("invalid selector accepted", raw)
		}
	}
	if ValidateResourceCatalogRequest([]byte(good+strings.Repeat(" ", resourceCatalogRequestBytes))) == nil {
		t.Fatal("oversize accepted")
	}
}

func TestResourceCatalogRemoteSelectorUnion(t *testing.T) {
	peer := strings.Repeat("a", 64)
	remote := `{"kind":"remote_settings_v2","peerKey":"` + peer + `","target":{"schemaVersion":1,"resourceId":"` + strings.Repeat("b", 32) + `"},"grantId":"` + strings.Repeat("c", 32) + `","grantRevision":1}`
	wrap := func(sources string) []byte { return []byte(`{"schemaVersion":1,"sources":[` + sources + `]}`) }
	if ValidateResourceCatalogRequest(wrap(remote+`,{"kind":"remote_service","peerId":"`+peer+`"}`)) != nil {
		t.Fatal("same exact peer rejected")
	}
	for _, sources := range []string{remote + `,` + strings.Replace(remote, "remote_settings_v2", "remote_settings_v1", 1), remote + `,{"kind":"remote_service","peerId":"synthetic-other"}`, strings.Replace(remote, `"grantRevision":1`, `"grantRevision":9007199254740992`, 1), strings.Replace(remote, `"grantRevision":1`, `"grantRevision":0`, 1)} {
		if ValidateResourceCatalogRequest(wrap(sources)) == nil {
			t.Fatal("mixed or invalid remote accepted")
		}
	}
}

func TestResourceCatalogEnvelopeClosedRoundTrip(t *testing.T) {
	l, err := resourceCatalogPinnedLimits(capacity.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	s := resourcecatalog.Selection{SourceID: resourcecatalog.LocalService, Kind: resourcecatalog.LocalService, Epoch: "synthetic-epoch"}
	b, err := resourcecatalog.NewBuilder("synthetic-snapshot", "synthetic-scope", []resourcecatalog.Selection{s}, l)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.AddPage(resourcecatalog.Page{Selection: s, State: "current", CheckedAt: 1700000000000, Revision: "synthetic-revision", Rows: []resourcecatalog.Row{}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resourceCatalogResponse{1, resourceCatalogViewOf(l), snapshot})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResourceCatalogResponse(raw)
	if err != nil || !decoded.Snapshot.Complete || decoded.Limits.CatalogLimits() != l {
		t.Fatal("round trip failed", err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"schemaVersion":1`, `"schemaVersion":2`, 1), strings.Replace(string(raw), `"maxRows":`, `"MaxRows":`, 1), strings.Replace(string(raw), `"snapshot":`, `"extra":0,"snapshot":`, 1), strings.Replace(string(raw), snapshot.Revision, strings.Repeat("0", 64), 1)} {
		if _, err := DecodeResourceCatalogResponse([]byte(bad)); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}
