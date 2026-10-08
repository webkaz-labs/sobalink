package directlan

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// All fixtures are synthetic copied data. No Start, socket, application bytes,
// retirement/replacement execution, publication or real profile is involved.
func currentProjectionFixture(t *testing.T) (Config, endpointmeta.Snapshot, Identity, time.Time) {
	t.Helper()
	cfg, remote := managedFixtureConfig()
	pair := cfg.PairContexts[remote.PublicKey()]
	initial, err := endpointmeta.InitialState(pair, cfg.Identity.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	model := endpointmeta.Snapshot{Version: 4, Revision: "1", LocalPeer: endpointmeta.PeerWire{Key: cfg.Identity.PublicKey(), TunnelKey: cfg.Identity.TunnelKey(), Endpoint: cfg.Listen.String()}, LocalScope: pair.HostScope, PreviousLocalEndpoint: cfg.Listen.String(), ObservedAt: now.Format(time.RFC3339Nano), Peers: []endpointmeta.PeerRecord{{Peer: endpointmeta.PeerWire{Key: remote.PublicKey(), TunnelKey: remote.TunnelKey(), Endpoint: pair.JoinerEndpoint}, Revision: "1", PairContext: &pair, ContextConfirmed: true, EndpointState: &initial}}}
	cfg.PairContexts = nil
	return cfg, model, remote, now
}

func currentProjectionCopy(model endpointmeta.Snapshot) endpointmeta.Snapshot {
	data, _ := json.Marshal(model)
	var copy endpointmeta.Snapshot
	_ = json.Unmarshal(data, &copy)
	return copy
}

func currentProjectionBase(base Config, model endpointmeta.Snapshot) Config {
	base = cloneGenerationConfig(base)
	base.Peers = nil
	for _, record := range model.Peers {
		base.Peers = append(base.Peers, Peer{Key: record.Peer.Key, Name: record.Peer.Name, TunnelKey: record.Peer.TunnelKey, Endpoint: netip.MustParseAddrPort(record.Peer.Endpoint)})
	}
	return base
}

func currentProjectionFinish(t *testing.T, model endpointmeta.Snapshot, mutation endpointmeta.Mutation, now time.Time) endpointmeta.Snapshot {
	t.Helper()
	review, err := endpointmeta.PreviewMutation(model, mutation)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := endpointmeta.Fence(model, mutation, review, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	next, err := endpointmeta.FinishPending(fenced, false, now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func currentProjectionSet(t *testing.T, model endpointmeta.Snapshot, remote Identity, sequence, endpoint string, now time.Time, follow bool) endpointmeta.Snapshot {
	t.Helper()
	record := model.Peers[0]
	digest, _ := model.LocalScope.Digest()
	proof, err := endpointmeta.Sign(endpointmeta.UpdateBody{Version: 1, Domain: endpointmeta.UpdateDomain, PairBinding: record.EndpointState.PairBinding, Issuer: remote.PublicKey(), Recipient: model.LocalPeer.Key, IssuerTunnelKey: remote.TunnelKey(), RecipientTunnelKey: model.LocalPeer.TunnelKey, Sequence: sequence, PriorEndpoint: record.Peer.Endpoint, Operation: "set", Endpoint: endpoint, ScopeDigest: digest, Issued: now.UTC().Format(time.RFC3339Nano), Lifetime: "finite", Expires: now.Add(time.Hour).UTC().Format(time.RFC3339Nano)}, mustCurrentProjectionPrivate(t, remote))
	if err != nil {
		t.Fatal(err)
	}
	proofDigest, _ := proof.Digest()
	approval := &endpointmeta.Approval{Kind: "exact", ProofDigest: proofDigest, Endpoint: endpoint, Granted: proof.Update.Issued, Lifetime: "finite", Expires: proof.Update.Expires}
	if follow {
		approval = nil
	}
	mutation, outcome, err := endpointmeta.ProposeReceive(model, remote.PublicKey(), proof, approval, now)
	if err != nil || outcome != "candidate" {
		t.Fatal(outcome, err)
	}
	return currentProjectionFinish(t, model, mutation, now)
}

func mustCurrentProjectionPrivate(t *testing.T, identity Identity) ed25519.PrivateKey {
	t.Helper()
	key, err := identity.private()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCurrentEndpointProjectionInitialSignedAndImmutableContext(t *testing.T) {
	for _, version := range []int{3, 4} {
		base, model, remote, now := currentProjectionFixture(t)
		model.Version = version
		original := *model.Peers[0].PairContext
		initial, err := ProjectCurrentEndpointConfig(base, model, now)
		if err != nil || len(initial.Peers) != 1 || initial.Peers[0] != base.Peers[0] {
			t.Fatal("initial projection", err)
		}
		model = currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
		before := currentProjectionCopy(model)
		current, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now)
		if err != nil || len(current.Peers) != 1 || current.Peers[0].Endpoint.String() != "127.0.0.2:45107" {
			t.Fatal("signed projection", err)
		}
		if !reflect.DeepEqual(current.PairContexts[remote.PublicKey()], original) || !reflect.DeepEqual(model, before) {
			t.Fatal("immutable pair or evidence changed")
		}
		authority := current.currentEndpoints.peers[remote.PublicKey()]
		if authority.highwater != "7" || authority.authorityRevision != model.Peers[0].EndpointState.AuthorityRevision || authority.proofDigest == "" || !authority.deadline.Equal(now.Add(time.Hour)) {
			t.Fatal("missing exact current authority")
		}
		ordinary := cloneGenerationConfig(current)
		ordinary.currentEndpoints = nil
		if ordinary.Validate() == nil {
			t.Fatal("ordinary fixed config relaxed immutable endpoint checks")
		}
		// This constructor returns before allocating even an inert Node.
		if node, err := NewNode(current); node != nil || !errors.Is(err, ErrUnavailable) {
			t.Fatal("current projection activated", err)
		}
	}
}

func TestCurrentEndpointProjectionFollowDeadlineAndNoFallback(t *testing.T) {
	base, model, remote, now := currentProjectionFixture(t)
	scope, _ := model.LocalScope.Digest()
	follow := endpointmeta.FollowApproval{ScopeDigest: scope, Revision: "1", Granted: now.Format(time.RFC3339Nano), Lifetime: "finite", Expires: now.Add(30 * time.Minute).Format(time.RFC3339Nano), Active: true}
	mutation, err := endpointmeta.ProposeFollow(model, model.Peers[0].EndpointState.PairBinding, follow, now)
	if err != nil {
		t.Fatal(err)
	}
	model = currentProjectionFinish(t, model, mutation, now)
	model = currentProjectionSet(t, model, remote, "2", "127.0.0.2:45107", now, true)
	current, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now)
	if err != nil || !current.currentEndpoints.peers[remote.PublicKey()].deadline.Equal(now.Add(30*time.Minute)) {
		t.Fatal("follow bound", err)
	}
	expired, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now.Add(30*time.Minute))
	if err != nil || len(expired.Peers) != 0 || len(expired.PairContexts) != 0 || !expired.deniedKey(remote.PublicKey()) || len(expired.DeniedPeerKeys) != 0 {
		t.Fatal("expired authority fell back or became terminal", err)
	}
}

func TestCurrentEndpointProjectionInactiveRestoresOnlyFreshApprovedRecord(t *testing.T) {
	for _, reduction := range []string{"revoke", "expire", "withdraw"} {
		t.Run(reduction, func(t *testing.T) {
			base, model, remote, now := currentProjectionFixture(t)
			model = currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
			if reduction == "expire" {
				now = now.Add(time.Hour)
			}
			if reduction == "withdraw" {
				proof := *model.Peers[0].EndpointState.ReceivedProof
				proof.Update.Sequence, proof.Update.Operation, proof.Update.PriorEndpoint, proof.Update.Endpoint = "8", "withdraw", model.Peers[0].Peer.Endpoint, ""
				proof, err := endpointmeta.Sign(proof.Update, mustCurrentProjectionPrivate(t, remote))
				if err != nil {
					t.Fatal(err)
				}
				mutation, outcome, err := endpointmeta.ProposeReceive(model, remote.PublicKey(), proof, nil, now)
				if err != nil || outcome != "candidate" {
					t.Fatal(outcome, err)
				}
				model = currentProjectionFinish(t, model, mutation, now)
			} else {
				result, err := endpointmeta.ProposeReduction(model, model.Peers[0].EndpointState.PairBinding, reduction, now)
				if err != nil || !result.Changed {
					t.Fatal("reduction", err)
				}
				model = currentProjectionFinish(t, model, result.Mutation, now)
			}
			inactive, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now)
			if err != nil || len(inactive.Peers) != 0 || len(inactive.PairContexts) != 0 || !reflect.DeepEqual(inactive.InactiveEndpointPeerKeys(), []string{remote.PublicKey()}) || len(inactive.DeniedPeerKeys) != 0 {
				t.Fatal("inactive route leaked", err)
			}
			originalPair := *model.Peers[0].PairContext
			restored := currentProjectionSet(t, model, remote, "9", "127.0.0.3:45109", now, false)
			fresh, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, restored), restored, now)
			if err != nil || len(fresh.Peers) != 1 || fresh.Peers[0].Endpoint.String() != "127.0.0.3:45109" || len(fresh.InactiveEndpointPeerKeys()) != 0 || !reflect.DeepEqual(fresh.PairContexts[remote.PublicKey()], originalPair) {
				t.Fatal("retained inactive peer could not restore", err)
			}
			if len(inactive.Peers) != 0 || !inactive.deniedKey(remote.PublicKey()) {
				t.Fatal("fresh projection changed old inactive classification")
			}
		})
	}
}

