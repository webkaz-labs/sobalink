package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveProxyFixture(t *testing.T, c *Core, generate, launch bool) SavedProxyView {
	t.Helper()
	scope := proxyFixture()
	scope.Lifetime = "finite"
	scope.TTLSeconds = 3600
	review := previewProxy(t, c, scope)
	input := map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "expectedStoreRevision": c.savedProxyView()["revision"], "startOnLaunch": launch}
	name := "proxy.generate"
	if !generate {
		name = "proxy.save"
		input["username"] = "fixture-private-user"
		input["password"] = "fixture-private-password"
	}
	return mustCommand(t, c, name, input).(SavedProxyView)
}
func TestSavedProxyPrivateExplicitGenerationNoLeakage(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, true, true)
	if count.Load() != 0 || !saved.CredentialsSaved {
		t.Fatal("save started listener")
	}
	credentials := mustCommand(t, p.a, "proxy.reveal", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}).(SavedProxyCredentials)
	if len(credentials.Username) != 24 || len(credentials.Password) != 43 {
		t.Fatal("unexpected generated credential strength")
	}
	snapshot, err := p.a.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exported := mustCommand(t, p.a, "profile.export", map[string]any{})
	public, _ := json.Marshal([]any{snapshot, exported, saved, p.a.savedProxyView(), p.a.requests})
	for _, secret := range []string{credentials.Username, credentials.Password} {
		if strings.Contains(string(public), secret) {
			t.Fatal("credential leaked into public result or history")
		}
	}
	path := filepath.Join(p.a.dir, "saved-proxies.json")
	assertLANStatePrivate(t, path)
	if err := p.a.loadStartup(true); err != nil {
		t.Fatal(err)
	}
	p.a.runSavedProxyStartup(context.Background())
	if count.Load() != 0 {
		t.Fatal("offline launched saved proxy")
	}
	if err := p.a.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	withServiceOperation(p.a, func() { p.a.runSavedProxyStartup(context.Background()); p.a.runSavedProxyStartup(context.Background()) })
	if count.Load() != 1 {
		t.Fatal("launch count", count.Load())
	}
}
func TestSavedProxyRecoveryPreservesExpiryAndStopCancels(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, false)
	started := mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}).(map[string]any)
	expires := p.a.proxies[started["id"].(string)].expires
	withServiceOperation(p.a, func() { p.a.suspendSavedProxies(); p.a.stopAllProxies(); p.a.resumeSavedProxies(context.Background()) })
	run := p.a.savedProxyRuns[saved.Name]
	if count.Load() != 2 || !p.a.proxies[run.ID].expires.Equal(expires) {
		t.Fatal("recovery renewed finite expiry")
	}
	mustCommand(t, p.a, "proxy.stop", map[string]any{"id": run.ID})
	withServiceOperation(p.a, func() { p.a.resumeSavedProxies(context.Background()); p.a.runSavedProxyStartup(context.Background()) })
	if count.Load() != 2 || len(p.a.proxies) != 0 {
		t.Fatal("explicit stop restarted")
	}
}
func TestSavedProxyConflictRevokeDisableAndAtomicFailure(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, true)
	if _, err := command(p.a, randomID(), "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": "stale"}); networkErrorCode(err) != "proxy_saved_revision_conflict" {
		t.Fatal("stale start accepted", err)
	}
	started := mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}).(map[string]any)
	if _, err := command(p.a, randomID(), "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}); networkErrorCode(err) != "proxy_name_conflict" {
		t.Fatal("duplicate start accepted", err)
	}
	withServiceOperation(p.a, func() {
		p.a.suspendSavedProxies()
		p.a.stopAllProxies()
		p.a.stopPeerProxies("peer-b")
		p.a.resumeSavedProxies(context.Background())
	})
	if count.Load() != 1 {
		t.Fatal("revoked proxy resumed")
	}
	_ = started
	path := filepath.Join(p.a.dir, "saved-proxies.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	before := p.a.savedProxyView()["revision"]
	if _, err := command(p.a, randomID(), "proxy.saved.disable", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}); err == nil {
		t.Fatal("failed write accepted")
	}
	if p.a.savedProxyView()["revision"] != before {
		t.Fatal("failed write mutated private store")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	mustCommand(t, p.a, "proxy.saved.disable", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
	if err := p.a.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	p.a.runSavedProxyStartup(context.Background())
	if count.Load() != 1 {
		t.Fatal("disabled proxy started")
	}
}
func TestSavedProxyExpiredRecoveryAndCorruptInputStayPrivate(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, false)
	p.a.savedProxyRuns[saved.Name] = savedProxyRun{Revision: saved.Revision, Suspended: true, Expires: time.Now().Add(-time.Second)}
	p.a.resumeSavedProxies(context.Background())
	if count.Load() != 0 {
		t.Fatal("expired permission renewed")
	}
	path := filepath.Join(p.a.dir, "saved-proxies.json")
	if err := os.WriteFile(path, []byte(`{"fixture-private-password":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := p.a.loadSavedProxies(false)
	if err == nil || strings.Contains(err.Error(), "fixture-private-password") {
		t.Fatal("corrupt store leaked data", err)
	}
}

func TestSavedProxyDurableRevokeRequiresFreshReview(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, true)
	mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
	if err := p.a.loadStartup(false); err != nil {
		t.Fatal(err)
	}
	withServiceOperation(p.a, func() { p.a.runSavedProxyStartup(context.Background()) })
	if count.Load() != 0 {
		t.Fatal("reopen restored revoked proxy")
	}
	if _, err := command(p.a, randomID(), "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision}); networkErrorCode(err) != "proxy_saved_revision_conflict" {
		t.Fatal("revoked approval accepted", err)
	}
	saved = saveProxyFixture(t, p.a, false, false)
	mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
	if count.Load() != 1 {
		t.Fatal("explicit re-review failed")
	}
}
func TestSavedProxyRevokeWriteFailureStopsAndBlocks(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, true)
	mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
	path := filepath.Join(p.a.dir, "startup-revocations.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := command(p.a, randomID(), "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false}); networkErrorCode(err) != "startup_revocation_unconfirmed" {
		t.Fatal("failed durable revoke not reported", err)
	}
	if len(p.a.proxies) != 0 || !p.a.startupSuppressed {
		t.Fatal("failed revoke retained live or pending work")
	}
	withServiceOperation(p.a, func() { p.a.runSavedProxyStartup(context.Background()); p.a.resumeSavedProxies(context.Background()) })
	if count.Load() != 1 {
		t.Fatal("failed revoke restarted")
	}
	if err := p.a.loadStartup(false); err == nil {
		t.Fatal("unreadable revocation journal did not block reopen")
	}
}
