package lanlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"io"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	tailcat "github.com/webkaz-labs/sobalink/internal/routecat"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

const maxPairMessage = 64 << 10
const pairingVersion = 1
const requestDomain = "sobalink pairing v1 request"
const replyDomain = "sobalink pairing v1 reply"

var ErrCancelInviteFirst = errors.New("cancel the invitation you issued before joining this peer's invitation")
var ErrPairReplyUncertain = errors.New("pairing reply unavailable; remote may already have committed")

var ErrRemotePairedLocalSave = errors.New("remote pairing committed but local save failed; revoke the remote approval before retrying")

type Invitation struct {
	Version       int          `json:"version"`
	EmbeddedRelay bool         `json:"embedded_relay"`
	Host          PeerOffer    `json:"host"`
	RecipientKey  string       `json:"recipient_key"`
	Relay         TrustedRelay `json:"relay"`
	Expires       time.Time    `json:"expires"`
	Token         string       `json:"token"`
}

// ValidateFor checks invitation contents locally, without creating a node or
// contacting the host. Only pairing can prove that the host still accepts the
// invitation; this check cannot establish remote availability or consumption.
func (inv Invitation) ValidateFor(recipient string, now time.Time) error {
	if inv.Version != pairingVersion || !validKey(recipient) || inv.RecipientKey != recipient || inv.Host.Peer.Key == recipient || !deadline.Active(now, inv.Expires) {
		return ErrInvite
	}
	if len(inv.Token) != 43 {
		return ErrInvite
	}
	token, err := base64.RawURLEncoding.Strict().DecodeString(inv.Token)
	if err != nil || len(token) != 32 {
		return ErrInvite
	}
	if err := inv.Relay.Validate(); err != nil {
		return err
	}
	_, err = validateRemote(inv.Host, inv.Relay)
	return err
}

func (n *Node) IssueInvitation(ctx context.Context, recipient Peer, hostName string, ttl time.Duration) (Invitation, error) {
	if n.cfg.Persist == nil {
		return Invitation{}, errors.New("durable pairing persistence required")
	}
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	if recipient.Key == n.PublicKey() {
		return Invitation{}, ErrInvite
	}
	if _, e := n.cfg.Trust.Epoch(recipient.Key); e == nil {
		return Invitation{}, errors.New("peer is already paired")
	}
	n.mu.Lock()
	closed, busy, recovery := n.closed, len(n.attempts[recipient.Key]) > 0, n.pairingRecovery
	n.mu.Unlock()
	if recovery {
		return Invitation{}, config.ErrAtomicRecovery
	}
	if closed {
		return Invitation{}, net.ErrClosed
	}
	if busy {
		return Invitation{}, errors.New("pairing to this peer is already in progress")
	}
	offer := n.Offer(hostName)
	if !validPeer(offer.Peer) {
		return Invitation{}, ErrInvite
	}
	now := time.Now()
	token, e := n.cfg.Trust.IssueTrusted(ctx, recipient, n.cfg.Relay, now, ttl)
	if e != nil {
		return Invitation{}, e
	}
	return Invitation{Version: pairingVersion, Host: offer, RecipientKey: recipient.Key, Relay: n.cfg.Relay, Expires: now.Add(ttl), Token: token, EmbeddedRelay: n.cfg.EmbeddedRelay}, nil
}
func (n *Node) CancelInvitation(token string) { n.cfg.Trust.CancelInvite(token) }
func (n *Node) PairInvitation(ctx context.Context, inv Invitation) error {
	if err := inv.ValidateFor(n.PublicKey(), time.Now()); err != nil {
		return err
	}
	if inv.Relay != n.cfg.Relay {
		return ErrRelayMismatch
	}
	return n.pair(ctx, inv.Host, inv.Token, inv.EmbeddedRelay)
}

type pairEnvelope struct {
	Sender string `json:"sender"`
	Box    []byte `json:"box"`
}
type pairRequest struct {
	Version        int          `json:"version"`
	Domain         string       `json:"domain"`
	Sender         string       `json:"sender"`
	Recipient      string       `json:"recipient"`
	Nonce          []byte       `json:"nonce"`
	Token          string       `json:"token"`
	TokenHash      string       `json:"token_hash"`
	Relay          TrustedRelay `json:"relay"`
	RoleKey        string       `json:"role_key"`
	CapabilityHash string       `json:"capability_hash"`
	Address        tailcat.Addr `json:"address"`
}
type pairReply struct {
	Version            int          `json:"version"`
	Domain             string       `json:"domain"`
	Sender             string       `json:"sender"`
	Recipient          string       `json:"recipient"`
	Nonce              []byte       `json:"nonce"`
	TokenHash          string       `json:"token_hash"`
	Relay              TrustedRelay `json:"relay"`
	RoleKey            string       `json:"role_key"`
	CapabilityHash     string       `json:"capability_hash"`
	PeerCapabilityHash string       `json:"peer_capability_hash"`
	RequestHash        string       `json:"request_hash"`
}

