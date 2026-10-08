package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// No Core.Open, manager, listener, process or network is started by these
// fixtures. Only an owned temporary private directory and real atomic files.
func resourceFixture(t *testing.T) (*Core, *config.Lock) {
	t.Helper()
	dir := t.TempDir()
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	c := &Core{ctx: context.Background(), dir: dir, capacity: capacity.Defaults(), profile: Profile{Version: 1, Settings: Settings{Network: "none", Locale: "auto", Theme: "system", Hostname: "synthetic-node"}}, requests: map[string]requestResult{}}
	c.initializeResourceIdentity(lock)
	if c.resourceIdentity == "" {
		t.Fatal("identity unavailable")
	}
	return c, lock
}
func resourceCall(t *testing.T, c *Core, name string, payload any) any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	out, err := c.Command(context.Background(), webui.Command{RequestID: "same-request", Name: name, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func resourceTarget(c *Core) resource.Target {
	return resource.Target{SchemaVersion: 1, ResourceID: c.resourceIdentity}
}
func TestResourceIdentityRestartRenameAndOwnership(t *testing.T) {
	c, lock := resourceFixture(t)
	id, nonce := c.resourceIdentity, c.resourceNonce
	before := c.lanStartWriteRevision.Load()
	c.initializeResourceIdentity(lock)
	if c.resourceIdentity != id || c.resourceNonce == nonce || c.lanStartWriteRevision.Load() != before {
		t.Fatal("reopen changed identity or authority accounting")
	}
	renamed := filepath.Join(t.TempDir(), "renamed")
	if err := os.Rename(c.dir, renamed); err != nil {
		t.Fatal(err)
	}
	c.dir = renamed
	c.initializeResourceIdentity(lock)
	if c.resourceIdentity != id {
		t.Fatal("rename changed identity")
	}
	other := &Core{dir: t.TempDir()}
	other.initializeResourceIdentity(lock)
	if other.resourceIdentity != "" {
		t.Fatal("unrelated lock accepted")
	}
	if _, err := os.Stat(filepath.Dir(resourceStatePath(other.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unowned directory mutated")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.resourceCommand("resource.list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("released lifecycle accepted")
	}
	c.initializeResourceIdentity(nil)
	if c.resourceIdentity != "" {
		t.Fatal("nil ownership accepted")
	}
}
func TestResourceStartupFailsClosedWithoutRegeneration(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"schemaVersion":2,"resourceId":"0123456789abcdef0123456789abcdef","highWater":0,"records":[]}`,
		`{"schemaVersion":1,"resourceId":"bad","highWater":0,"records":[]}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":0,"highWater":0,"records":[]}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":null,"records":[]}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":18446744073709551616,"records":[]}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":1,"records":[{}]}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":0,"records":null}`,
		`{"schemaVersion":1,"resourceId":"0123456789abcdef0123456789abcdef","highWater":0,"records":[],"extra":1}`,
		strings.Repeat(" ", resourceStateMaxBytes+1),
	} {
		t.Run("invalid", func(t *testing.T) {
			c, lock := resourceFixture(t)
			path := resourceStatePath(c.dir)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			writes := 0
			c.atomicWrite = func(string, []byte) error { writes++; return nil }
			c.initializeResourceIdentity(lock)
			if c.resourceIdentity != "" || writes != 0 {
				t.Fatal("malformed state rewritten")
			}
			got, _ := os.ReadFile(path)
			if string(got) != data {
				t.Fatal("state replaced")
			}
			if _, err := c.capacityCommand("policy.config", json.RawMessage(`{}`)); err != nil {
				t.Fatal("legacy feature disabled", err)
			}
		})
	}
}
func TestResourceStartupRepublishMustSucceed(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run("republish", func(t *testing.T) {
			c, lock := resourceFixture(t)
			id := c.resourceIdentity
			calls := 0
			c.atomicWrite = func(path string, data []byte) error {
				calls++
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						t.Fatal(err)
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic private path failure")
			}
			c.initializeResourceIdentity(lock)
			if calls != 1 || c.resourceIdentity != "" {
				t.Fatal("uncertain republish exposed identity")
			}
			_, err := c.resourceCommand("resource.list", json.RawMessage(`{}`))
			if err == nil || strings.Contains(err.Error(), "synthetic private") {
				t.Fatal("unsafe public error", err)
			}
			c.atomicWrite = nil
			c.initializeResourceIdentity(lock)
			if c.resourceIdentity != id {
				t.Fatal("certified reopen lost identity")
			}
		})
	}
}
func TestResourcePreviewReadOnlyProjectionAndPrivacy(t *testing.T) {
	c, _ := resourceFixture(t)
	c.capacity.Logical["savedServices"] = capacity.Unlimited()
	c.capacity.Resources["profileBytes"] = capacity.Limited(300000)
	before := c.capacity.Clone()
	fileBefore, _ := os.ReadFile(resourceStatePath(c.dir))
	writes := 0
	c.atomicWrite = func(string, []byte) error { writes++; return errors.New("unexpected write") }
	list := resourceCall(t, c, "resource.list", struct{}{}).(resource.Catalog)
	if len(list.Resources) != 1 || strings.Join(list.Resources[0].Operations, ",") != "list,inspect,preview,apply,operation.status" {
		t.Fatal("unsupported operations advertised")
	}
	req := resource.PreviewRequest{Target: resourceTarget(c), Settings: resource.Settings{TransferConcurrentFiles: capacity.Limited(4), TransferConcurrentPerPeer: capacity.Default()}}
	preview := resourceCall(t, c, "resource.preview", req).(resource.Preview)
	if preview.Destructive || preview.Effective.TransferConcurrentFiles != 4 || preview.Requested.TransferConcurrentPerPeer.Mode != "default" {
		t.Fatal("wrong preview")
	}
	if !capacityJSONEqual(before, c.capacity) || writes != 0 {
		t.Fatal("preview mutated policy")
	}
	fileAfter, _ := os.ReadFile(resourceStatePath(c.dir))
	if string(fileBefore) != string(fileAfter) {
		t.Fatal("preview wrote evidence")
	}
	proposed := resourceProjection(before, req.Settings)
	delete(proposed.Resources, "transferConcurrentFiles")
	delete(proposed.Resources, "transferConcurrentPerPeer")
	delete(before.Resources, "transferConcurrentFiles")
	delete(before.Resources, "transferConcurrentPerPeer")
	if !capacityJSONEqual(before, proposed) {
		t.Fatal("unrelated settings changed")
	}
	for _, value := range []any{list, preview, resourceCall(t, c, "resource.inspect", resourceTarget(c))} {
		raw, _ := json.Marshal(value)
		for _, private := range []string{c.dir, "synthetic-node", "profileBytes", "savedServices", "peers", "receiveDirectory"} {
			if strings.Contains(string(raw), private) {
				t.Fatal("private data leaked", private)
			}
		}
	}
}
func TestResourceRevisionTracksLegacyABAAndRestart(t *testing.T) {
	c, lock := resourceFixture(t)
	target := resourceTarget(c)
	first := resourceCall(t, c, "resource.inspect", target).(resource.Descriptor)
	original := c.capacity.Clone()
	changed := original.Clone()
	changed.Resources["transferConcurrentFiles"] = capacity.Limited(3)
	for _, policy := range []capacity.Policy{changed, original} {
		req, _ := json.Marshal(struct {
			Policy capacity.Policy `json:"policy"`
		}{policy})
		view, err := c.capacityCommand("policy.preview", req)
		if err != nil {
			t.Fatal(err)
		}
		apply, _ := json.Marshal(map[string]any{"policy": policy, "expectedRevision": view.(map[string]any)["revision"]})
		if _, err := c.capacityCommand("policy.apply", apply); err != nil {
			t.Fatal(err)
		}
	}
	after := resourceCall(t, c, "resource.inspect", target).(resource.Descriptor)
	if first.Revision == after.Revision || !capacityJSONEqual(c.capacity, original) {
		t.Fatal("ABA review remained valid")
	}
	c.initializeResourceIdentity(lock)
	reopened := resourceCall(t, c, "resource.inspect", target).(resource.Descriptor)
	if reopened.Revision == after.Revision {
		t.Fatal("review survived restart")
	}
	if len(c.requests) != 0 {
		t.Fatal("resource command cached")
	}
	for _, name := range []string{"resource.apply", "resource.operation.status", "resource.future"} {
		_, err := c.Command(context.Background(), webui.Command{RequestID: "same-request", Name: name, Payload: json.RawMessage(`{}`)})
		if err == nil || len(c.requests) != 0 {
			t.Fatal("unknown resource command supported or cached")
		}
	}
}
func TestResourceSidecarSymlinkRejected(t *testing.T) {
	c, lock := resourceFixture(t)
	path := resourceStatePath(c.dir)
	target := filepath.Join(t.TempDir(), "target")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlink unavailable")
	}
	c.initializeResourceIdentity(lock)
	if c.resourceIdentity != "" {
		t.Fatal("symlink accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(data) {
		t.Fatal("symlink target changed")
	}
}

