package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Saved synthetic data only: no publication, Node constructor, lifecycle,
// generation preparation, transport, signed movement or application bytes.
func TestCurrentLegacyAdditionPreservesRetainedEndpointHistory(t *testing.T) {
	for _, inactive := range []bool{false, true} {
		_, s, key := localEndpointExportFixture(t, func(state *directLANState) {
			currentCoreProjectionSignedState(t, state)
			if inactive {
				e := state.Metadata.Peers[0].EndpointState
				e.Approval, e.ReceiveStatus = nil, "locally_revoked"
			}
		})
		now := time.Now()
		projection, err := s.managedCurrentEndpointProjectionLocked(now)
		if err != nil {
			t.Fatal(err)
		}
		if inactive && len(projection.Peers) != 0 {
			t.Fatal("inactive history retained positive authority")
		}
		before := cloneDirectLANState(s.state)
		peers := append(append([]directlan.Peer{}, projection.Peers...), legacyAdditionPeer())
		next, err := s.prepareLegacyPeerAdditionLocked(peers, now)
		if err != nil {
			t.Fatal("current legacy delta", err)
		}
		if !reflect.DeepEqual(before, s.state) || !reflect.DeepEqual(next.Metadata.Peers[0], before.Metadata.Peers[0]) || next.Peers[0] != before.Peers[0] {
			t.Fatal("retained endpoint proof, approval or membership changed")
		}
		projected, err := projectManagedCurrentEndpoint(next, now)
		if err != nil || len(projected.Peers) != len(peers) {
			t.Fatal("new legacy membership", err)
		}
		if inactive {
			if !reflect.DeepEqual(projected.InactiveEndpointPeerKeys(), []string{key}) {
				t.Fatal("inactive classification lost")
			}
			if _, err := s.prepareLegacyPeerAdditionLocked([]directlan.Peer{before.Peers[0]}, now); err == nil {
				t.Fatal("retained inactive peer reintroduced as legacy addition")
			}
		}
	}
}

func TestCurrentLegacyAdditionPublicationLivenessUsesOriginalSignals(t *testing.T) {
	epoch := directlan.NewContextEpoch()
	epoch.Invalidate()
	backend := &directLANBackend{}
	owner := &managedCompletionOwner{backend: backend, epoch: epoch, deadline: time.Now().Add(time.Minute)}
	live := &contextSaveLiveness{ctx: context.Background(), ordinary: owner}
	if err := live.err(); err != nil {
		t.Fatal("publisher's expected epoch invalidation rejected live write", err)
	}
	backend.endpointStopped.Store(true)
	if err := live.err(); !errors.Is(err, context.Canceled) {
		t.Fatal("backend Stop signal omitted", err)
	}
	backend.endpointStopped.Store(false)
	owner.deadline = time.Now().Add(-time.Second)
	if err := live.err(); !errors.Is(err, endpointmeta.ErrExpired) {
		t.Fatal("original deadline omitted", err)
	}
}

func TestCurrentEndpointReopenCannotUseReadAsReceipt(t *testing.T) {
	c, s, _ := localEndpointExportFixture(t, func(state *directLANState) { currentCoreProjectionSignedState(t, state) })
	before := cloneDirectLANState(s.state)
	s.write = func(string, []byte) error { t.Fatal("unavailable startup attempted publication"); return nil }
	a, err := s.captureActivationLocked(c, time.Now())
	if err != nil || a == nil || !a.currentEndpoints || len(a.deadlines) != 2 {
		t.Fatal("current endpoint admission", err)
	}
	if _, err := c.newManagedCompletionBackendLocked(s, a); err == nil {
		t.Fatal("saved flags constructed without actual publication receipt")
	}
	if s.contextPublication != nil || s.contextEpoch != nil || !reflect.DeepEqual(before, s.state) {
		t.Fatal("failed reopen minted authority or changed evidence")
	}
}