func digest(s []byte) string { h := sha256.Sum256(s); return hex.EncodeToString(h[:]) }
func strictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return ErrInvite
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvite
	}
	return nil
}
func sealMessage(identity Identity, recipient string, value any) ([]byte, []byte, error) {
	pub, e := parseNodePublic(recipient)
	if e != nil {
		return nil, nil, e
	}
	plain, e := json.Marshal(value)
	if e != nil || len(plain) > 24<<10 {
		return nil, nil, ErrInvite
	}
	envelope, e := json.Marshal(pairEnvelope{identity.PublicKey(), identity.Key.SealTo(pub, plain)})
	if e != nil || len(envelope) > maxPairMessage {
		return nil, nil, ErrInvite
	}
	return envelope, plain, nil
}
func openMessage(identity Identity, data []byte, expectedSender string, out any) ([]byte, error) {
	if len(data) > maxPairMessage {
		return nil, ErrInvite
	}
	var envelope pairEnvelope
	if strictJSON(data, &envelope) != nil || envelope.Sender != expectedSender {
		return nil, ErrInvite
	}
	pub, e := parseNodePublic(envelope.Sender)
	if e != nil {
		return nil, e
	}
	plain, ok := identity.Key.OpenFrom(pub, envelope.Box)
	if !ok || len(plain) > 24<<10 || strictJSON(plain, out) != nil {
		return nil, ErrInvite
	}
	return plain, nil
}
func (n *Node) makeRequest(remote PeerOffer, token string, role key.NodePrivate) ([]byte, []byte, pairRequest, error) {
	if len(token) != 43 || role.IsZero() {
		return nil, nil, pairRequest{}, ErrInvite
	}
	var nonce [32]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return nil, nil, pairRequest{}, e
	}
	req := pairRequest{pairingVersion, requestDomain, n.PublicKey(), remote.Peer.Key, nonce[:], token, digest([]byte(token)), n.cfg.Relay, keyString(role.Public()), digest([]byte(n.Address())), n.Address()}
	frame, plain, e := sealMessage(n.cfg.Identity, remote.Peer.Key, req)
	return frame, plain, req, e
}
func (n *Node) readRequest(frame []byte, transportKey string) (pairRequest, Peer, []byte, error) {
	var envelope pairEnvelope
	if strictJSON(frame, &envelope) != nil {
		return pairRequest{}, Peer{}, nil, ErrInvite
	}
	var req pairRequest
	plain, e := openMessage(n.cfg.Identity, frame, envelope.Sender, &req)
	if e != nil {
		return req, Peer{}, nil, e
	}
	if req.Version != pairingVersion || req.Domain != requestDomain || req.Sender != envelope.Sender || req.Recipient != n.PublicKey() || len(req.Nonce) != 32 || len(req.Token) != 43 || req.TokenHash != digest([]byte(req.Token)) || req.Relay != n.cfg.Relay || !validKey(req.RoleKey) || req.RoleKey != transportKey || req.RoleKey == req.Sender || req.CapabilityHash != digest([]byte(req.Address)) {
		return req, Peer{}, nil, ErrInvite
	}
	peer, e := n.cfg.Trust.invitedPeer(req.Token, req.Sender, n.cfg.Relay.Address, time.Now())
	if e != nil {
		return req, Peer{}, nil, e
	}
	if _, e = validateRemote(PeerOffer{peer, req.Address}, n.cfg.Relay); e != nil {
		return req, Peer{}, nil, e
	}
	return req, peer, plain, nil
}
func (n *Node) makeReply(req pairRequest, requestPlain []byte, role key.NodePrivate) ([]byte, error) {
	reply := pairReply{pairingVersion, replyDomain, n.PublicKey(), req.Sender, req.Nonce, req.TokenHash, n.cfg.Relay, keyString(role.Public()), digest([]byte(n.Address())), req.CapabilityHash, digest(requestPlain)}
	frame, _, e := sealMessage(n.cfg.Identity, req.Sender, reply)
	return frame, e
}
func (n *Node) readReply(frame []byte, remote PeerOffer, req pairRequest, requestPlain []byte) (pairReply, error) {
	var reply pairReply
	_, e := openMessage(n.cfg.Identity, frame, remote.Peer.Key, &reply)
	if e != nil {
		return reply, e
	}
	if reply.Version != pairingVersion || reply.Domain != replyDomain || reply.Sender != remote.Peer.Key || reply.Recipient != n.PublicKey() || !bytes.Equal(reply.Nonce, req.Nonce) || reply.TokenHash != req.TokenHash || reply.Relay != n.cfg.Relay || !validKey(reply.RoleKey) || reply.RoleKey == reply.Sender || reply.CapabilityHash != digest([]byte(remote.Address)) || reply.PeerCapabilityHash != req.CapabilityHash || reply.RequestHash != digest(requestPlain) {
		return reply, ErrInvite
	}
	return reply, nil
}

