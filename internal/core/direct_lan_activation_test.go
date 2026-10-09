package core

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// Pure/store-mocked acceptance only. No constructor, Start, socket, preparer,
// or transport join is executed. Recorded Close outcomes are injected.
func activationFixture(t *testing.T, kind string) (*Core, *directLANStore) {
	t.Helper()
	state, now := pairRecordFixture(t)
	switch kind {
	case "terminal":
		state = pairRecordV4(t, state, now, true)
	case "mixed":
		state, now = activeV4MixedFixture(t)
	case "legacy":
		state = pairRecordV4(t, state, now, false)
		r := &state.Metadata.Peers[0]
		r.PairContext = nil
		r.EndpointState = nil
		r.ContextConfirmed = false
	}
	// The source reducer fixtures use a fixed future clock. This coordinator
	// observes the real clock, so rebase only synthetic observation/denial times
	// before materializing its file; initial contexts contain no timed proofs.
	observed := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	state.Metadata.ObservedAt = observed
	for i := range state.Metadata.Peers {
		if marker := state.Metadata.Peers[i].PairRevocation; marker != nil {
			marker.RevokedAt = observed
		}
	}
	c, s := pairRecordStoreFixture(t, state)
	c.profile.Settings.Network = "direct-lan"
	c.profile.Settings.Hostname = "synthetic-host"
	s.write = func(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
	return c, s
}
func TestActivationWholeStateReceiptWithoutTarget(t *testing.T) {
	for _, kind := range []string{"active", "terminal", "mixed", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			c, s := activationFixture(t, kind)
			before := cloneDirectLANState(s.state)
			if s.contextPublicationCurrentLocked(c.lanStartNonce) {
				t.Fatal("read minted receipt")
			}
			c.op.Lock()
			a, err := c.prepareDirectLANActivationLocked(context.Background(), s)
			c.op.Unlock()
			if err != nil || a == nil || a.receipt == nil || !s.contextPublicationCurrentLocked(c.lanStartNonce) || !reflect.DeepEqual(before, s.state) || s.contextEpoch != nil {
				t.Fatal("whole-state admission", err)
			}
			revision, receipt := s.reviewRevision, s.contextPublication
			c.op.Lock()
			_, err = c.prepareDirectLANActivationLocked(context.Background(), s)
			c.op.Unlock()
			if err != nil || s.reviewRevision != revision || s.contextPublication != receipt {
				t.Fatal("current receipt republished", err)
			}
			if kind == "terminal" && (len(a.projection.Peers) != 0 || len(a.projection.DeniedPeerKeys) != 1) {
				t.Fatal("all-terminal projection fabricated a peer")
			}
		})
	}
}
func TestActivationExactAdmissionRejectsChangedEvidence(t *testing.T) {
	for _, change := range []string{"path", "file", "state", "revision", "limits", "projection", "recovery", "cancel", "process"} {
		t.Run(change, func(t *testing.T) {
			c, s := activationFixture(t, "mixed")
			a, err := s.captureActivationLocked(c, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "path":
				s.path += ".changed"
			case "file":
				if err := os.WriteFile(s.path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "state":
				s.state.Peers[0].Name = "changed"
			case "revision":
				s.reviewRevision++
			case "limits":
				s.bytes--
			case "projection":
				a.projection.Peers = nil
			case "recovery":
				s.recovery = true
			case "cancel":
				cancel()
			case "process":
				c.lanStartNonce = "changed"
			}
			if c.activationOwnerCurrent(ctx, a) && s.matchActivationLocked(a, time.Now()) == nil {
				t.Fatal("stale admission accepted")
			}
		})
	}
}
func TestActivationJoinsRecordedContextOwnerAndRetainsFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "joined", true: "failed"}[failed], func(t *testing.T) {
			c, s := activationFixture(t, "active")
			f := &localContextFixture{core: c, store: s}
			o := localContextRuntimeOwner(t, f)
			close(o.started)
			failure := errors.New("synthetic recorded close failure")
			o.closeOnce.Do(func() {
				if failed {
					o.closeErr = failure
				}
			})
			old := directlan.NewContextEpoch()
			s.contextEpoch = old
			c.op.Lock()
			a, err := c.prepareDirectLANActivationLocked(context.Background(), s)
			c.op.Unlock()
			if old.Valid() || s.contextEpoch != nil {
				t.Fatal("old response epoch survived")
			}
			if failed {
				if !errors.Is(err, failure) || a != nil || c.contextControl != o {
					t.Fatal("failed owner lost", err)
				}
			} else if err != nil || a == nil || c.contextControl != nil {
				t.Fatal("join rejected", err)
			}
		})
	}
}
func TestActivationFactoryCannotBypassAdmission(t *testing.T) {
	c, s := activationFixture(t, "active")
	if _, err := c.newManagedCompletionBackendLocked(s, nil); err == nil {
		t.Fatal("nil admission constructed")
	}
	if _, err := s.runtimeConfig(); err == nil {
		t.Fatal("generic config bypass opened")
	}
	a, err := s.captureActivationLocked(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a.consumed = true
	if _, err := c.newManagedCompletionBackendLocked(s, a); err == nil {
		t.Fatal("consumed admission constructed")
	}
}
func TestActivationExactMixedRootAndChild(t *testing.T) {
	c, s := activationFixture(t, "active")
	b := &directLANBackend{store: s}
	o := &managedCompletionOwner{core: c, coreDone: c.ctx.Done(), store: s, backend: b, process: c.lanStartNonce}
	b.completion = o
	root := &mixedBackend{nodes: map[string]NetworkBackend{"direct-lan": b}}
	bindManagedActivationRoot(root)
	c.node = root
	if !o.coreCurrent("") {
		t.Fatal("exact mixed ownership rejected")
	}
	root.nodes["direct-lan"] = &directLANBackend{store: s}
	if o.coreCurrent("") {
		t.Fatal("replacement child admitted")
	}
	root.nodes["direct-lan"] = b
	c.node = &mixedBackend{nodes: map[string]NetworkBackend{"direct-lan": b}}
	if o.coreCurrent("") {
		t.Fatal("replacement root admitted")
	}
}
func TestActivationRepublishFinalCancellationAndWriterFailure(t *testing.T) {
	for _, cancelAfter := range []bool{false, true} {
		c, s := activationFixture(t, "terminal")
		a, err := s.captureActivationLocked(c, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.write = func(path string, data []byte) error {
			if !cancelAfter {
				return errors.New("synthetic writer failure")
			}
			err := os.WriteFile(path, data, 0600)
			cancel()
			return err
		}
		err = s.republishActivationLocked(a, &contextSaveLiveness{ctx: ctx, core: c.ctx}, time.Now())
		cancel()
		if err == nil {
			t.Fatal("failed publication admitted")
		}
		if !cancelAfter && s.contextPublication != nil {
			t.Fatal("failed write minted receipt")
		}
	}
}
func TestActivationSameProcessReconnectRequiresJoinedExactOwner(t *testing.T) {
	c, s := activationFixture(t, "terminal")
	c.attemptedNetwork = "direct-lan"
	c.attemptedHostname = "synthetic-host"
	a, err := s.captureActivationLocked(c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.activationOwnerCurrent(context.Background(), a) {
		t.Fatal("arbitrary second engine")
	}
	removal := &managedRemovalOwner{core: c, store: s, process: c.lanStartNonce, joined: true, attemptedNetwork: c.attemptedNetwork, attemptedHostname: c.attemptedHostname}
	c.managedRemoval = removal
	a.removal = removal
	if !c.activationOwnerCurrent(context.Background(), a) {
		t.Fatal("joined exact replacement denied")
	}
	c.attemptedHostname = "changed"
	if c.activationOwnerCurrent(context.Background(), a) {
		t.Fatal("changed identity admitted")
	}
}
func TestActivationProductionCallersUseCoordinator(t *testing.T) {
	for _, path := range []string{"runtime.go", "mixed_state.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "newAdmittedDirectLANBackendLocked") || strings.Contains(string(raw), "c.newDirectLANBackend(store)") {
			t.Fatal("ordinary caller bypass", path)
		}
	}
	// Keep generic store and constructor guards in place even after integration.
}

type activationCloseMock struct {
	NetworkBackend
	startErr error
	err      error
	calls    int
}

func (b *activationCloseMock) Start() error { return b.startErr }
func (b *activationCloseMock) Close() error { b.calls++; return b.err }
func TestActivationFailedCandidateRetention(t *testing.T) {
	for _, failed := range []bool{false, true} {
		b := &activationCloseMock{}
		failure := errors.New("synthetic candidate close failure")
		if failed {
			b.err = failure
		}
		c := &Core{node: b}
		c.networkReady.Store(true)
		cause := errors.New("synthetic final readiness failure")
		err := c.closeFailedNetworkCandidateLocked(b, cause)
		if !errors.Is(err, cause) || b.calls != 1 || c.networkReady.Load() {
			t.Fatal("incorrect failure cleanup")
		}
		if failed && (c.node != b || !errors.Is(err, failure)) {
			t.Fatal("failed candidate lost")
		}
		if !failed && c.node != nil {
			t.Fatal("joined candidate retained")
		}
	}
}

func TestActivationFactoryErrorDoesNotBoxNilDirectOrMixedCandidate(t *testing.T) {
	failure := errors.New("synthetic construction failure")
	for _, mode := range []string{"direct-lan", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			candidate, err := ordinaryDirectLANCandidate(nil, failure)
			if candidate != nil || !errors.Is(err, failure) {
				t.Fatal("nil concrete candidate was boxed")
			}
			c := &Core{}
			// The production retention/child-insertion guards must observe true nil.
			if mode == "direct-lan" {
				if candidate != nil {
					c.node = candidate
				}
			} else {
				root := &mixedBackend{nodes: map[string]NetworkBackend{}}
				if candidate != nil {
					root.nodes["direct-lan"] = candidate
				}
				if err := root.Close(); err != nil {
					t.Fatal(err)
				}
				if len(root.nodes) != 0 {
					t.Fatal("phantom mixed child")
				}
			}
			if c.node != nil {
				t.Fatal("phantom Core owner")
			}
		})
	}
	retained := &directLANBackend{closeErr: failure}
	candidate, err := ordinaryDirectLANCandidate(retained, failure)
	if candidate != retained || !errors.Is(err, failure) {
		t.Fatal("genuine retained candidate discarded")
	}
}

func TestActivationFailureTextHidesBackendCausesAndRetainsDiagnostics(t *testing.T) {
	startFailure := errors.New("synthetic-private-start-path")
	closeFailure := errors.New("synthetic-private-close-path")
	for _, mode := range []string{"tailnet", "lan", "direct-lan", "mixed"} {
		b := &activationCloseMock{err: closeFailure, startErr: startFailure}
		c := &Core{node: b}
		c.profile.Settings.Network = mode
		err := c.closeFailedNetworkCandidateLocked(b, b.Start())
		if err == nil || strings.Contains(err.Error(), "synthetic-private") || !errors.Is(err, startFailure) || !errors.Is(err, closeFailure) || c.node != b {
			t.Fatal("unsafe or incomplete startup error", mode, err)
		}
	}
	safe := &lanCommandError{"synthetic-safe-code", "Safe product guidance"}
	c := &Core{}
	err := c.safeNetworkStartupError("lan", errors.Join(safe, startFailure))
	if err.Error() != safe.Error() || networkErrorCode(err) != safe.code || !errors.Is(err, startFailure) {
		t.Fatal("safe LAN guidance or cause lost")
	}
}
