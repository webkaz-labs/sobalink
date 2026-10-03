package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func rustDeskFixture() RustDeskSetup {
	return RustDeskSetup{Name: "desk", Backend: "tailnet", IDPeerID: "id-peer", RelayPeerID: "relay-peer", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), IDPort: 21126, RelayPort: 21137, LocalIDPort: 32216, LocalRelayPort: 32227, LoopbackHost: "127.0.0.1", Lifetime: "until-stopped"}
}

func rustDeskPreview(t *testing.T, c *Core, in RustDeskSetup) RustDeskSetupReview {
	t.Helper()
	return mustCommand(t, c, "rustdesk.preview", RustDeskSetupRequest{Configuration: in}).(RustDeskSetupReview)
}
func saveRustDesk(t *testing.T, c *Core, in RustDeskSetup) RustDeskSetupReview {
	t.Helper()
	review := rustDeskPreview(t, c, in)
	return mustCommand(t, c, "rustdesk.save", RustDeskSetupRequest{Configuration: in, ExpectedRevision: review.Revision}).(RustDeskSetupReview)
}

func TestClientHelperImportReportsRemovedPublicMetadata(t *testing.T) {
	c := offlineDefinitionCore(t)
	saved := saveRustDesk(t, c, rustDeskFixture())
	p := c.profileCopy()
	bundle := DefinitionBundle{Version: 1, Services: p.Services, Groups: cloneGroups(p.Groups)}
	unchanged := mustCommand(t, c, "profile.import.preview", map[string]any{"profile": bundle}).(map[string]any)
	if len(unchanged["removesRustDeskMetadata"].([]string)) != 0 {
		t.Fatal("unchanged metadata reported lost")
	}
	bundle.Groups[0].RustDesk = nil
	review := mustCommand(t, c, "profile.import.preview", map[string]any{"profile": bundle}).(map[string]any)
	removed := review["removesRustDeskMetadata"].([]string)
	if len(removed) != 1 || removed[0] != saved.Group.Name {
		t.Fatal("removed public key/role metadata not identified", removed)
	}
	if c.profileCopy().Groups[0].RustDesk == nil {
		t.Fatal("preview removed metadata")
	}
}