func TestResourceMissingAdoptionFailureAndRetry(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run("adoption", func(t *testing.T) {
			c, owner := resourceFixture(t)
			path := resourceStatePath(c.dir)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			old := c.resourceIdentity
			writes := 0
			proposedID := ""
			c.atomicWrite = func(path string, data []byte) error {
				writes++
				var state resourceEnvelope
				if err := json.Unmarshal(data, &state); err != nil {
					t.Fatal(err)
				}
				proposedID = state.ResourceID
				if committed {
					if err := config.AtomicWrite(path, data); err != nil {
						t.Fatal(err)
					}
					return config.ErrAtomicCommitted
				}
				return errors.New("synthetic disk full")
			}
			c.initializeResourceIdentity(owner)
			if writes != 1 || c.resourceIdentity != "" || proposedID == old {
				t.Fatal("failed adoption exposed/reused identity")
			}
			c.atomicWrite = nil
			c.initializeResourceIdentity(owner)
			if c.resourceIdentity == "" || c.resourceIdentity == old {
				t.Fatal("reopen did not adopt")
			}
			if committed && c.resourceIdentity != proposedID {
				t.Fatal("uncertain published identity regenerated")
			}
		})
	}
}
func TestResourceQueuedCancellationAndFailedWriteInvalidation(t *testing.T) {
	c, _ := resourceFixture(t)
	target := resourceTarget(c)
	first := resourceCall(t, c, "resource.inspect", target).(resource.Descriptor)
	c.atomicWrite = func(string, []byte) error { return errors.New("synthetic failed write") }
	if err := c.writeAtomic(filepath.Join(c.dir, "capacity.json"), []byte("unused")); err == nil {
		t.Fatal("fixture did not fail")
	}
	after := resourceCall(t, c, "resource.inspect", target).(resource.Descriptor)
	if first.Revision == after.Revision {
		t.Fatal("failed authority attempt did not invalidate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(target)
	if _, err := c.Command(ctx, webui.Command{RequestID: "canceled", Name: "resource.inspect", Payload: raw}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
