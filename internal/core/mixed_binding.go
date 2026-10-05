package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/webkaz-labs/sobalink/internal/connectionroute"
)

const mixedIdentityPath = "/v1/connection-identity"
const mixedProofPath = "/v1/connection-proof"

type mixedIdentityInfo struct {
	PublicKey  string                              `json:"publicKey"`
	Identities []connectionroute.TransportIdentity `json:"identities"`
}

func (c *Core) mixedIdentityInfo(ctx context.Context) (mixedIdentityInfo, error) {
	n, ok := c.nodeCopy().(*mixedBackend)
	if !ok {
		return mixedIdentityInfo{}, errors.New("mixed networking is not active")
	}
	s, e := c.readMixedState()
	if e != nil {
		return mixedIdentityInfo{}, e
	}
	seed, _ := hex.DecodeString(s.Seed)
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	_, states, e := n.routeSnapshot(ctx)
	if e != nil {
		return mixedIdentityInfo{}, e
	}
	out := mixedIdentityInfo{PublicKey: hex.EncodeToString(pub)}
	for _, name := range n.order {
		state := states[name]
		id := state.SelfID
		if id == "" {
			if keyed, ok := n.nodes[name].(interface{ PublicKey() string }); ok {
				id = keyed.PublicKey()
			}
		}
		if !state.Snapshot.Running || id == "" {
			return mixedIdentityInfo{}, errors.New("all selected identities must be current before binding")
		}
		out.Identities = append(out.Identities, connectionroute.TransportIdentity{Backend: name, ID: id})
	}
	return out, nil
}