func TestClientHelperRustDeskPreviewSaveReopenRetainsFourRolesWithoutStart(t *testing.T) {
	c := offlineDefinitionCore(t)
	input := rustDeskFixture()
	before := c.profileCopy()
	preview := rustDeskPreview(t, c, input)
	if preview.Saved || preview.Applied || !reflect.DeepEqual(before, c.profileCopy()) || len(preview.Services) != 4 {
		t.Fatal("preview changed saved state or omitted roles")
	}
	for _, role := range preview.ClientSettings.Roles {
		if role.Status != "planned" || role.ListenerReady {
			t.Fatal("unsaved preview claimed a durable or ready role")
		}
	}
	want := []struct {
		network, ports, peer string
		local                int
	}{{"tcp", "21125", "id-peer", 32215}, {"tcp", "21126", "id-peer", 32216}, {"udp", "21126", "id-peer", 32216}, {"tcp", "21137", "relay-peer", 32227}}
	for i, expected := range want {
		got := preview.Services[i]
		if got.Network != expected.network || got.Ports != expected.ports || got.PeerID != expected.peer || got.LocalPort != expected.local || got.Direction != "forward" || got.Discoverable || len(got.PeerIDs) != 0 {
			t.Fatalf("incorrect role %d: %#v", i, got)
		}
	}
	if preview.ClientSettings.IDServer != "127.0.0.1:32216" || preview.ClientSettings.RelayServer != "127.0.0.1:32227" || preview.ClientSettings.PublicKey != input.PublicKey || preview.ClientSettings.Proxy != "" || !preview.ClientSettings.UDPEnabled || preview.ClientSettings.RemoteIDSuffix != "/r" || preview.ClientSettings.Application != "unverified" {
		t.Fatal("client settings changed exact endpoints or claim verification")
	}
	saved := mustCommand(t, c, "rustdesk.save", RustDeskSetupRequest{Configuration: input, ExpectedRevision: preview.Revision}).(RustDeskSetupReview)
	if !saved.Saved || !saved.Applied || len(c.profileCopy().Services) != 4 || len(c.profileCopy().Groups) != 1 || len(c.active) != 0 || c.nodeCopy() != nil {
		t.Fatal("atomic save incomplete or started transport")
	}
	for _, role := range saved.ClientSettings.Roles {
		if role.Status != "saved" || role.ListenerReady {
			t.Fatal("saved helper role state is incorrect")
		}
	}
	duplicate := saveRustDesk(t, c, input)
	if !duplicate.Saved || duplicate.Applied || len(c.profileCopy().Services) != 4 {
		t.Fatal("repeat save recreated state")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Options{Directory: c.dir, SkipNetworkStart: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result := mustCommand(t, reopened, "rustdesk.settings", map[string]string{"group": "desk"}).(RustDeskClientSettings)
	if result.PublicKey != input.PublicKey || result.IDServer != saved.ClientSettings.IDServer || len(reopened.active) != 0 {
		t.Fatal("reopen lost helper metadata or activated transport")
	}
	for _, role := range result.Roles {
		if role.ListenerReady || role.Status != "saved" {
			t.Fatal("offline helper claimed readiness")
		}
	}
}

func TestClientHelperRustDeskValidationAndSaveFailureAreAtomic(t *testing.T) {
	tests := map[string]func(*RustDeskSetup){
		"key":            func(s *RustDeskSetup) { s.PublicKey = "invalid" },
		"short-key":      func(s *RustDeskSetup) { s.PublicKey = base64.StdEncoding.EncodeToString(make([]byte, 31)) },
		"peer":           func(s *RustDeskSetup) { s.IDPeerID = "host.example.test" },
		"backend":        func(s *RustDeskSetup) { s.Backend = "" },
		"nat-port":       func(s *RustDeskSetup) { s.LocalIDPort = 1024 },
		"remote-nat":     func(s *RustDeskSetup) { s.IDPort = 1024 },
		"local-overlap":  func(s *RustDeskSetup) { s.LocalRelayPort = s.LocalIDPort - 1 },
		"remote-overlap": func(s *RustDeskSetup) { s.RelayPeerID = s.IDPeerID; s.RelayPort = s.IDPort },
		"nonloopback":    func(s *RustDeskSetup) { s.LoopbackHost = "0.0.0.0" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			c := offlineDefinitionCore(t)
			before := c.profileCopy()
			in := rustDeskFixture()
			change(&in)
			if _, err := command(c, randomID(), "rustdesk.preview", RustDeskSetupRequest{Configuration: in}); err == nil {
				t.Fatal("invalid setup accepted")
			}
			if !reflect.DeepEqual(before, c.profileCopy()) {
				t.Fatal("validation partially saved metadata")
			}
		})
	}
	t.Run("stale-review", func(t *testing.T) {
		c := offlineDefinitionCore(t)
		in := rustDeskFixture()
		review := rustDeskPreview(t, c, in)
		in.RelayPort++
		if _, err := command(c, randomID(), "rustdesk.save", RustDeskSetupRequest{Configuration: in, ExpectedRevision: review.Revision}); err == nil {
			t.Fatal("changed setup accepted old review")
		}
		in = rustDeskFixture()
		saveDefinitionFixture(t, c, definitionFixture("newer", "tcp", "9000"))
		if _, err := command(c, randomID(), "rustdesk.save", RustDeskSetupRequest{Configuration: in, ExpectedRevision: review.Revision}); err == nil {
			t.Fatal("changed profile accepted old review")
		}
		if len(c.profileCopy().Services) != 1 || len(c.profileCopy().Groups) != 0 {
			t.Fatal("stale save partially applied")
		}
	})
	t.Run("capacity", func(t *testing.T) {
		c := offlineDefinitionCore(t)
		c.capacity.Logical["savedServices"] = capacity.Limited(3)
		before := c.profileCopy()
		if _, err := command(c, randomID(), "rustdesk.preview", RustDeskSetupRequest{Configuration: rustDeskFixture()}); err == nil {
			t.Fatal("four rules bypassed capacity")
		}
		if !reflect.DeepEqual(before, c.profileCopy()) {
			t.Fatal("capacity rejection changed state")
		}
	})
	t.Run("write-error", func(t *testing.T) {
		c := offlineDefinitionCore(t)
		in := rustDeskFixture()
		review := rustDeskPreview(t, c, in)
		before := c.profileCopy()
		path := filepath.Join(c.dir, "sobalink.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := command(c, randomID(), "rustdesk.save", RustDeskSetupRequest{Configuration: in, ExpectedRevision: review.Revision}); err == nil {
			t.Fatal("write failure was hidden")
		}
		if !reflect.DeepEqual(before, c.profileCopy()) || len(c.active) != 0 {
			t.Fatal("failed write retained partial settings")
		}
	})
}

func TestClientHelperRustDeskMetadataCloneEditsAndImport(t *testing.T) {
	c := offlineDefinitionCore(t)
	saved := saveRustDesk(t, c, rustDeskFixture())
	cloned := c.profileCopy()
	cloned.Groups[0].RustDesk.PublicKey = "changed"
	if c.profileCopy().Groups[0].RustDesk.PublicKey == "changed" {
		t.Fatal("profile clone aliases public metadata")
	}
	altered := saved.Services[0]
	altered.Ports = "21124"
	if _, err := command(c, randomID(), "service.save", map[string]any{"configuration": altered, "expectedRevision": serviceRevision(saved.Services[0])}); err == nil {
		t.Fatal("individual edit left inconsistent NAT role")
	}
	group := saved.Group
	group.RustDesk = nil
	in := map[string]any{"group": group, "expectedRevision": definitionsRevision(c.profileCopy())}
	if _, err := command(c, randomID(), "group.save", in); err == nil {
		t.Fatal("omitted metadata silently discarded public key")
	}
	exported := mustCommand(t, c, "profile.export", map[string]any{}).(map[string]any)["profile"].(DefinitionBundle)
	other := offlineDefinitionCore(t)
	preview := mustCommand(t, other, "profile.import.preview", map[string]any{"profile": exported}).(map[string]any)
	mustCommand(t, other, "profile.import", map[string]any{"profile": exported, "expectedRevision": preview["revision"]})
	if got := other.profileCopy().Groups[0].RustDesk; got == nil || got.PublicKey != saved.Group.RustDesk.PublicKey || len(other.active) != 0 {
		t.Fatal("import lost public metadata or activated state")
	}
	broken := exported
	broken.Groups = cloneGroups(exported.Groups)
	broken.Groups[0].RustDesk.IDServiceID = broken.Groups[0].RustDesk.NATServiceID
	if _, err := command(other, randomID(), "profile.import.preview", map[string]any{"profile": broken}); err == nil {
		t.Fatal("import accepted duplicate roles")
	}
	in["removeRustDesk"] = true
	mustCommand(t, c, "group.save", in)
	if c.profileCopy().Groups[0].RustDesk != nil || len(c.profileCopy().Services) != 4 {
		t.Fatal("explicit detach removed underlying forwards")
	}
}

func TestClientHelperGeneralExactMappingsIdentityAndWarnings(t *testing.T) {
	c := offlineDefinitionCore(t)
	state := identity.State{IPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}, Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: "peer-example", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}}}}}
	spec := ServiceSpec{ID: "example", Name: "ssh", Backend: "tailnet", Direction: "forward", Network: "tcp", Ports: "22", LocalPort: 22022, LoopbackHost: "::1", Lifetime: "until-stopped", PeerID: "peer-example", Purpose: "ssh"}
	got, err := c.clientServiceSettings("tailnet", spec, state)
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalEndpoint != "[::1]:22022" || !reflect.DeepEqual(got.RemoteEndpoints, []string{"100.64.0.2:22"}) || got.SSH == nil || got.SSH.HostKeyAlias != "sobalink-tailnet-peer-example-22" || got.ListenerReady || got.Application != "unverified" {
		t.Fatalf("wrong SSH hints: %#v", got)
	}
	spec.Purpose = "web"
	spec.Ports = "443"
	got, err = c.clientServiceSettings("tailnet", spec, state)
	if err != nil || got.HTTPCandidate != "http://[::1]:22022/" || got.SSH != nil {
		t.Fatal("wrong HTTP candidate", err)
	}
	notices, _ := json.Marshal(got.Notices)
	if !strings.Contains(string(notices), "http_candidate_tls") || !strings.Contains(string(notices), "証明書") {
		t.Fatal("TLS caveat missing a language")
	}
	spec.Ports = "8000-8003,9000-9002"
	spec.ExcludePorts = "8001,9001"
	spec.LocalPort = 30000
	got, err = c.clientServiceSettings("tailnet", spec, state)
	want := []ClientPortMapping{{30000, 30000, 8000, 8000}, {30001, 30002, 8002, 8003}, {30003, 30003, 9000, 9000}, {30004, 30004, 9002, 9002}}
	if err != nil || !reflect.DeepEqual(got.Mappings, want) || got.LocalEndpoint != "" || got.HTTPCandidate != "" || len(got.RemoteEndpoints) != 0 || !reflect.DeepEqual(got.RemoteHosts, []string{"100.64.0.2"}) {
		t.Fatalf("range mapping fabricated a scalar endpoint: %#v (%v)", got, err)
	}
	got, err = c.clientServiceSettings("lan", spec, state)
	if err != nil || len(got.RemoteEndpoints) != 0 {
		t.Fatal("another backend supplied endpoint identity")
	}
}

