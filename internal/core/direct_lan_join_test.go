package core

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

func TestPublicDirectLANJoinUsesTwoStageAdmission(t *testing.T) {
	// Static route coverage complements the inert runner test below. No Node
	// constructor, Start, preparer, network or product publisher is used.
	source, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	executor := strings.SplitN(string(source), "func (c *Core) executeCommand", 2)
	if len(executor) != 2 {
		t.Fatal("command executor missing")
	}
	route := strings.Index(executor[1], "return c.executeDirectLANJoin(ctx, cmd.Payload)")
	lock := strings.Index(executor[1], "c.op.Lock()")
	if route < 0 || lock < 0 || route > lock {
		t.Fatal("public join cannot reach persistence without holding Core.op")
	}
}

func TestDirectLANJoinAdmittedRunnerAllowsManagedPersistence(t *testing.T) {
	state, _ := pairRecordFixture(t)
	state.Metadata.ObservedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	c, s := pairRecordStoreFixture(t, state)
	node := &directlan.Node{}
	b := &directLANBackend{Node: node, ctx: c.ctx, store: s, ready: true}
	o := &managedCompletionOwner{core: c, backend: b, root: b, node: node, store: s, process: c.lanStartNonce,
		configuration: contextConfigurationDigest(s.state), revision: s.reviewRevision, limits: *s.currentCapacity(), limitsSource: s.limits.Load()}
	s.contextPublication = &contextPublicationReceipt{store: s, process: o.process, path: s.path, file: s.fileDigest, state: privateRevision(s.state), writeRevision: s.reviewRevision}
	o.receipt = s.contextPublication
	o.epoch = directlan.NewContextEpoch()
	o.authority.Store(o.epoch)
	s.contextEpoch = o.epoch
	b.completion, c.node = o, b
	o.root = b
	// This updates a synthetic readback fixture only. It supplies no actual
	// atomicity/durability evidence and uses the existing publisher with this injected writer.
	s.write = func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	addition := legacyAdditionPeer()
	a := &directLANJoinAdmission{core: c, backend: b, owner: o, store: s, node: node,
		invitation: directlan.Invitation{Host: addition}, limits: *s.currentCapacity(), limitsSource: s.limits.Load()}
	result, err := a.run(context.Background(), func(ctx context.Context, invitation directlan.Invitation) error {
		if !c.op.TryLock() {
			t.Fatal("pairing exchange still holds Core.op")
		}
		c.op.Unlock()
		if _, bounded := ctx.Deadline(); !bounded || invitation.Host != addition {
			t.Fatal("admitted payload or finite bound changed")
		}
		return o.persistLegacyAddition(append(append([]directlan.Peer{}, state.Peers...), addition))
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.(map[string]any)
	if !ok || value["paired"] != true || value["trusted"] != false || value["peerId"] != addition.Key || !o.authorityCurrent() {
		t.Fatal("admitted join did not finish with current authority", result)
	}
}

func TestLegacyAdditionCannotReviveFrozenCompletionSlot(t *testing.T) {
	state, _ := pairRecordFixture(t)
	state.Metadata.ObservedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	c, s := pairRecordStoreFixture(t, state)
	node := &directlan.Node{}
	b := &directLANBackend{Node: node, ctx: c.ctx, store: s, ready: true}
	o := &managedCompletionOwner{core: c, backend: b, root: b, node: node, store: s, process: c.lanStartNonce, configuration: contextConfigurationDigest(s.state), revision: s.reviewRevision, limits: *s.currentCapacity(), limitsSource: s.limits.Load()}
	s.contextPublication = &contextPublicationReceipt{store: s, process: o.process, path: s.path, file: s.fileDigest, state: privateRevision(s.state), writeRevision: s.reviewRevision}
	o.receipt = s.contextPublication
	o.epoch = directlan.NewContextEpoch()
	o.authority.Store(o.epoch)
	s.contextEpoch = o.epoch
	b.completion = o
	c.node = b
	old := &managedCompletionSlot{owner: o, epoch: o.epoch, receipt: o.receipt, revision: o.revision, deadline: time.Now().Add(time.Minute)}
	s.write = func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	if err := o.persistLegacyAddition(append(append([]directlan.Peer{}, state.Peers...), legacyAdditionPeer())); err != nil {
		t.Fatal(err)
	}
	if old.epoch == o.epoch || old.epoch.Valid() || old.receipt == o.receipt || old.revision == o.revision {
		t.Fatal("old response scope refreshed")
	}
	if old.admit(context.Background()) || !old.used.Load() {
		t.Fatal("old token regained admission")
	}
}
