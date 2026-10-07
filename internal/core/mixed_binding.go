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
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
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
func (c *Core) routeRequest(ctx context.Context, n *mixedBackend, route mixedPeerRoute, method, path string, body, out any) (requestErr error) {
	ctx, releaseEntrance, err := c.enterPeerHTTP(ctx)
	if err != nil {
		return err
	}
	defer releaseEntrance()
	var input io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		input = bytes.NewReader(raw)
	}
	target, _, e := validatePeerHTTPRequest(method, path, true)
	if e != nil {
		return e
	}
	captured, e := capturePeerHTTPBackend(ctx, route.backend, n.nodes[route.backend], route.id, route.peer.DNSName, netip.AddrPortFrom(route.address, PeerPort), true)
	if e != nil {
		return e
	}
	// The old client timeout covered dial, headers, body, and decoding.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	temporary, commit, e := privatePeerHTTPResult(out)
	if e != nil {
		return e
	}
	source, size, e := adoptPeerHTTPSource(input)
	if e != nil {
		return e
	}
	h, e := c.newPeerHTTPRequest(ctx, route.id, captured, source)
	if e != nil {
		return e
	}
	defer func() {
		if err := h.finish(ctx); requestErr == nil {
			requestErr = err
		}
	}()
	if e = h.prepare(c, route.id, captured); e != nil {
		return e
	}
	response, e := h.request(method, target.String(), "application/json", size, false, connectionroute.ErrDenied)
	if e != nil {
		return e
	}
	if response.StatusCode != 200 {
		return connectionroute.ErrDenied
	}
	reader := &io.LimitedReader{R: response.Body, N: 16 << 10}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(temporary); e != nil {
		return e
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || reader.N == 0 {
		return connectionroute.ErrBinding
	}
	return h.publish(commit)
}
func (c *Core) bindMixedPeers(ctx context.Context, raw json.RawMessage) (result any, resultErr error) {
	var in struct {
		Peers []string `json:"peers"`
	}
	if e := decodePayload(raw, &in); e != nil {
		return nil, e
	}
	if len(in.Peers) < 2 || len(in.Peers) > 3 {
		return nil, &localCommandError{"mixed_binding_selection", "select two or three authenticated peer routes from different backends"}
	}
	n, ok := c.nodeCopy().(*mixedBackend)
	if !ok {
		return nil, &localCommandError{"mixed_network_required", "start the reviewed mixed network before verifying a peer binding"}
	}
	workDone, e := c.beginWork()
	if e != nil {
		return nil, e
	}
	defer workDone()
	ctx, application, e := c.peerHTTPApplication(ctx)
	if e != nil {
		return nil, e
	}
	defer application.finish()
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
		if e = c.routeRequest(ctx, n, route, "GET", mixedIdentityPath, nil, &info); e != nil {
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
		if e = c.routeRequest(ctx, n, route, "POST", mixedProofPath, challenge.Claim(), &proof); e != nil {
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
	encoded, e := json.Marshal(state)
	if e != nil {
		return nil, e
	}
	if int64(len(encoded)) > c.limit("resources", "lanStateBytes") {
		return nil, errors.New("mixed identity state exceeds the configured storage budget")
	}
	// Command serialization already holds Core.op. Recheck the exact backend
	// under Core.mu, then admit this precomputed durable transaction at each
	// captured origin's stop fence. No origin mutex or publication permit spans
	// approval revocation, disk I/O or the final mapping update.
	c.mu.Lock()
	if c.closing || c.ctx.Err() != nil || c.node != n || c.profile.Settings.Network != "mixed" {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	releaseCommit, e := application.admitCommit()
	c.mu.Unlock()
	if e != nil {
		return nil, e
	}
	failure := newPeerScopeFailure(c, n)
	defer func() {
		// Every transitive scope failure has already sealed logical authority
		// and signalled children. Release this participant before any real
		// backend/server join, and preserve failure through the result.
		releaseCommit()
		application.finish()
		resultErr = failure.join(resultErr)
		if resultErr != nil {
			result = nil
		}
	}()
	// Old approvals stay bound to their old transport IDs. They are closed
	// before publication and are never copied to the new logical identity.
	if e = c.pauseMixedApprovalsWithFailure(in.Peers, failure); e != nil {
		return nil, e
	}
	saveErr := c.writeAtomic(filepath.Join(c.dir, "mixed.json"), encoded)
	if saveErr != nil {
		if atomicPublished(saveErr) {
			return nil, failure.fail(saveErr)
		}
		return nil, saveErr
	}
	n.mu.Lock()
	n.bindings = state.Bindings
	n.mu.Unlock()
	return map[string]any{"binding": binding, "applicationApprovalRequired": true, "oldApprovalsMigrated": false}, nil
}

func (c *Core) pauseMixedApprovals(ids []string) error {
	return c.pauseMixedApprovalsWithFailure(ids, nil)
}

func (c *Core) pauseMixedApprovalsWithFailure(ids []string, failure *peerScopeFailure) error {
	fail := func(cause error) error {
		if failure != nil {
			return failure.fail(cause)
		}
		return c.failWorkerScope(c.nodeCopy(), cause)
	}
	p := c.profileCopy()
	changed := false
	// Runtime authority closes before the first fallible durability operation.
	for _, id := range ids {
		if err := c.revokePeerWithFailure(id, failure); err != nil {
			return fail(err)
		}
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
	for _, id := range ids {
		if e := c.revokeStartupPeerWithFailure(id, failure); e != nil {
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

func (c *proofIdentityConn) TransportOrigin() transportorigin.Origin {
	if carrier, ok := c.Conn.(transportorigin.Carrier); ok {
		return carrier.TransportOrigin()
	}
	return nil
}

func (c *proofIdentityConn) WaitClosed(ctx context.Context) error {
	if terminal, ok := c.Conn.(transportorigin.TerminalConnection); ok {
		return terminal.WaitClosed(ctx)
	}
	if c.TransportOrigin() != nil {
		return transportorigin.ErrMissingOrigin
	}
	return nil
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
