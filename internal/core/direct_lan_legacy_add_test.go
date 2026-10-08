package core

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Source-only synthetic fixtures. These tests do not construct a Node or start
// any transport. Injected publication outcomes are not durability evidence.
func legacyAdditionPeer() directlan.Peer {
	id := directlan.Identity{Seed: strings.Repeat("07", 32)}
	return directlan.Peer{Key: id.PublicKey(), TunnelKey: id.TunnelKey(), Name: "synthetic-new-peer", Endpoint: netip.MustParseAddrPort("127.0.0.5:22005")}
}

func TestLegacyAdditionRetainsManagedAndTerminalHistory(t *testing.T) {
	state, now := activeV4MixedFixture(t)
	_, store := pairRecordStoreFixture(t, state)
	projection, err := projectManagedFixedEndpoint(state)
	if err != nil {
		t.Fatal(err)
	}
	peers := append(append([]directlan.Peer{}, projection.Peers...), legacyAdditionPeer())
	// Transport sorting is not retained-file ordering or terminal membership.
	for i, j := 0, len(peers)-1; i < j; i, j = i+1, j-1 {
		peers[i], peers[j] = peers[j], peers[i]
	}
	next, err := store.prepareLegacyPeerAdditionLocked(peers, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.state, state) || !reflect.DeepEqual(next.Metadata.Peers[:len(state.Metadata.Peers)], state.Metadata.Peers) ||
		!reflect.DeepEqual(next.Peers[:len(state.Peers)], state.Peers) {
		t.Fatal("existing records changed or reducer mutated store")
	}
	newRecord := next.Metadata.Peers[len(state.Metadata.Peers)]
	if !reflect.DeepEqual(newRecord, endpointmeta.PeerRecord{Peer: directLANPeerWire(legacyAdditionPeer()), Revision: next.Metadata.Revision}) ||
		next.Metadata.Revision == state.Metadata.Revision || next.Identity != state.Identity || !reflect.DeepEqual(next.Selection, state.Selection) {
		t.Fatal("addition is not an isolated fresh legacy record")
	}
	if _, err := store.prepareLegacyEditLocked(next, now); err == nil {
		t.Fatal("generic legacy writer acquired managed mutation permission")
	}
}

func TestLegacyAdditionRejectsEveryOtherMembershipDelta(t *testing.T) {
	for _, kind := range []string{"no-change", "remove", "two-additions", "change-managed", "change-legacy", "duplicate", "revive-terminal", "reuse-terminal-tunnel", "omit-active", "invalid-key"} {
		t.Run(kind, func(t *testing.T) {
			state, now := activeV4MixedFixture(t)
			_, store := pairRecordStoreFixture(t, state)
			projection, err := projectManagedFixedEndpoint(state)
			if err != nil {
				t.Fatal(err)
			}
			peers := append(append([]directlan.Peer{}, projection.Peers...), legacyAdditionPeer())
			switch kind {
			case "no-change":
				peers = peers[:len(peers)-1]
			case "remove":
				peers = peers[:1]
			case "two-additions":
				peers = append(peers, legacyAdditionPeer())
			case "change-managed":
				peers[0].Name = "synthetic-change"
			case "change-legacy":
				peers[1].Name = "synthetic-change"
			case "duplicate":
				peers[2] = peers[0]
			case "revive-terminal":
				peers[2] = state.Peers[0]
			case "reuse-terminal-tunnel":
				peers[2].TunnelKey = state.Peers[0].TunnelKey
			case "omit-active":
				peers[0] = state.Peers[0]
			case "invalid-key":
				peers[2].Key = "invalid"
			}
			store.write = func(string, []byte) error { t.Fatal("invalid delta reached writer"); return nil }
			if _, err := store.prepareLegacyPeerAdditionLocked(peers, now); err == nil {
				t.Fatal("invalid delta accepted")
			}
			if !reflect.DeepEqual(store.state, state) {
				t.Fatal("rejected delta changed retained store")
			}
		})
	}
}

func TestLegacyAdditionRequiresCurrentReceiptAndLiveOwner(t *testing.T) {
	state, now := activeV4MixedFixture(t)
	_, store := pairRecordStoreFixture(t, state)
	projection, err := projectManagedFixedEndpoint(state)
	if err != nil {
		t.Fatal(err)
	}
	peers := append(projection.Peers, legacyAdditionPeer())
	store.write = func(string, []byte) error { t.Fatal("missing authority reached writer"); return nil }
	live := &contextSaveLiveness{ctx: context.Background(), ordinary: &managedCompletionOwner{store: store, process: "synthetic-process"}}
	if err := store.saveLegacyPeerAdditionLocked("synthetic-process", peers, now, live); err == nil {
		t.Fatal("missing receipt accepted")
	}
	store.contextPublication = &contextPublicationReceipt{store: store, process: "synthetic-process", path: store.path, file: store.fileDigest, state: privateRevision(store.state), writeRevision: store.reviewRevision}
	if err := store.saveLegacyPeerAdditionLocked("synthetic-process", peers, now, nil); err == nil {
		t.Fatal("missing liveness accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live.ctx = ctx
	if err := store.saveLegacyPeerAdditionLocked("synthetic-process", peers, now, live); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled owner accepted", err)
	}
	if !reflect.DeepEqual(store.state, state) {
		t.Fatal("denied publication changed state")
	}
}

func TestLegacyAdditionNeverWaitsForCoreOperationLock(t *testing.T) {
	c := &Core{}
	o := &managedCompletionOwner{core: c, store: &directLANStore{}}
	c.op.Lock()
	defer c.op.Unlock()
	if err := o.persistLegacyAddition(nil); !errors.Is(err, directlan.ErrUnavailable) || !o.stopped.Load() {
		t.Fatal("contended callback did not fail closed", err)
	}
}

func TestLegacyAdditionWriterFailureCannotIssueReceipt(t *testing.T) {
	state, now := activeV4MixedFixture(t)
	_, store := pairRecordStoreFixture(t, state)
	projection, err := projectManagedFixedEndpoint(state)
	if err != nil {
		t.Fatal(err)
	}
	store.contextPublication = &contextPublicationReceipt{store: store, process: "synthetic-process", path: store.path, file: store.fileDigest, state: privateRevision(store.state), writeRevision: store.reviewRevision}
	oldEpoch := directlan.NewContextEpoch()
	store.contextEpoch = oldEpoch
	writes := 0
	store.write = func(string, []byte) error { writes++; return errors.New("synthetic write failure") }
	live := &contextSaveLiveness{ctx: context.Background(), ordinary: &managedCompletionOwner{store: store, process: "synthetic-process"}}
	if err := store.saveLegacyPeerAdditionLocked("synthetic-process", append(projection.Peers, legacyAdditionPeer()), now, live); err == nil {
		t.Fatal("failed write succeeded")
	}
	if writes != 1 || !store.recovery || store.contextPublication != nil || oldEpoch.Valid() || !reflect.DeepEqual(store.state, state) {
		t.Fatal("failed publication retained authority or changed state")
	}
}