func TestCurrentEndpointProjectionTerminalLegacyAndCopies(t *testing.T) {
	base, model, remote, now := currentProjectionFixture(t)
	model = currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
	terminal, changed, err := endpointmeta.RevokeManagedPairV4(model, model.Peers[0], now, 1<<20)
	if err != nil || !changed {
		t.Fatal(err)
	}
	legacy := controlLifecycleIdentity(113)
	terminal.Peers = append(terminal.Peers, endpointmeta.PeerRecord{Peer: endpointmeta.PeerWire{Key: legacy.PublicKey(), TunnelKey: legacy.TunnelKey(), Endpoint: "127.0.0.4:45104"}, Revision: "1"})
	before := currentProjectionCopy(terminal)
	cfg, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, terminal), terminal, now)
	if err != nil || len(cfg.Peers) != 1 || cfg.Peers[0].Key != legacy.PublicKey() || len(cfg.PairContexts) != 0 || !reflect.DeepEqual(cfg.DeniedPeerKeys, []string{remote.PublicKey()}) || len(cfg.InactiveEndpointPeerKeys()) != 0 || !reflect.DeepEqual(terminal, before) {
		t.Fatal("terminal mixed projection", err)
	}
	// Genuine legacy metadata retains the ordinary config contract.
	legacyOnly := currentProjectionCopy(terminal)
	legacyOnly.Peers = legacyOnly.Peers[1:]
	plain, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, legacyOnly), legacyOnly, now)
	if err != nil || plain.currentEndpoints != nil || len(plain.Peers) != 1 {
		t.Fatal("legacy behavior changed", err)
	}
	current, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, model), model, now)
	if err != nil {
		t.Fatal(err)
	}
	owned := cloneGenerationConfig(current)
	current.PairContexts[remote.PublicKey()].HostScope.Prefixes[0] = "10.0.0.0/8"
	current.Peers[0].Endpoint = base.Peers[0].Endpoint
	delete(current.currentEndpoints.peers, remote.PublicKey())
	model.Peers[0].PairContext.JoinerScope.Prefixes[0] = "10.0.0.0/8"
	if owned.Validate() != nil || owned.currentEndpoints.peers[remote.PublicKey()].highwater != "7" {
		t.Fatal("generation copy aliases caller data")
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Peers = nil },
		func(c *Config) { c.PairContexts = nil },
		func(c *Config) { c.Listen = netip.MustParseAddrPort("127.0.0.5:45105") },
		func(c *Config) { c.Peers[0].Endpoint = base.Peers[0].Endpoint },
		func(c *Config) { c.DeniedPeerKeys = []string{legacy.PublicKey()} },
	} {
		bad := cloneGenerationConfig(owned)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("projection tampering accepted")
		}
	}
}

