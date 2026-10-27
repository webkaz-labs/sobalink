package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func resumeTestCore(t *testing.T) *Core {
	c := openLANTestCore(t)
	c.mu.Lock()
	c.profile.Settings.Network = "tailnet"
	c.transferNetwork = "tailnet"
	c.startupSuppressed = true
	c.mu.Unlock()
	return c
}
func resumeReview(t *testing.T, c *Core) *NetworkResumeReview {
	t.Helper()
	result, err := command(c, randomID(), "network.resume.preview", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return result.(*NetworkResumeReview)
}
func TestNetworkResumeReviewIsReadOnlyAndProcessBound(t *testing.T) {
	c := resumeTestCore(t)
	before, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	view := resumeReview(t, c)
	if !view.ExternalControlTraffic || view.Mode != "tailnet" || !view.StartupSuppressed || !view.SavedProxiesSuppressed {
		t.Fatal("incorrect resume scope")
	}
	raw, _ := json.Marshal(view)
	if bytes.Contains(raw, []byte(c.dir)) || bytes.Contains(raw, []byte(c.lanStartNonce)) {
		t.Fatal("private review leaked ownership")
	}
	if resumeReview(t, c).Revision != view.Revision || c.nodeCopy() != nil {
		t.Fatal("preview changed state")
	}
	after, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview wrote profile")
	}
	other := resumeTestCore(t)
	if resumeReview(t, other).Revision == view.Revision {
		t.Fatal("review crosses owner")
	}
}
func TestNetworkResumeRejectsChangedAuthorityAndExtraSettings(t *testing.T) {
	for _, change := range []string{"profile", "startup", "proxy", "capacity", "process"} {
		t.Run(change, func(t *testing.T) {
			c := resumeTestCore(t)
			v := resumeReview(t, c)
			c.mu.Lock()
			switch change {
			case "profile":
				c.profile.Settings.Hostname = "changed"
			case "startup":
				c.startupPending["synthetic"] = "changed"
			case "proxy":
				c.savedProxyPending["synthetic"] = "changed"
			case "capacity":
				c.lanStartWriteRevision.Add(1)
			case "process":
				c.lanStartNonce = "changed"
			}
			c.mu.Unlock()
			_, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision})
			if err == nil {
				t.Fatal("stale review admitted")
			}
		})
	}
	c := resumeTestCore(t)
	v := resumeReview(t, c)
	for _, payload := range []map[string]any{{}, {"expectedRevision": v.Revision, "mode": "lan"}, {"expectedRevision": v.Revision, "hostname": "changed"}} {
		if _, err := command(c, randomID(), "network.resume", payload); err == nil {
			t.Fatal("unreviewed change admitted")
		}
	}
}
func TestNetworkResumeStartsOncePreservesSuppressionAndReportsFailure(t *testing.T) {
	c := resumeTestCore(t)
	v := resumeReview(t, c)
	starts := 0
	c.factory = func(string, string) (NetworkBackend, error) { starts++; return failedStartBackend{}, nil }
	if _, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision}); err == nil {
		t.Fatal("failure reported success")
	}
	if starts != 1 || c.attemptedNetwork != "tailnet" || !c.startupSuppressed || len(c.startupPending) != 0 || len(c.savedProxyPending) != 0 {
		t.Fatal("resume altered scope")
	}
	if _, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision}); err == nil || starts != 1 {
		t.Fatal("repeat started backend again")
	}
}
func TestNetworkResumeCancellationAndNoSelectionDoNotStart(t *testing.T) {
	c := resumeTestCore(t)
	v := resumeReview(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload, _ := json.Marshal(map[string]string{"expectedRevision": v.Revision})
	_, err := c.Command(ctx, webui.Command{RequestID: randomID(), Name: "network.resume", Payload: payload})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled command admitted", err)
	}
	c.mu.Lock()
	c.profile.Settings.Network = "none"
	c.mu.Unlock()
	if _, err := command(c, randomID(), "network.resume.preview", map[string]any{}); err == nil {
		t.Fatal("missing network admitted")
	}
}

