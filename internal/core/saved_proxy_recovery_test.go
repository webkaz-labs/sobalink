package core

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

func saveReviewedRecoveryProxy(t *testing.T, c *Core, scope ProxyScope) SavedProxyView {
	t.Helper()
	review := previewProxy(t, c, scope)
	return mustCommand(t, c, "proxy.save", map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "expectedStoreRevision": c.savedProxyView()["revision"], "username": "fixture-user", "password": "fixture-private-password", "startOnLaunch": false}).(SavedProxyView)
}
func TestSavedProxyRecoveryRetainsAdmissionAfterCapacityReduction(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	p.na.mu.Lock()
	p.na.state.Snapshot.Peers = append(p.na.state.Snapshot.Peers, policy.Peer{ID: "peer-c", DNSName: "peer-c.invalid", IPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}})
	p.na.mu.Unlock()
	original := map[string]savedProxyRun{}
	for i, name := range []string{"first", "second"} {
		scope := proxyFixture()
		scope.Name = name
		scope.LocalPort = 1080 + i
		scope.Lifetime = "finite"
		scope.TTLSeconds = 3600
		scope.Targets = append(scope.Targets, ProxyTarget{PeerID: "peer-c", Port: 443})
		saved := saveReviewedRecoveryProxy(t, p.a, scope)
		mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
		original[name] = p.a.savedProxyRuns[name]
	}
	next := p.a.capacityPolicy()
	next.Logical["sharePeers"] = capacity.Limited(1)
	next.Resources["materializedListeners"] = capacity.Limited(1)
	preview := mustCommand(t, p.a, "policy.preview", map[string]any{"policy": next}).(map[string]any)
	mustCommand(t, p.a, "policy.apply", map[string]any{"policy": next, "expectedRevision": preview["revision"]})
	withServiceOperation(p.a, func() { p.a.suspendSavedProxies(); p.a.stopAllProxies() })
	if p.a.materializedCount() != 2 || p.a.capacityUsage()["materializedListeners"] != 2 || len(p.a.proxyViews()) != 2 {
		t.Fatal("suspension lost retained reservation or stop control")
	}
	before := p.a.proxyStart
	failures := 2
	p.a.proxyStart = func(ctx context.Context, cfg transport.SOCKSConfig, d transport.Dialer) (proxyServer, error) {
		if failures > 0 {
			failures--
			return nil, errors.New("synthetic temporary listener failure")
		}
		return before(ctx, cfg, d)
	}
	withServiceOperation(p.a, func() { p.a.resumeSavedProxies(context.Background()) })
	if p.a.materializedCount() != 2 || len(p.a.savedProxyRuns) != 2 || len(p.a.proxies) != 0 {
		t.Fatal("temporary failure dropped admitted retry state")
	}
	withServiceOperation(p.a, func() { p.a.resumeSavedProxies(context.Background()) })
	if count.Load() != 4 || len(p.a.proxies) != 2 || p.a.materializedCount() != 2 {
		t.Fatal("lower capacity evicted retained permission", count.Load(), len(p.a.proxies))
	}
	for name, was := range original {
		now := p.a.savedProxyRuns[name]
		if now.ID != was.ID || now.Suspended || !now.Expires.Equal(was.Expires) || privateRevision(now.Scope) != privateRevision(was.Scope) {
			t.Fatal("recovery changed admitted scope, identity or expiry")
		}
	}
}
func TestSavedProxySuspendedStopRevokeAndExpiryCancelRetry(t *testing.T) {
	for _, operation := range []string{"stop", "revoke", "expiry", "disable"} {
		t.Run(operation, func(t *testing.T) {
			p := newCorePair(t)
			_, _, count := installProxyStarter(t, p.a)
			saved := saveProxyFixture(t, p.a, false, false)
			mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
			run := p.a.savedProxyRuns[saved.Name]
			withServiceOperation(p.a, func() { p.a.suspendSavedProxies(); p.a.stopAllProxies() })
			switch operation {
			case "stop":
				mustCommand(t, p.a, "proxy.stop", map[string]any{"id": run.ID})
			case "revoke":
				mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
			case "disable":
				mustCommand(t, p.a, "proxy.saved.disable", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
			case "expiry":
				p.a.mu.Lock()
				run = p.a.savedProxyRuns[saved.Name]
				run.Expires = time.Now().Add(-time.Second)
				p.a.savedProxyRuns[saved.Name] = run
				p.a.mu.Unlock()
			}
			withServiceOperation(p.a, func() { p.a.expireProxies(); p.a.resumeSavedProxies(context.Background()) })
			if count.Load() != 1 || p.a.materializedCount() != 0 || len(p.a.savedProxyRuns) != 0 {
				t.Fatal("cancelled permission retained recovery or capacity")
			}
		})
	}
}
func TestSavedProxyPreviewsBindEpochAndHostnameBeforeFirstSave(t *testing.T) {
	for _, action := range []string{"proxy.save", "proxy.generate"} {
		for _, change := range []string{"revoke", "hostname"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				p := newCorePair(t)
				_, _, count := installProxyStarter(t, p.a)
				review := previewProxy(t, p.a, proxyFixture())
				store := p.a.savedProxyView()["revision"]
				if change == "revoke" {
					mustCommand(t, p.a, "peer.trust", map[string]any{"peerId": "peer-b", "trusted": false})
				} else {
					p.a.mu.Lock()
					p.a.profile.Settings.Hostname = "changed-node"
					p.a.mu.Unlock()
				}
				input := map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "expectedStoreRevision": store, "startOnLaunch": false}
				if action == "proxy.save" {
					input["username"] = "fixture-user"
					input["password"] = "fixture-private-password"
				}
				if _, err := command(p.a, randomID(), action, input); networkErrorCode(err) != "proxy_saved_revision_conflict" {
					t.Fatal("stale review silently adopted new identity/epoch", err)
				}
				current := previewProxy(t, p.a, review.Scope)
				if review.Revision == current.Revision || store == p.a.savedProxyView()["revision"] || len(p.a.savedProxies.Entries) != 0 || count.Load() != 0 {
					t.Fatal("revocation/hostname not bound to both review tokens")
				}
				input["expectedRevision"] = current.Revision
				input["expectedStoreRevision"] = p.a.savedProxyView()["revision"]
				mustCommand(t, p.a, action, input)
			})
		}
	}
}