func TestCurrentEndpointProjectionRejectsWholeInvalidModel(t *testing.T) {
	base, initial, remote, now := currentProjectionFixture(t)
	good := currentProjectionSet(t, initial, remote, "7", "127.0.0.2:45107", now, false)
	for name, change := range map[string]func(*endpointmeta.Snapshot){
		"unconfirmed":      func(m *endpointmeta.Snapshot) { m.Peers[0].ContextConfirmed = false },
		"missing-approval": func(m *endpointmeta.Snapshot) { m.Peers[0].EndpointState.Approval = nil },
		"stale-proof":      func(m *endpointmeta.Snapshot) { m.Peers[0].EndpointState.ReceivedHighwater = "8" },
		"wrong-binding":    func(m *endpointmeta.Snapshot) { m.Peers[0].EndpointState.PairBinding = initial.LocalPeer.Key },
		"wrong-signer": func(m *endpointmeta.Snapshot) {
			m.Peers[0].EndpointState.ReceivedProof.Update.Issuer = initial.LocalPeer.Key
		},
		"wrong-tunnel": func(m *endpointmeta.Snapshot) { m.Peers[0].Peer.TunnelKey = initial.LocalPeer.TunnelKey },
		"wrong-scope":  func(m *endpointmeta.Snapshot) { m.LocalScope.Prefixes = []string{"127.0.0.0/9"} },
		"conflicting-proof": func(m *endpointmeta.Snapshot) {
			m.Peers[0].EndpointState.ReceivedProof.Update.Endpoint = "127.0.0.5:45105"
		},
		"wrong-approval": func(m *endpointmeta.Snapshot) { m.Peers[0].EndpointState.Approval.ProofDigest = initial.LocalPeer.Key },
		"authority-zero": func(m *endpointmeta.Snapshot) { m.Peers[0].EndpointState.AuthorityRevision = "0" },
		"future":         func(m *endpointmeta.Snapshot) { m.ObservedAt = now.Add(time.Hour).Format(time.RFC3339Nano) },
		"pending":        func(m *endpointmeta.Snapshot) { m.PendingChange = &endpointmeta.PendingChange{} },
		"local-moved":    func(m *endpointmeta.Snapshot) { m.LocalPeer.Endpoint = "127.0.0.8:45108" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := currentProjectionCopy(good)
			change(&bad)
			cfg, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, bad), bad, now)
			if err == nil || len(cfg.Peers) != 0 || len(cfg.PairContexts) != 0 || cfg.currentEndpoints != nil {
				t.Fatal("invalid model leaked partial authority", err)
			}
		})
	}
}

