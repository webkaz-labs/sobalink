package transfer

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestReceiveSettingsCommitOnceBeforeAtomicPolicyAndPause(t *testing.T) {
	m, peer := testManager(t, Options{})
	policy := ReceivePolicy{Peer: peer, Destination: t.TempDir(), AutoAccept: true}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	saves := 0
	var saved []ReceivePolicy
	go func() {
		done <- m.UpdateReceiveSettings(peer, &policy, true, func(policies []ReceivePolicy) error {
			saves++
			saved = append([]ReceivePolicy(nil), policies...)
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("settings did not reach persistence")
	}
	offered := make(chan error, 1)
	go func() {
		_, err := m.Offer(peer, testManifest("during-update", testEntry("file", "file", "payload")))
		offered <- err
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-offered:
		if !errors.Is(err, ErrPeerPaused) {
			t.Fatal("offer saw enabled autosave before its combined pause", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("offer did not observe the committed settings")
	}
	if saves != 1 || !reflect.DeepEqual(saved, []ReceivePolicy{policy}) || !reflect.DeepEqual(m.ReceivePolicies(), saved) {
		t.Fatal("combined settings required multiple saves or published a different policy")
	}
}

func TestReceiveSettingsFailurePreservesPolicyPauseAndFailClosedRemoval(t *testing.T) {
	m, peer := testManager(t, Options{})
	old := ReceivePolicy{Peer: peer, Destination: t.TempDir(), AutoAccept: true}
	if err := m.SetReceivePolicy(old); err != nil {
		t.Fatal(err)
	}
	changed := ReceivePolicy{Peer: peer, Destination: t.TempDir(), AutoAccept: true}
	failure := errors.New("synthetic combined save failure")
	saves := 0
	persist := func([]ReceivePolicy) error { saves++; return failure }
	if err := m.UpdateReceiveSettings(peer, &changed, true, persist); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if saves != 1 || !reflect.DeepEqual(m.ReceivePolicies(), []ReceivePolicy{old}) || m.peers[peer.ID].paused {
		t.Fatal("failed combined save changed live directory, policy or pause")
	}
	disabled := ReceivePolicy{Peer: peer, AutoAccept: false}
	if err := m.UpdateReceiveSettings(peer, &disabled, true, persist); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if saves != 2 || len(m.ReceivePolicies()) != 0 || m.peers[peer.ID].paused {
		t.Fatal("failed policy removal did not preserve fail-closed behavior")
	}
}

func TestPauseOnlySettingsDoNotReopenAnUnavailableDirectory(t *testing.T) {
	m, peer := testManager(t, Options{})
	directory := t.TempDir()
	policy := ReceivePolicy{Peer: peer, Destination: directory, AutoAccept: true}
	if err := m.SetReceivePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	saves := 0
	if err := m.UpdateReceiveSettings(peer, nil, true, func([]ReceivePolicy) error { saves++; return nil }); err != nil || saves != 1 || !m.peers[peer.ID].paused {
		t.Fatal("pausing was blocked by an unavailable receive directory", err)
	}
	wrong := peer
	wrong.Generation++
	if err := m.UpdateReceiveSettings(wrong, nil, false, func([]ReceivePolicy) error { saves++; return nil }); !errors.Is(err, ErrPeerChanged) || saves != 1 {
		t.Fatal("stale trust generation reached persistence")
	}
}
