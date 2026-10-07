package endpointmeta

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func asymmetricExportFixture(t *testing.T) (Snapshot, UpdateBody) {
	t.Helper()
	s, _, _ := senderModel(t)
	p := *s.Peers[0].PairContext
	p.JoinerScope = Scope{Family: "ipv4", Prefixes: []string{"127.0.0.0/24"}}
	state, err := InitialState(p, s.LocalPeer.Key)
	if err != nil {
		t.Fatal(err)
	}
	s.Peers[0].PairContext, s.Peers[0].EndpointState = &p, &state
	u, err := PrepareExport(s, s.Peers[0].Peer.Key, ExportOptions{"set", "finite", testNow().Add(time.Hour).Format(time.RFC3339Nano)}, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func TestPrepareExportDerivesRecipientScopeAndAuthority(t *testing.T) {
	s, u := asymmetricExportFixture(t)
	_, _, key := senderModel(t)
	wantScope, _ := s.Peers[0].PairContext.JoinerScope.Digest()
	localScope, _ := s.LocalScope.Digest()
	if wantScope == localScope || u.ScopeDigest != wantScope || u.Issuer != s.LocalPeer.Key ||
		u.Recipient != s.Peers[0].Peer.Key || u.IssuerTunnelKey != s.LocalPeer.TunnelKey ||
		u.RecipientTunnelKey != s.Peers[0].Peer.TunnelKey || u.Sequence != "1" ||
		u.Endpoint != s.LocalPeer.Endpoint || u.PriorEndpoint != s.PreviousLocalEndpoint ||
		u.Issued != testNow().Format(time.RFC3339Nano) {
		t.Fatal("outgoing authority was not derived from the exact snapshot")
	}
	e, err := Sign(u, key)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ProposeIssued(s, u.Recipient, e, testNow(), modelBudget)
	if err != nil {
		t.Fatal(err)
	}
	before, after := *s.Peers[0].EndpointState, *next.Peers[0].EndpointState
	after.IssuedVersion, after.IssuedHighwater, after.IssuedProof = before.IssuedVersion, before.IssuedHighwater, before.IssuedProof
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(s.Peers[0].PairContext, next.Peers[0].PairContext) ||
		next.Peers[0].Peer != s.Peers[0].Peer || !next.Peers[0].ContextConfirmed {
		t.Fatal("issuing changed received authority, context or peer identity")
	}
	w, err := PrepareExport(next, u.Recipient, ExportOptions{"withdraw", "until-revoked", ""}, testNow(), modelBudget)
	if err != nil || w.Sequence != "2" || w.Endpoint != "" || w.PriorEndpoint != s.LocalPeer.Endpoint {
		t.Fatal("withdrawal inherited an endpoint or sequence", err)
	}
}

func TestProposeIssuedRejectsSignedBodySubstitution(t *testing.T) {
	s, prepared := asymmetricExportFixture(t)
	_, _, key := senderModel(t)
	localScope, _ := s.LocalScope.Digest()
	otherTunnel, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	otherTunnelKey := hex.EncodeToString(otherTunnel.PublicKey().Bytes())
	changes := map[string]func(*UpdateBody){
		"sequence":         func(u *UpdateBody) { u.Sequence = "2" },
		"scope":            func(u *UpdateBody) { u.ScopeDigest = localScope },
		"endpoint":         func(u *UpdateBody) { u.Endpoint = "127.0.0.3:20003" },
		"prior":            func(u *UpdateBody) { u.PriorEndpoint = "127.0.0.4:20004" },
		"issued":           func(u *UpdateBody) { u.Issued = testNow().Add(-time.Second).Format(time.RFC3339Nano) },
		"recipient tunnel": func(u *UpdateBody) { u.RecipientTunnelKey = otherTunnelKey },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			u := prepared
			change(&u)
			e, err := Sign(u, key)
			if err != nil {
				t.Fatal("invalid synthetic substitution", err)
			}
			if _, err := ProposeIssued(s, prepared.Recipient, e, testNow(), modelBudget); err == nil {
				t.Fatal("accepted signed fields outside the prepared snapshot/time")
			}
		})
	}
}