func TestNetworkResumeSuccessStartsOnlySavedBackend(t *testing.T) {
	c := resumeTestCore(t)
	v := resumeReview(t, c)
	calls := 0
	node := &pipeNode{hub: &pipeNetwork{listeners: map[netip.AddrPort]*pipeListener{}}}
	c.factory = func(dir, name string) (NetworkBackend, error) {
		calls++
		if dir != c.dir || name != v.Hostname {
			t.Fatal("changed backend target")
		}
		return node, nil
	}
	before := privateRevision(c.profileCopy())
	result, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision})
	if err != nil || result.(map[string]string)["state"] != "starting" || calls != 1 || c.networkReady.Load() {
		t.Fatal("incorrect startup result", err)
	}
	if privateRevision(c.profileCopy()) != before || !c.startupSuppressed || len(c.startupPending) != 0 || len(c.savedProxyPending) != 0 {
		t.Fatal("resume changed saved permissions")
	}
	if _, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision}); err == nil || calls != 1 {
		t.Fatal("repeat started again")
	}
}
func TestNetworkResumeLANAndDirectReviewTracksPrivateAuthority(t *testing.T) {
	for _, mode := range []string{"lan", "direct-lan"} {
		t.Run(mode, func(t *testing.T) {
			c := resumeTestCore(t)
			if mode == "lan" {
				if err := c.configureLAN(testLANSelection()); err != nil {
					t.Fatal(err)
				}
			} else {
				configureDirectLANTest(t, c)
			}
			c.mu.Lock()
			c.profile.Settings.Network = mode
			c.mu.Unlock()
			v := resumeReview(t, c)
			if v.ExternalControlTraffic || (mode == "lan" && v.LAN == nil) || (mode == "direct-lan" && v.DirectLAN == nil) {
				t.Fatal("wrong saved scope")
			}
			if mode == "lan" {
				s := c.lanStoreCopy()
				s.mu.Lock()
				s.reviewRevision++
				s.mu.Unlock()
			} else {
				s := c.directLANStoreCopy()
				s.mu.Lock()
				s.reviewRevision++
				s.mu.Unlock()
			}
			if _, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision}); err == nil {
				t.Fatal("changed authority accepted")
			}
			if mode == "lan" {
				s := c.lanStoreCopy()
				s.mu.Lock()
				s.startUncertain = true
				s.mu.Unlock()
			} else {
				s := c.directLANStoreCopy()
				s.mu.Lock()
				s.recovery = true
				s.mu.Unlock()
			}
			if _, err := command(c, randomID(), "network.resume.preview", map[string]any{}); err == nil {
				t.Fatal("uncertain state previewed as resumable")
			}
		})
	}
}
func TestNetworkResumeReviewListsOnlyActiveAutomaticReceivers(t *testing.T) {
	c := resumeTestCore(t)
	c.mu.Lock()
	c.profile.Settings.ReceiveDirectory = "synthetic-default"
	c.profile.Peers = []Trust{{ID: "one", Network: "tailnet", Autosave: true}, {ID: "two", Network: "tailnet", Autosave: true, Paused: true}, {ID: "three", Network: "lan", Autosave: true}}
	c.mu.Unlock()
	v := resumeReview(t, c)
	if v.TrustedDevices != 2 || len(v.AutomaticReceivers) != 1 || v.AutomaticReceivers[0].Directory != "synthetic-default" {
		t.Fatal("incorrect receiving scope")
	}
}

func TestNetworkResumeMixedReviewTracksExactBackendOrder(t *testing.T) {
	c := resumeTestCore(t)
	configureDirectLANTest(t, c)
	if err := c.configureMixed(&MixedSelection{Backends: []string{"direct-lan", "tailnet"}}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.profile.Settings.Network = "mixed"
	c.mu.Unlock()
	v := resumeReview(t, c)
	if !v.ExternalControlTraffic || v.DirectLAN == nil || len(v.Backends) != 2 || v.Backends[0] != "direct-lan" {
		t.Fatal("missing mixed impact")
	}
	if err := c.configureMixed(&MixedSelection{Backends: []string{"tailnet", "direct-lan"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := command(c, randomID(), "network.resume", map[string]any{"expectedRevision": v.Revision}); err == nil {
		t.Fatal("changed backend order admitted")
	}
}
func TestNetworkResumeRejectsNonOfflineOwner(t *testing.T) {
	c := resumeTestCore(t)
	c.mu.Lock()
	c.startupSuppressed = false
	c.mu.Unlock()
	if _, err := command(c, randomID(), "network.resume.preview", map[string]any{}); err == nil {
		t.Fatal("ordinary owner gained offline resume entry")
	}
}
