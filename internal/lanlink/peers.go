package lanlink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"io"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUntrusted = errors.New("peer is not currently approved")
	ErrInvite    = errors.New("invitation is invalid, expired, cancelled or already used")
)

type Peer struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Snapshot contains public trust records only; private node keys, PSKs and
// invitations must never be included. The caller owns secure atomic persistence.
type Snapshot struct {
	Version int    `json:"version"`
	Peers   []Peer `json:"peers"`
}
type trackedFlow struct{ closer io.Closer }

type approval struct {
	peer  Peer
	epoch uint64
	flows map[*trackedFlow]struct{}
}
type invitation struct {
	peer    Peer
	expires time.Time
	relay   netip.AddrPort
}
type Book struct {
	mu         sync.Mutex
	peers      map[string]*approval
	invites    map[[32]byte]invitation
	generation uint64
	peerLimit  func() int64
}

func NewBook() *Book {
	return NewBookWithPeerLimit(func() int64 { return 128 })
}

// NewBookWithPeerLimit uses a live admission policy. The callback must be safe
// during concurrent policy changes and must not call back into Book or Node.
// Existing approvals remain valid when the policy is lowered.
func NewBookWithPeerLimit(limit func() int64) *Book {
	return &Book{peers: make(map[string]*approval), invites: make(map[[32]byte]invitation), peerLimit: limit}
}

type PeerCapacityError struct{ Limit int64 }

func (e *PeerCapacityError) Error() string {
	return fmt.Sprintf("LAN paired-identity policy allows %d peers; raise trustedPeers or revoke an unused pair before pairing", e.Limit)
}
func (*PeerCapacityError) ErrorCode() string { return "peer_capacity" }

func (b *Book) admissionErrorLocked() error {
	limit := int64(128)
	if b.peerLimit != nil {
		limit = b.peerLimit()
	}
	if int64(len(b.peers)) >= limit {
		return &PeerCapacityError{Limit: limit}
	}
	return nil
}

func (b *Book) admissionError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.admissionErrorLocked()
}
func validKey(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	b, e := hex.DecodeString(s)
	if e != nil {
		return false
	}
	for _, v := range b {
		if v != 0 {
			return true
		}
	}
	return false
}
func validPeer(p Peer) bool {
	return validKey(p.Key) && len(p.Name) > 0 && len(p.Name) <= 80 && utf8.ValidString(p.Name) && strings.TrimSpace(p.Name) == p.Name && strings.IndexFunc(p.Name, unicode.IsControl) < 0
}

// Issue binds the invitation to the exact reviewed public key. The transport
// must independently prove possession of that key before Redeem; a name or
// caller-supplied header is not authentication. Invitations are memory-only.
func (b *Book) Issue(ctx context.Context, p Peer, relay RelayConfig, now time.Time, ttl time.Duration) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if !validPeer(p) || relay.Validate() != nil || ttl <= 0 || ttl > 10*time.Minute {
		return "", ErrInvite
	}
	return b.issue(ctx, p, relay.Listen, now, ttl)
}
func (b *Book) IssueTrusted(ctx context.Context, p Peer, relay TrustedRelay, now time.Time, ttl time.Duration) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if !validPeer(p) || relay.Validate() != nil || ttl <= 0 || ttl > 10*time.Minute {
		return "", ErrInvite
	}
	return b.issue(ctx, p, relay.Address, now, ttl)
}
func (b *Book) issue(ctx context.Context, p Peer, relay netip.AddrPort, now time.Time, ttl time.Duration) (string, error) {
	var raw [32]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	h := sha256.Sum256([]byte(token))
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if _, approved := b.peers[p.Key]; !approved {
		if err := b.admissionErrorLocked(); err != nil {
			return "", err
		}
	}
	for k, v := range b.invites {
		if !deadline.Active(now, v.expires) {
			delete(b.invites, k)
		}
	}
	if len(b.invites) >= 32 {
		return "", errors.New("too many pending invitations")
	}
	b.invites[h] = invitation{p, now.Add(ttl), relay}
	return token, nil
}
func (b *Book) CancelInvite(token string) {
	h := sha256.Sum256([]byte(token))
	b.mu.Lock()
	delete(b.invites, h)
	b.mu.Unlock()
}