func TestSavedProxySuspendedNameCannotBeClaimedByEphemeralStart(t *testing.T) {
	p := newCorePair(t)
	_, _, count := installProxyStarter(t, p.a)
	saved := saveProxyFixture(t, p.a, false, false)
	mustCommand(t, p.a, "proxy.saved.start", map[string]any{"name": saved.Name, "expectedRevision": saved.Revision})
	original := p.a.savedProxyRuns[saved.Name]
	withServiceOperation(p.a, func() { p.a.suspendSavedProxies(); p.a.stopAllProxies() })
	review := previewProxy(t, p.a, proxyFixture())
	if _, err := command(p.a, randomID(), "proxy.start", map[string]any{"scope": review.Scope, "expectedRevision": review.Revision, "username": "fixture-new-user", "password": "fixture-new-password"}); networkErrorCode(err) != "proxy_name_conflict" {
		t.Fatal("ephemeral start claimed a suspended saved name", err)
	}
	if count.Load() != 1 || p.a.materializedCount() != 1 {
		t.Fatal("failed conflicting start changed retained admission")
	}
	withServiceOperation(p.a, func() { p.a.resumeSavedProxies(context.Background()) })
	run := p.a.savedProxyRuns[saved.Name]
	if count.Load() != 2 || run.ID != original.ID || !run.Expires.Equal(original.Expires) {
		t.Fatal("conflicting request lost the original recovery")
	}
}