// commitPair stages both snapshots and changes no in-memory approval until the
// caller's atomic private-file save succeeds. A published error freezes snapshot
// writes until reopen without activating the uncertain pair. Locks serialize pairing/revocation;
// Persist must not re-enter either Node or Book.
// Outgoing commits require the exact registered attempt; inbound commits require
// a valid invitation token and transport admission.
func (n *Node) commitPair(ctx context.Context, remote RemotePeer, token string, attempt *pairAttempt, liveClient ...*tailcat.Client) error {
	if remote.Routes != nil {
		return ErrRouteUpdate
	}
	if token == "" && attempt == nil {
		return ErrUntrusted
	}
	n.pairMu.Lock()
	defer n.pairMu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pairingRecovery {
		return config.ErrAtomicRecovery
	}
	if token == "" {
		_, registered := n.attempts[remote.Peer.Key][attempt]
		if attempt.peer != remote.Peer.Key || attempt.invalidated || !registered {
			return ErrUntrusted
		}
	}
	if n.cfg.Persist == nil {
		return errors.New("durable pairing persistence required")
	}
	if len(liveClient) > 1 {
		return ErrUntrusted
	}
	if len(liveClient) == 1 && liveClient[0] != nil {
		c := liveClient[0]
		if c.Key.IsZero() || remote.ClientPrivate.IsZero() || keyString(c.Key.Public()) != keyString(remote.ClientPrivate.Public()) || c.Server != remote.Address {
			return ErrUntrusted
		}
	}
	ap, e := validateRemote(remote.Offer(), n.cfg.Relay)
	if e != nil {
		return e
	}
	b := n.cfg.Trust
	b.mu.Lock()
	defer b.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if n.clients[remote.Peer.Key] != nil || b.peers[remote.Peer.Key] != nil {
		return errors.New("peer already paired; revoke before replacing role keys")
	}
	if err := b.admissionErrorLocked(); err != nil {
		return err
	}
	usedKeys := map[string]bool{n.PublicKey(): true}
	for _, r := range n.clients {
		usedKeys[r.remote.Peer.Key] = true
		usedKeys[keyString(r.remote.ClientPrivate.Public())] = true
		usedKeys[r.remote.IncomingClientKey] = true
	}
	if e = distinctRoleKeys(remote, usedKeys); e != nil {
		return e
	}
	var tokenDigest [32]byte
	if token != "" {
		if expiry, ok := n.admissions[remote.IncomingClientKey]; !ok || !deadline.Active(time.Now(), expiry) {
			return ErrUntrusted
		}
		tokenDigest = sha256.Sum256([]byte(token))
		inv, ok := b.invites[tokenDigest]
		if !ok || inv.peer.Key != remote.Peer.Key || inv.relay != n.cfg.Relay.Address || !deadline.Active(time.Now(), inv.expires) {
			return ErrInvite
		}
	}
	snapshot := Snapshot{Version: 1, Peers: make([]Peer, 0, len(b.peers)+1)}
	for _, a := range b.peers {
		snapshot.Peers = append(snapshot.Peers, a.peer)
	}
	snapshot.Peers = append(snapshot.Peers, remote.Peer)
	sort.Slice(snapshot.Peers, func(i, j int) bool { return snapshot.Peers[i].Key < snapshot.Peers[j].Key })
	records := n.remoteSnapshotLocked()
	records = append(records, remote)
	sort.Slice(records, func(i, j int) bool { return records[i].Peer.Key < records[j].Peer.Key })
	if e = n.cfg.Persist(snapshot, records); e != nil {
		if errors.Is(e, config.ErrAtomicCommitted) {
			// Keep the published approval on disk without granting uncertain trust.
			// pairMu makes the latch visible before any other snapshot writer runs.
			n.pairingRecovery = true
		}
		return fmt.Errorf("pairing was not activated because private state could not be saved: %w", e)
	}
	b.generation++
	b.peers[remote.Peer.Key] = &approval{remote.Peer, b.generation, make(map[*trackedFlow]struct{})}
	for h, inv := range b.invites {
		if inv.peer.Key == remote.Peer.Key {
			delete(b.invites, h)
		}
	}
	delete(n.admissions, remote.IncomingClientKey)
	entry := &remoteClient{remote: remote, address: ap, destinationPolicy: n.cfg.DestinationPolicy, wanCandidates: n.cfg.WANCandidates}
	if len(liveClient) == 1 && liveClient[0] != nil {
		entry.client = liveClient[0]
		entry.started = true
		entry.runCtx, entry.cancel = context.WithCancel(context.Background())
	}
	n.clients[remote.Peer.Key] = entry
	return nil
}