// Redeem requires the cryptographically authenticated key, not merely the key
// claimed by the request. No network protocol is implemented in this package.
func (b *Book) Redeem(ctx context.Context, token, authenticatedKey string, contactedRelay netip.AddrPort, now time.Time) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if len(token) != 43 {
		return ErrInvite
	}
	h := sha256.Sum256([]byte(token))
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	v, ok := b.invites[h]
	if !ok || !deadline.Active(now, v.expires) || v.peer.Key != authenticatedKey || v.relay != contactedRelay {
		return ErrInvite
	}
	delete(b.invites, h)
	if _, ok := b.peers[authenticatedKey]; ok {
		return nil
	}
	if err := b.admissionErrorLocked(); err != nil {
		return err
	}
	b.generation++
	b.peers[authenticatedKey] = &approval{v.peer, b.generation, make(map[*trackedFlow]struct{})}
	return nil
}
func (b *Book) Epoch(key string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.peers[key]
	if a == nil {
		return 0, ErrUntrusted
	}
	return a.epoch, nil
}

// Track rejects a dial that raced with revocation/re-approval. It closes an
// unapproved flow itself. Release removes the flow from the registry.
func (b *Book) Track(key string, epoch uint64, c io.Closer) (release func(), err error) {
	if c == nil {
		return nil, errors.New("flow required")
	}
	b.mu.Lock()
	a := b.peers[key]
	if a == nil || a.epoch != epoch {
		b.mu.Unlock()
		c.Close()
		return nil, ErrUntrusted
	}
	flow := &trackedFlow{c}
	a.flows[flow] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if current := b.peers[key]; current == a {
				delete(a.flows, flow)
			}
			b.mu.Unlock()
		})
	}, nil
}

// Revoke invalidates admission first and closes registered flows outside the
// lock. It also cancels pending invitations for the revoked key.
func (b *Book) Revoke(key string) {
	b.mu.Lock()
	a := b.peers[key]
	delete(b.peers, key)
	for h, v := range b.invites {
		if v.peer.Key == key {
			delete(b.invites, h)
		}
	}
	var flows []io.Closer
	if a != nil {
		for c := range a.flows {
			flows = append(flows, c.closer)
		}
	}
	b.mu.Unlock()
	for _, c := range flows {
		c.Close()
	}
}
func (b *Book) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := Snapshot{Version: 1, Peers: make([]Peer, 0, len(b.peers))}
	for _, a := range b.peers {
		s.Peers = append(s.Peers, a.peer)
	}
	sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Key < s.Peers[j].Key })
	return s
}

// Restore is for a fresh, stopped Book only. Loading state never resumes flows
// or pending invitations; repeated import cannot silently replace live trust.
// The caller must bound private-state decoding. Previously saved approvals are
// preserved even when the current policy blocks further admission.
func (b *Book) Restore(s Snapshot) error {
	if s.Version != 1 {
		return errors.New("invalid trust snapshot")
	}
	seen := make(map[string]bool)
	for _, p := range s.Peers {
		if !validPeer(p) || seen[p.Key] {
			return errors.New("invalid or duplicate peer")
		}
		seen[p.Key] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.peers) != 0 || len(b.invites) != 0 {
		return errors.New("trust book must be empty before restore")
	}
	for _, p := range s.Peers {
		b.generation++
		b.peers[p.Key] = &approval{p, b.generation, make(map[*trackedFlow]struct{})}
	}
	return nil
}

func (b *Book) pending(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, v := range b.invites {
		if v.peer.Key == key && deadline.Active(now, v.expires) {
			return true
		}
	}
	return false
}
func (b *Book) invitedPeer(token, key string, relay netip.AddrPort, now time.Time) (Peer, error) {
	h := sha256.Sum256([]byte(token))
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.invites[h]
	if !ok || !deadline.Active(now, v.expires) || v.peer.Key != key || v.relay != relay {
		return Peer{}, ErrInvite
	}
	return v.peer, nil
}

// approveVerified is called only after a successful handshake with the pinned
// remote identity. User confirmation of the supplied capability is prerequisite.
func (b *Book) approveVerified(p Peer) error {
	if !validPeer(p) {
		return ErrUntrusted
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.peers[p.Key]; ok {
		return nil
	}
	if err := b.admissionErrorLocked(); err != nil {
		return err
	}
	b.generation++
	b.peers[p.Key] = &approval{p, b.generation, make(map[*trackedFlow]struct{})}
	return nil
}

func (b *Book) anyPending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, v := range b.invites {
		if deadline.Active(now, v.expires) {
			return true
		}
	}
	return false
}
