package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"path/filepath"
	"testing"
)

func TestMixedStrictBoundaryPreflightBeforeAnyWorker(t *testing.T) {
	c, e := Open(context.Background(), Options{Directory: t.TempDir(), SkipNetworkStart: true})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	state := mixedState{Version: 1, Seed: hex.EncodeToString(key.Seed()), Selection: MixedSelection{Backends: []string{"tailnet", "lan"}}}
	raw, _ := json.Marshal(state)
	if e = c.writeAtomic(filepath.Join(c.dir, "mixed.json"), raw); e != nil {
		t.Fatal(e)
	}
	c.lan = &lanStore{state: lanState{DestinationPolicy: lanpolicy.Config{Mode: lanpolicy.AllowedLANDestinations, Prefixes: []string{"127.0.0.0/8"}}}}
	called := false
	_, e = c.newMixedBackendUsing(func(context.Context, string, string, string) (NetworkBackend, error) { called = true; return nil, nil })
	if e == nil || called {
		t.Fatal("external worker started before strict boundary validation", e, called)
	}
}
func TestMixedWorkerBudgetsReviewAndApply(t *testing.T) {
	pair := newCorePair(t)
	policy := capacity.Defaults()
	policy.Resources["workerFrameBytes"] = capacity.Limited(2 << 20)
	policy.Resources["workerRequests"] = capacity.Limited(256)
	policy.Resources["workerHandles"] = capacity.Limited(2048)
	preview := mustCommand(t, pair.a, "policy.preview", map[string]any{"policy": policy}).(map[string]any)
	mustCommand(t, pair.a, "policy.apply", map[string]any{"policy": policy, "expectedRevision": preview["revision"]})
	limits, e := selectedWorkerLimits(pair.a.capacityPolicy())
	if e != nil || limits.FrameBytes != 2<<20 || limits.Requests != 256 || limits.Handles != 2048 {
		t.Fatal(limits, e)
	}
}