func writePairFrame(c io.Writer, data []byte) error {
	if len(data) == 0 || len(data) > maxPairMessage {
		return ErrInvite
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	_, e := io.Copy(c, bytes.NewReader(append(size[:], data...)))
	return e
}
func readPairFrame(c io.Reader) ([]byte, error) {
	var size [4]byte
	if _, e := io.ReadFull(c, size[:]); e != nil {
		return nil, e
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxPairMessage {
		return nil, ErrInvite
	}
	b := make([]byte, n)
	_, e := io.ReadFull(c, b)
	return b, e
}

type PairingServer struct {
	listener net.Listener
	node     *Node
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]string
	slots    chan struct{}
}

func (n *Node) ServePairing(ctx context.Context) (*PairingServer, error) {
	if n.cfg.Persist == nil {
		return nil, errors.New("durable pairing persistence required")
	}
	n.listenMu.Lock()
	defer n.listenMu.Unlock()
	n.mu.Lock()
	closed, existing := n.closed, n.pairing
	n.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	if existing != nil {
		return nil, errors.New("pairing listener already started")
	}
	ln, e := n.server.Listen(ctx, "tcp", fmt.Sprintf(":%d", PairingPort))
	if e != nil {
		return nil, e
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		ln.Close()
		return nil, net.ErrClosed
	}
	run, cancel := context.WithCancel(ctx)
	p := &PairingServer{listener: ln, node: n, ctx: run, cancel: cancel, conns: make(map[net.Conn]string), slots: make(chan struct{}, 8)}
	n.pairing = p
	p.wg.Add(2)
	go p.serve()
	go p.sweep()
	go func() { <-run.Done(); p.Close() }()
	return p, nil
}
func (p *PairingServer) sweep() {
	defer p.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case now := <-ticker.C:
			p.node.pairMu.Lock()
			p.node.mu.Lock()
			var expired []string
			for k, expiry := range p.node.admissions {
				if !deadline.Active(now, expiry) {
					expired = append(expired, k)
					delete(p.node.admissions, k)
				}
			}
			p.node.mu.Unlock()
			for _, s := range expired {
				p.revoke(s)
				if k, e := parseNodePublic(s); e == nil {
					p.node.server.DisconnectClient(k)
				}
			}
			p.node.pairMu.Unlock()
		}
	}
}
func (p *PairingServer) serve() {
	defer p.wg.Done()
	for {
		c, e := p.listener.Accept()
		if e != nil {
			return
		}
		k, ok := p.node.server.PeerKey(c.RemoteAddr())
		if !ok {
			c.Close()
			continue
		}
		select {
		case p.slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			<-p.slots
			c.Close()
			return
		}
		p.conns[c] = keyString(k)
		p.mu.Unlock()
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer func() { c.Close(); p.mu.Lock(); delete(p.conns, c); p.mu.Unlock(); <-p.slots }()
			p.handle(c, keyString(k))
		}()
	}
}
func (p *PairingServer) handle(c net.Conn, transportKey string) {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	requestCtx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	frame, e := readPairFrame(c)
	if e != nil {
		return
	}
	req, peer, plain, e := p.node.readRequest(frame, transportKey)
	if e != nil {
		return
	}
	role := key.NewNode()
	reply, e := p.node.makeReply(req, plain, role)
	if e != nil {
		return
	}
	remote := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: role, IncomingClientKey: transportKey}
	if e = p.node.commitPair(requestCtx, remote, req.Token, nil); e != nil {
		return
	}
	writePairFrame(c, reply)
}
func (p *PairingServer) revoke(peer string) {
	p.mu.Lock()
	var cs []net.Conn
	for c, key := range p.conns {
		if key == peer {
			cs = append(cs, c)
		}
	}
	p.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}
func (p *PairingServer) Close() error {
	p.once.Do(func() {
		p.cancel()
		p.listener.Close()
		p.mu.Lock()
		for c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
	})
	p.wg.Wait()
	return nil
}