func TestClientHelperMetadataOnlyOfflineCommandAndSelection(t *testing.T) {
	dir := t.TempDir()
	call := func(name string, payload any) any {
		t.Helper()
		raw, _ := json.Marshal(payload)
		result, err := OfflineDefinitionCommand(context.Background(), Options{Directory: dir, SkipNetworkStart: true}, webui.Command{RequestID: randomID(), Name: name, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	in := rustDeskFixture()
	in.LoopbackHost = "::1"
	preview := call("rustdesk.preview", RustDeskSetupRequest{Configuration: in}).(RustDeskSetupReview)
	saved := call("rustdesk.save", RustDeskSetupRequest{Configuration: in, ExpectedRevision: preview.Revision}).(RustDeskSetupReview)
	view := call("client.settings", ClientSettingsRequest{Group: saved.Group.Name}).(ClientSettingsView)
	if len(view.Services) != 4 || len(view.RustDesk) != 1 || view.RustDesk[0].IDServer != "[::1]:32216" {
		t.Fatal("offline group hints incomplete")
	}
	for _, forbidden := range []string{"identity", "lan.json", "messages.json"} {
		if _, err := os.Stat(filepath.Join(dir, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("helper touched runtime identity/state: %s (%v)", forbidden, err)
		}
	}
}

func TestClientHelperReportsActualRuntimeLifetimeWithoutRewritingSavedRules(t *testing.T) {
	c := offlineDefinitionCore(t)
	saved := saveRustDesk(t, c, rustDeskFixture())
	spec := saved.Services[1]
	runtime := spec
	runtime.Lifetime = "finite"
	runtime.TTLSeconds = 7200
	effective, err := ranges.Parse(spec.Ports)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.active[spec.ID] = &activeService{spec: runtime, effective: effective, ctx: context.Background()}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.active, spec.ID); c.mu.Unlock() }()
	hint, err := c.clientServiceSettings("tailnet", spec, identity.State{})
	if err != nil || hint.Lifetime != "finite" || hint.TTLSeconds != 7200 {
		t.Fatalf("general helper ignored runtime lifetime: %#v %v", hint, err)
	}
	settings, err := c.rustDeskSettings(c.profileCopy(), saved.Group)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Roles[1].Lifetime != "finite" || settings.Roles[1].TTLSeconds != 7200 {
		t.Fatal("RustDesk helper ignored runtime lifetime")
	}
	if current := c.profileCopy().Services[1]; current.Lifetime != "until-stopped" || current.TTLSeconds != 0 {
		t.Fatal("reporting runtime lifetime rewrote saved rules")
	}
}
