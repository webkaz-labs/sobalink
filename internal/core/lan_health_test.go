package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/identity"
)

func TestLANHostTerminationStopsReadinessWithoutRestart(t *testing.T) {
	c := openLANTestCore(t)
	b, engine := testLANBackend()
	relay := &trackedRelayClose{done: make(chan struct{})}
	starts := 0
	b.start = func() (hostedLANRelay, error) { starts++; return relay, nil }
	c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
	mustCommand(t, c, "network.configure", map[string]any{"mode": "lan", "lan": LANSelection{Kind: "host", Address: "127.0.0.1:48443"}})
	before, err := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if status := c.lanStatus(); status["relayReady"] != true || status["readinessKnown"] != true {
		t.Fatal("host never became ready", status)
	}
	close(relay.done) // The owned serving loop ended, while Core is still active.
	for range 3 {
		state, err := b.State(context.Background())
		if err != nil || state.Snapshot.Running || state.Backend != "unavailable" {
			t.Fatal("terminated host remained ready", state, err)
		}
		status := c.lanStatus()
		if status["relayReady"] != false || status["listenerReady"] != false || status["readinessKnown"] != true {
			t.Fatal("termination was not reflected in status", status)
		}
		if err := b.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 || c.ctx.Err() != nil || engine.closed {
		t.Fatal("read-only liveness restarted the relay or stopped Core")
	}
	after, err := os.ReadFile(filepath.Join(c.dir, "lan.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("liveness changed saved identity or configuration")
	}
	if err := c.Close(); err != nil || !relay.closed || !engine.closed {
		t.Fatal("whole-Core stop did not retain ownership", err)
	}
}

type lanStatusFixture struct {
	NetworkBackend
	state identity.State
	err   error
}

func (n lanStatusFixture) State(context.Context) (identity.State, error) { return n.state, n.err }

func TestLANMixedStatusUsesOnlyExactWorkerEvidence(t *testing.T) {
	for _, outcome := range []string{"ready", "unavailable", "unknown", "not-selected"} {
		t.Run(outcome, func(t *testing.T) {
			c := openLANTestCore(t)
			if err := c.configureLAN(&LANSelection{Kind: "host", Address: "127.0.0.1:48443"}); err != nil {
				t.Fatal(err)
			}
			lan, _ := testLANBackend()
			other, _ := testLANBackend()
			if err := other.Start(); err != nil {
				t.Fatal(err)
			}
			worker := lanStatusFixture{NetworkBackend: lan}
			worker.state.Backend = outcome
			worker.state.Snapshot.Running = outcome == "ready"
			if outcome == "unknown" {
				worker.err = errors.New("synthetic observation failure")
			}
			mixed := &mixedBackend{ctx: c.ctx, order: []string{"tailnet"}, nodes: map[string]NetworkBackend{"tailnet": other}}
			if outcome != "not-selected" {
				mixed.nodes["lan"] = worker
				mixed.order = append(mixed.order, "lan")
			}
			c.mu.Lock()
			c.node = mixed
			c.profile.Settings.Network = "mixed"
			c.mu.Unlock()
			status := c.lanStatus()
			if status["readinessKnown"] != (outcome != "unknown") || status["relayReady"] != (outcome == "ready") || status["listenerReady"] != (outcome == "ready") || status["path"] != "unknown" {
				t.Fatal("LAN health followed aggregate availability or invented evidence", status)
			}
			if outcome == "unavailable" {
				state, err := mixed.State(context.Background())
				if err != nil || !state.Snapshot.Running {
					t.Fatal("fixture did not retain unrelated ready backend", err)
				}
			}
		})
	}
}

func TestLANRelayStartupErrorsAreClassifiedAndRedacted(t *testing.T) {
	fixtures := fixtureListenerErrors()
	fixtures = append(fixtures, struct {
		err  error
		code string
	}{fixtureListenerConflict(), "listener_conflict"})
	for _, fixture := range fixtures {
		err := &net.OpError{Op: "listen", Net: "tcp", Addr: &net.TCPAddr{Port: 48443}, Err: fmt.Errorf("synthetic-private-detail: %w", fixture.err)}
		got := classifyLANRelayStartError(err)
		if networkErrorCode(got) != "lan_"+fixture.code || strings.Contains(got.Error(), "synthetic-private-detail") {
			t.Fatal("lost safe bind classification", got)
		}
	}
	for _, cause := range []error{
		errors.New("synthetic-private-detail: address already in use"),
		fixtureListenerConflict(), // Errno without evidence that a listen failed.
		&net.OpError{Op: "dial", Err: fixtureListenerConflict()},
		&net.OpError{Op: "listen", Err: errors.New("synthetic-private-detail")},
		&net.OpError{Op: "listen", Err: context.Canceled},
		&net.OpError{Op: "listen", Err: context.DeadlineExceeded},
		&net.OpError{Op: "listen", Err: errors.Join(fixtureListenerConflict(), errors.New("unknown"))},
		errors.Join(&net.OpError{Op: "listen", Err: fixtureListenerConflict()}, errors.New("unknown")),
	} {
		if got := classifyLANRelayStartError(cause); networkErrorCode(got) != "lan_start_failed" || strings.Contains(got.Error(), "synthetic-private-detail") {
			t.Fatal("unknown cause acquired bind diagnosis", got)
		}
	}
}

func TestLANStartPreservesSafeErrorsAndSavedIdentity(t *testing.T) {
	for _, code := range []string{"lan_listener_conflict", "lan_listener_address_unavailable", "lan_listener_permission_denied", "lan_listener_capacity", "lan_start_failed", ""} {
		t.Run(code, func(t *testing.T) {
			c := openLANTestCore(t)
			selection := &LANSelection{Kind: "host", Address: "127.0.0.1:48443"}
			if err := c.configureLAN(selection); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(c.dir, "lan.json"))
			if err != nil {
				t.Fatal(err)
			}
			b, engine := testLANBackend()
			b.start = func() (hostedLANRelay, error) {
				if code == "" {
					return nil, errors.New("synthetic-private-detail")
				}
				return nil, lanRelayStartError(code)
			}
			c.lanFactory = func(*lanStore) (lanNetworkBackend, error) { return b, nil }
			_, err = command(c, randomID(), "network.configure", map[string]any{"mode": "lan", "lan": selection})
			if err == nil || networkErrorCode(err) != code || strings.Contains(err.Error(), "synthetic-private-detail") {
				t.Fatal("startup lost safe error or leaked raw cause", err)
			}
			if !engine.closed || c.nodeCopy() != nil || c.lanStatus()["relayReady"] != false {
				t.Fatal("failed startup retained runtime readiness")
			}
			after, err := os.ReadFile(filepath.Join(c.dir, "lan.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed start changed saved keys, pin or endpoint")
			}
		})
	}
}
