package core

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"tailscale.com/types/key"
)

type coreRouteReductionFixture struct {
	core       *Core
	issuer     lanlink.Identity
	remote     lanlink.RemotePeer
	candidates []lanlink.RouteCandidate
	liveNode   *lanlink.Node
}

// Both paths use real authenticated offers and the private store. The active
// backend exercises Core's retained Node path without starting network sockets;
// the offline path creates and discards its Node on every command.
func newCoreRouteReductionFixture(t *testing.T, active bool) coreRouteReductionFixture {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "TS_PROXY" || strings.HasPrefix(upper, "TS_") && upper != "TS_NO_LOGS_NO_SUPPORT" {
			t.Setenv(name, "")
		}
	}
	c := openLANTestCore(t)
	if err := c.configureLAN(testLANSelection()); err != nil {
		t.Fatal(err)
	}
	store := c.lanStoreCopy()
	state := store.copy()
	relay, err := storedLANRelay(state)
	if err != nil {
		t.Fatal(err)
	}
	local, closeLocal, err := c.routeControl()
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocal()
	issuer := lanlink.GenerateIdentity()
	other, err := lanlink.NewNode(lanlink.NodeConfig{Identity: issuer, Relay: relay, Trust: lanlink.NewBook()})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	localRole, otherRole := key.NewNode(), key.NewNode()
	role := func(k key.NodePrivate) string { return strings.TrimPrefix(k.Public().String(), "nodekey:") }
	paired := lanlink.RemotePeer{Peer: lanlink.Peer{Key: issuer.PublicKey(), Name: "Route peer"}, Address: other.Address(), ClientPrivate: localRole, IncomingClientKey: role(otherRole)}
	remote := lanlink.RemotePeer{Peer: lanlink.Peer{Key: state.Identity.PublicKey(), Name: "Route device"}, Address: local.Address(), ClientPrivate: otherRole, IncomingClientKey: role(localRole)}
	state.Trust.Peers = []lanlink.Peer{paired.Peer}
	state.Remotes = []lanlink.RemotePeer{paired}
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	f := coreRouteReductionFixture{core: c, issuer: issuer, remote: remote, candidates: []lanlink.RouteCandidate{{Relay: relay, Scope: "local"}, extraRouteFixture()}}
	if active {
		backend, err := c.newLANBackend(store)
		if err != nil {
			t.Fatal(err)
		}
		f.liveNode = backend.(*lanBackend).routeNode
		c.mu.Lock()
		c.node = backend
		c.mu.Unlock()
	}
	return f
}

func (f coreRouteReductionFixture) offer(t *testing.T, sequence uint64, candidates []lanlink.RouteCandidate, lifetime string, expiry time.Time) (string, string) {
	t.Helper()
	frame, err := lanlink.SealRouteUpdateWithLifetime(f.issuer, f.remote, sequence, candidates, lifetime, time.Now().UTC().Add(-time.Second), expiry)
	if err != nil {
		t.Fatal(err)
	}
	review := mustCommand(t, f.core, "lan.routes.inspect", map[string]any{"peerId": f.issuer.PublicKey(), "update": string(frame)}).(map[string]any)
	return string(frame), review["digest"].(string)
}

func (f coreRouteReductionFixture) payload(frame, digest string, ids []string, lifetime string, expiry time.Time) map[string]any {
	return map[string]any{"peerId": f.issuer.PublicKey(), "update": frame, "digest": digest, "candidateIds": ids, "lifetime": lifetime, "expires": expiry}
}

