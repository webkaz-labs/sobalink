package directlan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const MaxInvitationTTL = 10 * time.Minute
const invitationPrefix = "soba-directlan1."

// Invitation is a secret one-use pairing capability. Host.Key is the exact TLS
// trust anchor. The recipient must already know their own key; IP/name text is
// never accepted as identity. Copy it only through the chosen private channel.
type Invitation struct {
	Version      int       `json:"version"`
	Host         Peer      `json:"host"`
	RecipientKey string    `json:"recipient_key"`
	Token        string    `json:"token"`
	Expires      time.Time `json:"expires"`
}
type pendingInvitation struct {
	invitation Invitation
	recipient  Peer
	deadline   time.Time
}

func active(now, until time.Time) bool {
	return !until.IsZero() && now.Before(until) && now.UTC().Before(until.UTC())
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (i Invitation) ValidateFor(key string, now time.Time) error {
	a := i.Host.Endpoint.Addr()
	if !i.Host.Endpoint.IsValid() || i.Host.Endpoint.Port() < 1024 || a.Is4In6() || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || (!a.IsPrivate() && !a.IsLoopback()) {
		return ErrInvitation
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(i.Token)
	if i.Version != 1 || !validKey(key) || key != i.RecipientKey || !validKey(i.Host.Key) || !validTunnelKey(i.Host.TunnelKey) || i.Host.Key == key || !validName(i.Host.Name) || e != nil || len(b) != 32 || len(i.Token) != 43 || !active(now, i.Expires) || i.Expires.Sub(now) > MaxInvitationTTL {
		return ErrInvitation
	}
	return nil
}
func (i Invitation) Encode() (string, error) {
	if e := i.ValidateFor(i.RecipientKey, time.Now()); e != nil {
		return "", e
	}
	b, e := json.Marshal(i)
	if e != nil || len(b) > maxControlFrame {
		return "", ErrInvitation
	}
	return invitationPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func ParseInvitation(text string) (Invitation, error) {
	var i Invitation
	if !strings.HasPrefix(text, invitationPrefix) || len(text) > maxControlFrame*2 {
		return i, ErrInvitation
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(text, invitationPrefix))
	if e != nil || len(b) > maxControlFrame || strictJSON(b, &i) != nil || i.ValidateFor(i.RecipientKey, time.Now()) != nil {
		return Invitation{}, ErrInvitation
	}
	return i, nil
}

// IssueInvitation accepts a recipient key/name; its endpoint may be omitted.
// When present, it additionally pins the recipient endpoint in the handshake.
// HostName is optional presentation metadata and has no authorization effect.
func (n *Node) IssueInvitation(ctx context.Context, recipient Peer, ttl time.Duration) (Invitation, error) {
	return n.IssueNamedInvitation(ctx, recipient, "", ttl)
}
func (n *Node) IssueNamedInvitation(ctx context.Context, recipient Peer, hostName string, ttl time.Duration) (Invitation, error) {
	if e := ctx.Err(); e != nil {
		return Invitation{}, e
	}
	if !validKey(recipient.Key) || recipient.Key == n.PublicKey() || (recipient.TunnelKey != "" && !validTunnelKey(recipient.TunnelKey)) || !validName(recipient.Name) || !validName(hostName) || ttl < time.Second || ttl > MaxInvitationTTL || (recipient.Endpoint.IsValid() && !n.cfg.permits(recipient.Endpoint, true)) {
		return Invitation{}, ErrInvitation
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := n.readyLocked(); e != nil {
		return Invitation{}, e
	}
	if n.cfg.Persist == nil {
		return Invitation{}, errors.New("durable pairing persistence callback required")
	}
	if n.managedKey(recipient.Key) || n.peers[recipient.Key] != nil {
		return Invitation{}, ErrUntrusted
	}
	now := time.Now()
	for h, p := range n.invites {
		if !active(now, p.deadline) {
			delete(n.invites, h)
		} else if p.recipient.Key == recipient.Key {
			return Invitation{}, errors.New("cancel the existing invitation before replacing it")
		}
	}
	if len(n.invites) >= n.cfg.InvitationLimit || n.peerCapacityLocked() {
		return Invitation{}, ErrCapacity
	}
	token := make([]byte, 32)
	if _, e := rand.Read(token); e != nil {
		return Invitation{}, e
	}
	g := n.generation.Load()
	if g == nil {
		return Invitation{}, ErrUnavailable
	}
	inv := Invitation{Version: 1, Host: Peer{Key: n.PublicKey(), Name: hostName, Endpoint: g.cfg.Listen, TunnelKey: n.cfg.Identity.TunnelKey()}, RecipientKey: recipient.Key, Token: base64.RawURLEncoding.EncodeToString(token), Expires: now.Add(ttl)}
	n.invites[tokenHash(inv.Token)] = pendingInvitation{inv, recipient, inv.Expires}
	return inv, nil
}
func (n *Node) CancelInvitation(token string) {
	n.mu.Lock()
	delete(n.invites, tokenHash(token))
	n.mu.Unlock()
}
func (n *Node) acceptPair(ctx context.Context, w *wire, req request) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := n.readyLocked(); e != nil {
		return e
	}
	if n.managedKey(w.key) || n.generation.Load() != w.g {
		return ErrRecovery
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if n.cfg.Persist == nil {
		return ErrRecovery
	}
	p, ok := n.invites[tokenHash(req.Token)]
	if !ok || !active(time.Now(), p.deadline) || req.Peer.Key != p.recipient.Key || w.key != p.recipient.Key || !w.g.cfg.permits(req.Peer.Endpoint, true) || !validName(req.Peer.Name) || !validTunnelKey(req.Peer.TunnelKey) || n.peers[w.key] != nil {
		return ErrInvitation
	}
	if p.recipient.TunnelKey != "" && p.recipient.TunnelKey != req.Peer.TunnelKey {
		return ErrInvitation
	}
	if p.recipient.Endpoint.IsValid() && p.recipient.Endpoint != req.Peer.Endpoint {
		return ErrInvitation
	}
	// Only the expected key over mutually authenticated TLS can consume it.
	// Consume before persistence: failure must not accidentally permit a replay.
	delete(n.invites, tokenHash(req.Token))
	remote := *req.Peer
	remote.Name = p.recipient.Name
	return n.commitPeerLocked(remote)
}
func (n *Node) commitPeerLocked(p Peer) error {
	if n.managedKey(p.Key) {
		return ErrUntrusted
	}
	if !validTunnelKey(p.TunnelKey) || p.TunnelKey == n.cfg.Identity.TunnelKey() {
		return ErrIdentity
	}
	for _, existing := range n.peers {
		if existing.peer.TunnelKey == p.TunnelKey {
			return ErrIdentity
		}
	}
	if n.peerCapacityLocked() {
		return ErrCapacity
	}
	if n.peers[p.Key] != nil {
		return ErrUntrusted
	}
	snapshot := append(n.snapshotLocked(), p)
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Key < snapshot[j].Key })
	if e := n.cfg.Persist(snapshot); e != nil {
		n.failClosedLocked()
		return errors.Join(ErrRecovery, e)
	}
	n.peers[p.Key] = n.newPeerState(p)
	if n.engine != nil {
		n.refreshBindLocked()
		if e := n.installPeerLocked(p); e != nil {
			n.failClosedLocked()
			return errors.Join(ErrRecovery, e)
		}
	}
	for h, inv := range n.invites {
		if inv.recipient.Key == p.Key {
			delete(n.invites, h)
		}
	}
	return nil
}
func (n *Node) failClosedLocked() {
	n.recovery = true
	n.nonTransportRecovery = true
	if g := n.generation.Load(); g != nil {
		g.requestStop(ErrRecovery)
	}
	if n.bind != nil {
		n.bind.policy.Store(&bindPolicy{endpoints: map[netip.AddrPort]bool{}, sources: map[netip.Addr]bool{}})
	}
	for p := range n.dials {
		p.cancel()
	}
	for _, cancel := range n.attempts {
		cancel()
	}
	for w := range n.wires {
		w.raw.Close()
	}
	n.invites = map[string]pendingInvitation{}
}
func (n *Node) PairInvitation(ctx context.Context, inv Invitation) error {
	if e := inv.ValidateFor(n.PublicKey(), time.Now()); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	n.mu.Lock()
	if e := n.readyLocked(); e != nil {
		n.mu.Unlock()
		return e
	}
	if n.cfg.Persist == nil {
		n.mu.Unlock()
		return errors.New("durable pairing persistence callback required")
	}
	if n.managedKey(inv.Host.Key) || n.peers[inv.Host.Key] != nil {
		n.mu.Unlock()
		return ErrUntrusted
	}
	if n.peerCapacityLocked() {
		n.mu.Unlock()
		return ErrCapacity
	}
	if n.attempts[inv.Host.Key] != nil {
		n.mu.Unlock()
		return ErrInvitation
	}
	for _, pending := range n.invites {
		if pending.recipient.Key == inv.Host.Key && active(time.Now(), pending.deadline) {
			n.mu.Unlock()
			return errors.New("cancel the issued invitation before joining this peer")
		}
	}
	attempt, cancel := context.WithTimeout(ctx, handshakeTimeout)
	g := n.generation.Load()
	if g == nil || !g.cfg.permits(inv.Host.Endpoint, true) {
		n.mu.Unlock()
		cancel()
		return ErrPolicy
	}
	work, e := g.acquireWork(cancel, false)
	if e != nil {
		n.mu.Unlock()
		cancel()
		return e
	}
	n.attempts[inv.Host.Key] = cancel
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.attempts, inv.Host.Key); n.mu.Unlock(); work.finish() }()
	c, w, e := n.connect(attempt, inv.Host, nil)
	if e != nil {
		return e
	}
	defer n.removeWire(w)
	stop := watchConnection(attempt, w.raw)
	defer stop()
	self := Peer{Key: n.PublicKey(), Endpoint: g.cfg.Listen, TunnelKey: g.cfg.Identity.TunnelKey()}
	if e = writeJSON(c, request{Version: 1, Operation: "pair", Token: inv.Token, Peer: &self}); e != nil {
		return e
	}
	var reply response
	if e = readJSON(c, &reply); e != nil {
		return errors.Join(ErrPairUncertain, e)
	}
	if reply.Version != 1 || !reply.OK || reply.Code != "" {
		return ErrInvitation
	}
	if reply.Peer == nil || reply.Peer.Key != inv.Host.Key || reply.Peer.Endpoint != inv.Host.Endpoint || reply.Peer.TunnelKey != inv.Host.TunnelKey {
		return errors.Join(ErrRemotePairedLocalSave, ErrIdentity)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := n.readyLocked(); e != nil {
		return errors.Join(ErrRemotePairedLocalSave, e)
	}
	if e := attempt.Err(); e != nil {
		return errors.Join(ErrRemotePairedLocalSave, e)
	}
	if e = n.commitPeerLocked(inv.Host); e != nil {
		return errors.Join(ErrRemotePairedLocalSave, e)
	}
	return nil
}

// Revoke immediately invalidates authorization, cancels pairing attempts and
// closes all established streams for the key, even if persistence fails.
func (n *Node) Revoke(key string) error {
	// Legacy snapshots cannot encode a managed terminal tombstone.
	if n.managedKey(key) {
		return ErrUntrusted
	}
	if !validKey(key) {
		return ErrIdentity
	}
	n.mu.Lock()
	// Initial construction is outside Node.mu. Preserve the old serialization:
	// revocation must see the completed initial snapshot, never be overwritten
	// by its publication. Stop and failed construction still win below.
	for !n.started && !n.closed && !n.recovery && !n.closing.Load() {
		build := n.building.Load()
		if build == nil {
			break
		}
		n.mu.Unlock()
		<-build.done
		n.mu.Lock()
	}
	defer n.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	if n.recovery {
		return ErrRecovery
	}
	if n.cfg.Persist == nil {
		return errors.New("durable revocation persistence callback required")
	}
	for p := range n.dials {
		if p.key == key {
			p.cancel()
		}
	}
	if cancel := n.attempts[key]; cancel != nil {
		cancel()
	}
	for h, inv := range n.invites {
		if inv.recipient.Key == key {
			delete(n.invites, h)
		}
	}
	previous, paired := n.peers[key]
	delete(n.peers, key)
	if n.bind != nil {
		n.refreshBindLocked()
	}
	for w := range n.wires {
		if w.key == key {
			w.raw.Close()
		}
	}
	if !paired {
		return nil
	}
	if n.engine != nil {
		if e := n.engine.IpcSet("public_key=" + previous.peer.TunnelKey + "\nremove=true\n"); e != nil {
			n.failClosedLocked()
			return errors.Join(ErrRecovery, e)
		}
	}
	if e := n.cfg.Persist(n.snapshotLocked()); e != nil {
		n.failClosedLocked()
		return errors.Join(ErrRecovery, e)
	}
	return nil
}