func TestCurrentEndpointProjectionValidFenceAndUpgradeRemainClosed(t *testing.T) {
	base, model, remote, now := currentProjectionFixture(t)
	binding := model.Peers[0].EndpointState.PairBinding
	scope, _ := model.LocalScope.Digest()
	mutation, err := endpointmeta.ProposeFollow(model, binding, endpointmeta.FollowApproval{ScopeDigest: scope, Revision: "1", Granted: now.Format(time.RFC3339Nano), Lifetime: "until-revoked", Active: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	review, err := endpointmeta.PreviewMutation(model, mutation)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := endpointmeta.Fence(model, mutation, review, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), now, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := ProjectCurrentEndpointConfig(base, fenced, now); !errors.Is(err, ErrRecovery) || len(cfg.Peers) != 0 {
		t.Fatal("valid fence projected", err)
	}
	upgrading := currentProjectionCopy(model)
	record := &upgrading.Peers[0]
	record.UpgradePending = &endpointmeta.UpgradePending{ReviewedPeer: record.Peer, PeerRevision: record.Revision, OwnNonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), PrepareDeadline: now.Add(time.Hour).Format(time.RFC3339Nano)}
	if err := upgrading.Validate(); err != nil {
		t.Fatal("bad test fixture", err)
	}
	if cfg, err := ProjectCurrentEndpointConfig(base, upgrading, now); err == nil || len(cfg.Peers) != 0 {
		t.Fatal("pending upgrade projected", err)
	}
	current := currentProjectionSet(t, model, remote, "7", "127.0.0.2:45107", now, false)
	cfg, err := ProjectCurrentEndpointConfig(currentProjectionBase(base, current), current, now)
	if err != nil {
		t.Fatal(err)
	}
	node := &Node{cfg: cfg}
	if _, err := node.candidateConfigLocked(nil, TransportEndpoints{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("replacement guard opened", err)
	}
}