func TestCoreRouteReductionSaveFailureRequiresRecovery(t *testing.T) {
	for _, active := range []bool{false, true} {
		mode := map[bool]string{false: "offline", true: "active-backend"}[active]
		for _, uncertain := range []bool{false, true} {
			outcome := map[bool]string{false: "not-published", true: "published-uncertain"}[uncertain]
			for _, operation := range []string{"apply-replace", "apply-narrow", "apply-offer-shorten", "approve-narrow", "approve-shorten", "approve-finite-shorten", "approve-empty"} {
				t.Run(mode+"/"+outcome+"/"+operation, func(t *testing.T) {
					f := newCoreRouteReductionFixture(t, active)
					c := f.core
					ids := []string{f.candidates[0].ID(), f.candidates[1].ID()}
					lifetime, expiry := lanlink.RouteLifetimeUntilRevoked, time.Time{}
					if operation == "approve-finite-shorten" {
						lifetime, expiry = lanlink.RouteLifetimeFinite, time.Now().UTC().Add(time.Hour)
					}
					frame, digest := f.offer(t, 1, f.candidates, lanlink.RouteLifetimeUntilRevoked, time.Time{})
					mustCommand(t, c, "lan.routes.apply", f.payload(frame, digest, ids, lifetime, expiry))
					commandName := "lan.routes.approve"
					switch operation {
					case "apply-replace":
						commandName = "lan.routes.apply"
						frame, digest = f.offer(t, 2, f.candidates[1:], lifetime, expiry)
						ids = ids[1:]
					case "apply-narrow":
						commandName = "lan.routes.apply"
						frame, digest = f.offer(t, 2, f.candidates, lifetime, expiry)
						ids = ids[1:]
					case "apply-offer-shorten":
						commandName = "lan.routes.apply"
						lifetime, expiry = lanlink.RouteLifetimeFinite, time.Now().UTC().Add(time.Minute)
						frame, digest = f.offer(t, 2, f.candidates, lifetime, expiry)
					case "approve-narrow":
						ids = ids[1:]
					case "approve-shorten", "approve-finite-shorten":
						lifetime, expiry = lanlink.RouteLifetimeFinite, time.Now().UTC().Add(time.Minute)
					case "approve-empty":
						ids = nil
					}
					payload := f.payload(frame, digest, ids, lifetime, expiry)
					store := c.lanStoreCopy()
					before := store.copy()
					beforeBytes, err := os.ReadFile(store.path)
					if err != nil {
						t.Fatal(err)
					}
					writes := 0
					store.write = func(path string, data []byte) error {
						writes++
						if uncertain {
							if err := config.AtomicWrite(path, data); err != nil {
								return err
							}
							return config.ErrAtomicCommitted
						}
						return &os.PathError{Op: "write", Path: path, Err: os.ErrPermission}
					}
					requestID := randomID()
					_, err = command(c, requestID, commandName, payload)
					if !errors.Is(err, config.ErrAtomicRecovery) || networkErrorCode(err) != "lan_routes_recovery" || !store.routesNeedRecovery() {
						t.Fatalf("failed reduction did not latch Core recovery: %v", err)
					}
					if errors.Is(err, config.ErrAtomicCommitted) != uncertain || writes != 1 {
						t.Fatalf("save outcome changed: writes=%d error=%v", writes, err)
					}
					if !strings.Contains(err.Error(), "old permissions on disk") || !strings.Contains(err.Error(), "before restarting") {
						t.Fatal("recovery guidance hid the saved-authority restart boundary", err)
					}
					var privateCause *os.PathError
					if errors.As(err, &privateCause) || strings.Contains(err.Error(), store.path) || errors.Is(err, os.ErrPermission) {
						t.Fatal("private save details leaked", err)
					}
					if _, replayErr := command(c, requestID, commandName, payload); replayErr != err || writes != 1 {
						t.Fatal("request replay retried a failed reduction", replayErr)
					}
					if f.liveNode != nil {
						snapshot, err := f.liveNode.RouteSnapshot(f.issuer.PublicKey())
						if err != nil || !snapshot.RecoveryRequired || len(snapshot.Permitted) != 0 {
							t.Fatal("active Node lost its recovery gate", err)
						}
					}
					for _, name := range []string{"lan.routes.list", "lan.routes.review", commandName} {
						if _, err := command(c, randomID(), name, payload); !errors.Is(err, config.ErrAtomicRecovery) {
							t.Fatal("later route command reloaded old permission", name, err)
						}
					}
					// Discarding the active Node must not clear the Core-owned latch.
					if active {
						backend := c.nodeCopy()
						c.mu.Lock()
						c.node = nil
						c.mu.Unlock()
						if err := backend.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if _, closeNode, err := c.routeControl(); !errors.Is(err, config.ErrAtomicRecovery) {
						if closeNode != nil {
							closeNode()
						}
						t.Fatal("disposable route Node resurrected saved authority", err)
					}
					if backend, err := c.newLANBackend(store); !errors.Is(err, config.ErrAtomicRecovery) {
						if backend != nil {
							_ = backend.Close()
						}
						t.Fatal("same-Core activation bypassed recovery", err)
					}
					if writes != 1 {
						t.Fatal("recovery gate allowed another private write", writes)
					}
					afterBytes, err := os.ReadFile(store.path)
					if err != nil {
						t.Fatal(err)
					}
					if uncertain == bytes.Equal(beforeBytes, afterBytes) {
						t.Fatal("private file did not match the injected publication outcome")
					}
					if !uncertain && !reflect.DeepEqual(before, store.copy()) {
						t.Fatal("unpublished reduction claimed a new durable snapshot")
					}
					// This latch is process-local. A read of durable storage cannot
					// reconstruct a failed, unpublished reduction. Preserve that
					// boundary rather than claiming crash-safe revocation persistence.
					reloaded, err := readLANStore(store.path)
					if err != nil || reloaded.routesNeedRecovery() || !jsonEqual(reloaded.copy(), store.copy()) {
						t.Fatal("reloaded storage disagreed with its published snapshot", err)
					}
				})
			}
		}
	}
}

func TestCoreRouteGrantOnlySaveFailureKeepsExistingSemantics(t *testing.T) {
	for _, active := range []bool{false, true} {
		mode := map[bool]string{false: "offline", true: "active-backend"}[active]
		for _, uncertain := range []bool{false, true} {
			outcome := map[bool]string{false: "not-published", true: "published-uncertain"}[uncertain]
			for _, commandName := range []string{"lan.routes.apply", "lan.routes.approve"} {
				t.Run(mode+"/"+outcome+"/"+commandName, func(t *testing.T) {
					f := newCoreRouteReductionFixture(t, active)
					frame, digest := f.offer(t, 1, f.candidates, lanlink.RouteLifetimeUntilRevoked, time.Time{})
					mustCommand(t, f.core, "lan.routes.apply", f.payload(frame, digest, []string{f.candidates[0].ID()}, lanlink.RouteLifetimeUntilRevoked, time.Time{}))
					if commandName == "lan.routes.apply" {
						frame, digest = f.offer(t, 2, f.candidates, lanlink.RouteLifetimeUntilRevoked, time.Time{})
					}
					store := f.core.lanStoreCopy()
					before := store.copy()
					writes := 0
					store.write = func(path string, data []byte) error {
						writes++
						if uncertain {
							if err := config.AtomicWrite(path, data); err != nil {
								return err
							}
							return config.ErrAtomicCommitted
						}
						return os.ErrPermission
					}
					payload := f.payload(frame, digest, []string{f.candidates[0].ID(), f.candidates[1].ID()}, lanlink.RouteLifetimeUntilRevoked, time.Time{})
					_, err := command(f.core, randomID(), commandName, payload)
					if err == nil || errors.Is(err, config.ErrAtomicRecovery) || errors.Is(err, config.ErrAtomicCommitted) != uncertain || store.routesNeedRecovery() != uncertain || writes != 1 {
						t.Fatalf("grant-only save semantics changed: writes=%d error=%v", writes, err)
					}
					if uncertain {
						if _, err := command(f.core, randomID(), "lan.routes.review", map[string]any{"peerId": f.issuer.PublicKey()}); !errors.Is(err, config.ErrAtomicRecovery) {
							t.Fatal("uncertain grant allowed later route management", err)
						}
						return
					}
					if !reflect.DeepEqual(before, store.copy()) {
						t.Fatal("unpublished grant changed the saved snapshot")
					}
					snapshot := mustCommand(t, f.core, "lan.routes.list", map[string]any{"peerId": f.issuer.PublicKey()}).(map[string]any)
					if got := snapshot["permittedIds"].([]string); len(got) != 1 || got[0] != f.candidates[0].ID() {
						t.Fatal("failed grant changed the existing approved routes")
					}
					store.write = config.AtomicWrite
					snapshot = mustCommand(t, f.core, commandName, payload).(map[string]any)
					if len(snapshot["permittedIds"].([]string)) != 2 {
						t.Fatal("grant-only failure blocked an explicit retry")
					}
				})
			}
		}
	}
}