// Pair verifies possession of both server identities using the existing
// authenticated key.SealTo/OpenFrom primitive. Each peer then stores distinct
// private outbound and public inbound role keys. Raw assertions are not trusted.
func (n *Node) Pair(ctx context.Context, remote PeerOffer, token string) error {
	return n.pair(ctx, remote, token, false)
}
func (n *Node) pair(ctx context.Context, remote PeerOffer, token string, embedded bool) error {
	if embedded && !embeddedBootstrapEnabled {
		return ErrEmbeddedBootstrap
	}
	if n.cfg.Persist == nil {
		return errors.New("durable pairing persistence required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	ap, e := validateRemote(remote, n.cfg.Relay)
	if e != nil {
		return e
	}
	if _, e = n.cfg.Trust.Epoch(remote.Peer.Key); e == nil {
		return errors.New("peer is already paired")
	}
	if remote.Peer.Key == n.PublicKey() {
		return ErrInvite
	}
	// Reject a known local policy failure before contacting the remote. The
	// durable commit checks admission again after the authenticated handshake.
	if err := n.cfg.Trust.admissionError(); err != nil {
		return err
	}
	n.pairMu.Lock()
	if n.cfg.Trust.pending(remote.Peer.Key) {
		n.pairMu.Unlock()
		return ErrCancelInviteFirst
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	attempt := &pairAttempt{cancel: cancel}
	n.mu.Lock()
	if n.pairingRecovery {
		n.mu.Unlock()
		n.pairMu.Unlock()
		cancel()
		return config.ErrAtomicRecovery
	}
	if n.closed {
		n.mu.Unlock()
		n.pairMu.Unlock()
		cancel()
		return net.ErrClosed
	}
	attempt.peer = remote.Peer.Key
	if n.attempts[remote.Peer.Key] == nil {
		n.attempts[remote.Peer.Key] = make(map[*pairAttempt]struct{})
	}
	n.attempts[remote.Peer.Key][attempt] = struct{}{}
	n.mu.Unlock()
	n.pairMu.Unlock()
	defer func() {
		n.retirePairAttempt(remote.Peer.Key, attempt)
	}()
	role := key.NewNode()
	frame, plain, req, e := n.makeRequest(remote, token, role)
	if e != nil {
		return e
	}
	if embedded {
		if e = postRelayBootstrap(bounded, n.cfg.Relay, frame); e != nil {
			return e
		}
	}
	c := &tailcat.Client{Server: remote.Address, Key: role, PrivateOnly: n.cfg.PrivateOnly, DestinationPrefixes: destinationPrefixes(n.cfg.DestinationPolicy), WANCandidates: n.cfg.WANCandidates, Logf: logger.Discard}
	retained := false
	defer func() {
		if !retained {
			c.Close()
		}
	}()
	stream, e := c.DialTCP(bounded, netip.AddrPortFrom(ap, PairingPort))
	if e != nil {
		return e
	}
	defer stream.Close()
	stop := context.AfterFunc(bounded, func() { stream.Close() })
	defer stop()
	stream.SetDeadline(time.Now().Add(5 * time.Second))
	if e = writePairFrame(stream, frame); e != nil {
		return e
	}
	replyFrame, e := readPairFrame(stream)
	if e != nil {
		return fmt.Errorf("%w: %w", ErrPairReplyUncertain, e)
	}
	e = n.acceptPairReply(bounded, replyFrame, remote, req, plain, role, attempt, c)
	if e == nil {
		retained = true
	}
	return e

}

type pairAttempt struct {
	peer        string
	cancel      context.CancelFunc
	invalidated bool
}

func (n *Node) retirePairAttempt(peer string, attempt *pairAttempt) {
	if attempt.cancel != nil {
		attempt.cancel()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.attempts[peer], attempt)
	if len(n.attempts[peer]) == 0 {
		delete(n.attempts, peer)
	}
}

func (n *Node) acceptPairReply(ctx context.Context, frame []byte, remote PeerOffer, req pairRequest, plain []byte, role key.NodePrivate, attempt *pairAttempt, liveClient ...*tailcat.Client) error {
	if attempt == nil {
		return ErrUntrusted
	}
	reply, e := n.readReply(frame, remote, req, plain)
	if e != nil {
		return e
	}
	record := RemotePeer{Peer: remote.Peer, Address: remote.Address, ClientPrivate: role, IncomingClientKey: reply.RoleKey}
	e = n.commitPair(ctx, record, "", attempt, liveClient...)
	if e != nil {
		return fmt.Errorf("%w: %w", ErrRemotePairedLocalSave, e)
	}
	return nil
}
