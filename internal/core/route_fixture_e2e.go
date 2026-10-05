//go:build soba_e2e

package core

import (
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"tailscale.com/types/key"
)

// PrepareRouteBrowserFixture creates a disposable synthetic pair for actual
// local HTTP/UI authorization tests. It never starts a network engine and is
// excluded from ordinary builds and distribution. The harness supplies its own
// freshly allocated private temporary directory, never a user profile.
func PrepareRouteBrowserFixture(directory, updateFile string) (string, error) {
	if err := config.SecureDir(directory); err != nil {
		return "", err
	}
	a, b := lanlink.GenerateIdentity(), lanlink.GenerateIdentity()
	relay := lanlink.TrustedRelay{Address: netip.MustParseAddrPort("127.0.0.1:54446"), CertificateSHA256: strings.Repeat("a", 64)}
	an, err := lanlink.NewNode(lanlink.NodeConfig{Identity: a, Relay: relay, Trust: lanlink.NewBook()})
	if err != nil {
		return "", err
	}
	defer an.Close()
	bn, err := lanlink.NewNode(lanlink.NodeConfig{Identity: b, Relay: relay, Trust: lanlink.NewBook()})
	if err != nil {
		return "", err
	}
	defer bn.Close()
	ar, br := key.NewNode(), key.NewNode()
	role := func(k key.NodePrivate) string { return strings.TrimPrefix(k.Public().String(), "nodekey:") }
	ab := lanlink.RemotePeer{Peer: lanlink.Peer{Key: b.PublicKey(), Name: "Route peer"}, Address: bn.Address(), ClientPrivate: ar, IncomingClientKey: role(br)}
	ba := lanlink.RemotePeer{Peer: lanlink.Peer{Key: a.PublicKey(), Name: "Route notebook"}, Address: an.Address(), ClientPrivate: br, IncomingClientKey: role(ar)}
	state := lanState{Version: 1, Identity: a, Selection: &LANSelection{Kind: "relay", Address: relay.Address.String(), CertificateSHA256: relay.CertificateSHA256}, Trust: lanlink.Snapshot{Version: 1, Peers: []lanlink.Peer{ab.Peer}}, Remotes: []lanlink.RemotePeer{ab}}
	if err := validateLANState(state); err != nil {
		return "", err
	}
	if err := config.WriteJSON(filepath.Join(directory, "lan.json"), state); err != nil {
		return "", err
	}
	profile := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "system", Network: "lan", Hostname: "route-notebook"}, Peers: []Trust{}, Services: []ServiceSpec{}}
	if err := config.WriteJSON(filepath.Join(directory, "sobalink.json"), profile); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	offered := []lanlink.RouteCandidate{{Relay: relay, Scope: "local"}, {Relay: lanlink.TrustedRelay{Address: netip.MustParseAddrPort("192.0.2.20:443"), CertificateSHA256: strings.Repeat("b", 64)}, Scope: "external"}}
	frame, err := lanlink.SealRouteUpdateWithLifetime(b, ba, 1, offered, lanlink.RouteLifetimeUntilRevoked, now, time.Time{})
	if err != nil {
		return "", err
	}
	withdrawal, err := lanlink.SealRouteUpdateWithLifetime(b, ba, 2, nil, lanlink.RouteLifetimeUntilRevoked, now, time.Time{})
	if err != nil {
		return "", err
	}
	if err := config.WriteJSON(updateFile, map[string]string{"update": string(frame), "withdrawal": string(withdrawal)}); err != nil {
		return "", err
	}
	return b.PublicKey(), nil
}