// serveMixedIdentity runs only after the ordinary transport identity check.
// It cannot configure a binding or grant a resource on behalf of a remote peer.
func (c *Core) serveMixedIdentity(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != mixedIdentityPath && r.URL.Path != mixedProofPath {
		return false
	}
	info, e := c.mixedIdentityInfo(r.Context())
	if e != nil {
		peerFailure(w, 409)
		return true
	}
	if r.Method == "GET" && r.URL.Path == mixedIdentityPath {
		reply(w, 200, info)
		return true
	}
	if r.Method != "POST" || r.URL.Path != mixedProofPath {
		peerFailure(w, 405)
		return true
	}
	var claim connectionroute.Claim
	if readJSON(r, 8<<10, &claim) != nil {
		peerFailure(w, 400)
		return true
	}
	public, e := hex.DecodeString(info.PublicKey)
	now := time.Now()
	if e != nil || !bytes.Equal(public, claim.PublicKey) || !now.Before(claim.Expires) || claim.Expires.After(now.Add(5*time.Minute)) {
		peerFailure(w, 403)
		return true
	}
	for _, want := range claim.Identities {
		found := false
		for _, own := range info.Identities {
			if want == own {
				found = true
				break
			}
		}
		if !found {
			peerFailure(w, 403)
			return true
		}
	}
	s, e := c.readMixedState()
	if e != nil {
		peerFailure(w, 409)
		return true
	}
	seed, _ := hex.DecodeString(s.Seed)
	proof, e := connectionroute.Sign(claim, ed25519.NewKeyFromSeed(seed))
	if e != nil {
		peerFailure(w, 400)
		return true
	}
	reply(w, 200, proof)
	return true
}
func (n *mixedBackend) routeRequest(ctx context.Context, route mixedPeerRoute, method, path string, body, out any) error {
	var input io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		input = bytes.NewReader(raw)
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			return nil, connectionroute.ErrDenied
		}
		conn, e := n.nodes[route.backend].DialIP(ctx, "tcp", netip.AddrPortFrom(route.address, PeerPort))
		if e != nil {
			return nil, e
		}
		check := func() error {
			id, e := observedBackendPeer(ctx, n.nodes[route.backend], conn)
			if e != nil || id != route.id {
				return connectionroute.ErrDenied
			}
			return nil
		}
		if e = check(); e != nil {
			_ = conn.Close()
			return nil, e
		}
		return &proofIdentityConn{Conn: conn, check: check}, nil
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return connectionroute.ErrDenied }}
	req, e := http.NewRequestWithContext(ctx, method, "http://peer.invalid"+path, input)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := client.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return connectionroute.ErrDenied
	}
	reader := &io.LimitedReader{R: response.Body, N: 16 << 10}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(out); e != nil {
		return e
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || reader.N == 0 {
		return connectionroute.ErrBinding
	}
	return nil
}
func (c *Core) bindMixedPeers(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Peers []string `json:"peers"`
	}
	if e := decodePayload(raw, &in); e != nil {
		return nil, e
	}
	if len(in.Peers) < 2 || len(in.Peers) > 3 {
		return nil, errors.New("select two or three separate authenticated peer routes")
	}
	n, ok := c.nodeCopy().(*mixedBackend)
	if !ok {
		return nil, errors.New("start mixed networking before verifying a peer binding")
	}
	routes, states, e := n.routeSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	selected := make([]mixedPeerRoute, 0, len(in.Peers))
	claims := make([]connectionroute.TransportIdentity, 0, len(in.Peers))
	backends := map[string]bool{}
	var public []byte
	for _, id := range in.Peers {
		rs := routes[id]
		if len(rs) != 1 || backends[rs[0].backend] || rs[0].peer.Expired || !states[rs[0].backend].Snapshot.Running {
			return nil, connectionroute.ErrBinding
		}
		route := rs[0]
		backends[route.backend] = true
		var info mixedIdentityInfo
		if e = n.routeRequest(ctx, route, "GET", mixedIdentityPath, nil, &info); e != nil {
			return nil, e
		}
		key, e := hex.DecodeString(info.PublicKey)
		if e != nil || len(key) != ed25519.PublicKeySize {
			return nil, connectionroute.ErrBinding
		}
		if public == nil {
			public = key
		} else if !bytes.Equal(public, key) {
			return nil, connectionroute.ErrBinding
		}
		selected = append(selected, route)
		claims = append(claims, connectionroute.TransportIdentity{Backend: route.backend, ID: route.id})
	}
	challenge, e := connectionroute.NewChallenge(public, claims, time.Now(), 2*time.Minute)
	if e != nil {
		return nil, e
	}
	for i, route := range selected {
		var proof connectionroute.Proof
		if e = n.routeRequest(ctx, route, "POST", mixedProofPath, challenge.Claim(), &proof); e != nil {
			return nil, e
		}
		if e = challenge.Observe(claims[i], proof, time.Now()); e != nil {
			return nil, e
		}
	}
	binding, e := challenge.Finish(time.Now())
	if e != nil {
		return nil, e
	}
	state, e := c.readMixedState()
	if e != nil {
		return nil, e
	}
	for _, old := range state.Bindings {
		if old.PeerID == binding.PeerID {
			return nil, errors.New("logical identity already has a binding; remove it before changing routes")
		}
	}
	state.Bindings = append(state.Bindings, binding)
	if e = validateMixedState(state); e != nil {
		return nil, e
	}
	if int64(len(state.Bindings)) > c.limit("logical", "trustedPeers") {
		return nil, errors.New("mixed binding capacity reached; review capacity settings")
	}
	// Old approvals remain bound to their old transport IDs, never copied to the
	// newly verified logical peer. Stop their flows before publishing the mapping.
	if e = c.pauseMixedApprovals(in.Peers); e != nil {
		return nil, e
	}
	encoded, e := json.Marshal(state)
	if e != nil {
		return nil, e
	}
	if int64(len(encoded)) > c.limit("resources", "lanStateBytes") {
		return nil, errors.New("mixed identity state exceeds the configured storage budget")
	}
	saveErr := c.writeAtomic(filepath.Join(c.dir, "mixed.json"), encoded)
	if saveErr != nil {
		if atomicPublished(saveErr) {
			_ = c.failWorkerScope(n, saveErr)
		}
		return nil, saveErr
	}
	n.mu.Lock()
	n.bindings = state.Bindings
	n.mu.Unlock()
	return map[string]any{"binding": binding, "applicationApprovalRequired": true, "oldApprovalsMigrated": false}, nil
}

func (c *Core) pauseMixedApprovals(ids []string) error {
	p := c.profileCopy()
	changed := false
	// Runtime authority closes before the first fallible durability operation.
	for _, id := range ids {
		c.revokePeer(id)
		for i := range p.Peers {
			if p.Peers[i].ID == id && p.Peers[i].Network == "mixed" {
				p.Peers[i].Paused = true
				p.Peers[i].Autosave = false
				changed = true
			}
		}
	}
	c.mu.Lock()
	c.profile = p
	c.mu.Unlock()
	fail := func(e error) error { _ = c.failWorkerScope(c.nodeCopy(), e); return e }
	for _, id := range ids {
		if e := c.revokeStartupPeer(id); e != nil {
			return fail(e)
		}
	}
	if !changed {
		return nil
	}
	if e := c.saveProfile(p); e != nil {
		return fail(e)
	}
	return nil
}
func (c *Core) unbindMixedPeer(raw json.RawMessage) (any, error) {
	var in struct {
		PeerID string `json:"peerId"`
	}
	if e := decodePayload(raw, &in); e != nil {
		return nil, e
	}
	state, e := c.readMixedState()
	if e != nil {
		return nil, e
	}
	found := false
	kept := make([]connectionroute.Binding, 0, len(state.Bindings))
	for _, b := range state.Bindings {
		if b.PeerID == in.PeerID {
			found = true
		} else {
			kept = append(kept, b)
		}
	}
	if !found {
		return nil, connectionroute.ErrBinding
	}
	if e = c.pauseMixedApprovals([]string{in.PeerID}); e != nil {
		return nil, e
	}
	state.Bindings = kept
	encoded, e := json.Marshal(state)
	if e != nil {
		return nil, e
	}
	saveErr := c.writeAtomic(filepath.Join(c.dir, "mixed.json"), encoded)
	if saveErr != nil {
		if atomicPublished(saveErr) && c.nodeCopy() != nil {
			_ = c.failWorkerScope(c.nodeCopy(), saveErr)
		}
		return nil, saveErr
	}
	if n, ok := c.nodeCopy().(*mixedBackend); ok {
		n.mu.Lock()
		n.bindings = kept
		n.mu.Unlock()
	}
	return map[string]any{"removed": true, "applicationApprovalsResumed": false}, nil
}

type proofIdentityConn struct {
	net.Conn
	check func() error
}

func (c *proofIdentityConn) Read(p []byte) (int, error) {
	if e := c.check(); e != nil {
		return 0, e
	}
	n, e := c.Conn.Read(p)
	if verify := c.check(); verify != nil {
		clear(p[:n])
		return 0, verify
	}
	return n, e
}
func (c *proofIdentityConn) Write(p []byte) (int, error) {
	if e := c.check(); e != nil {
		return 0, e
	}
	return c.Conn.Write(p)
}

// retireMixedTransport prevents a later same-key re-pair in a standalone mode
// from restoring older mixed routing or saved outbound approvals.
func (c *Core) retireMixedTransport(backend, id string) error {
	state, e := c.readMixedState()
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var affected []string
	kept := make([]connectionroute.Binding, 0, len(state.Bindings))
	for _, b := range state.Bindings {
		found := false
		for _, claim := range b.Identities {
			if claim.Backend == backend && claim.ID == id {
				found = true
				break
			}
		}
		if found {
			affected = append(affected, b.PeerID)
		} else {
			kept = append(kept, b)
		}
	}
	if len(affected) == 0 {
		return nil
	}
	pauseErr := c.pauseMixedApprovals(affected)
	state.Bindings = kept
	raw, e := json.Marshal(state)
	if e != nil {
		return errors.Join(pauseErr, e)
	}
	saveErr := c.writeAtomic(filepath.Join(c.dir, "mixed.json"), raw)
	if pauseErr != nil || saveErr != nil {
		err := errors.Join(pauseErr, saveErr)
		_ = c.failWorkerScope(c.nodeCopy(), err)
		return err
	}
	if n, ok := c.nodeCopy().(*mixedBackend); ok {
		n.mu.Lock()
		n.bindings = kept
		n.mu.Unlock()
	}
	return nil
}
