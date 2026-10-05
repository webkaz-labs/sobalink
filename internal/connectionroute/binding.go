// Package connectionroute binds authenticated transport identities to stable
// application peers and selects explicitly permitted routes for new flows.
package connectionroute

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrBinding = errors.New("transport identity binding is invalid, expired or unverified")

// TransportIdentity is an identity returned by the authenticated backend, never
// a display name, DNS name or IP address supplied by the peer.
type TransportIdentity struct {
	Backend string `json:"backend"`
	ID      string `json:"id"`
}

func (i TransportIdentity) valid() bool {
	switch i.Backend {
	case "tailnet", "lan", "direct-lan":
	default:
		return false
	}
	return i.ID != "" && len(i.ID) <= 512 && strings.TrimSpace(i.ID) == i.ID && !strings.ContainsRune(i.ID, 0)
}

// Claim is signed separately on every authenticated transport. The fresh nonce
// belongs to the verifier, preventing copied signed claims from proving access.
type Claim struct {
	Version    int                 `json:"version"`
	PublicKey  []byte              `json:"publicKey"`
	Nonce      []byte              `json:"nonce"`
	Expires    time.Time           `json:"expires"`
	Identities []TransportIdentity `json:"identities"`
}
type Proof struct {
	Claim     Claim  `json:"claim"`
	Signature []byte `json:"signature"`
}

func canonicalClaim(c Claim) ([]byte, error) {
	if c.Version != 1 || len(c.PublicKey) != ed25519.PublicKeySize || len(c.Nonce) != 32 || c.Expires.IsZero() || len(c.Identities) < 2 || len(c.Identities) > 3 {
		return nil, ErrBinding
	}
	c.Identities = append([]TransportIdentity(nil), c.Identities...)
	sort.Slice(c.Identities, func(i, j int) bool { return c.Identities[i].Backend < c.Identities[j].Backend })
	for i, v := range c.Identities {
		if !v.valid() || i > 0 && c.Identities[i-1].Backend == v.Backend {
			return nil, ErrBinding
		}
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, ErrBinding
	}
	return append([]byte("sobalink.transport-binding.v1\x00"), raw...), nil
}
func Sign(c Claim, private ed25519.PrivateKey) (Proof, error) {
	if len(private) != ed25519.PrivateKeySize || !bytes.Equal(private.Public().(ed25519.PublicKey), c.PublicKey) {
		return Proof{}, ErrBinding
	}
	raw, e := canonicalClaim(c)
	if e != nil {
		return Proof{}, e
	}
	return Proof{c, ed25519.Sign(private, raw)}, nil
}
func StablePeerID(public ed25519.PublicKey) (string, error) {
	if len(public) != ed25519.PublicKeySize {
		return "", ErrBinding
	}
	sum := sha256.Sum256(public)
	return "peer:" + hex.EncodeToString(sum[:]), nil
}

// Binding may be persisted only after the caller has reviewed the exact mapping.
// A verified binding is identity evidence, not a resource or route permission.
type Binding struct {
	PeerID     string              `json:"peerId"`
	PublicKey  []byte              `json:"publicKey"`
	Identities []TransportIdentity `json:"identities"`
}

// Challenge is a bounded, single-use verification session. Each Observe call
// must follow backend WhoIs (or its cryptographic equivalent) on that connection.
// Both peers independently run this protocol for mutual binding.
type Challenge struct {
	mu       sync.Mutex
	claim    Claim
	observed map[string]bool
	used     bool
}

func NewChallenge(public ed25519.PublicKey, identities []TransportIdentity, now time.Time, lifetime time.Duration) (*Challenge, error) {
	if lifetime <= 0 || lifetime > 5*time.Minute {
		return nil, ErrBinding
	}
	c := Claim{Version: 1, PublicKey: append([]byte(nil), public...), Nonce: make([]byte, 32), Expires: now.Add(lifetime), Identities: append([]TransportIdentity(nil), identities...)}
	if _, e := rand.Read(c.Nonce); e != nil {
		return nil, e
	}
	if _, e := canonicalClaim(c); e != nil {
		return nil, e
	}
	return &Challenge{claim: c, observed: make(map[string]bool)}, nil
}
func (c *Challenge) Claim() Claim {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.claim
	v.PublicKey = append([]byte(nil), v.PublicKey...)
	v.Nonce = append([]byte(nil), v.Nonce...)
	v.Identities = append([]TransportIdentity(nil), v.Identities...)
	return v
}
func (c *Challenge) Observe(observed TransportIdentity, proof Proof, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used || !now.Before(c.claim.Expires) || !observed.valid() {
		return ErrBinding
	}
	want, e := canonicalClaim(c.claim)
	if e != nil {
		return e
	}
	got, e := canonicalClaim(proof.Claim)
	if e != nil || !bytes.Equal(want, got) || !ed25519.Verify(c.claim.PublicKey, got, proof.Signature) {
		return ErrBinding
	}
	found := false
	for _, i := range c.claim.Identities {
		if i == observed {
			found = true
			break
		}
	}
	if !found {
		return ErrBinding
	}
	c.observed[observed.Backend] = true
	return nil
}
func (c *Challenge) Finish(now time.Time) (Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used || !now.Before(c.claim.Expires) || len(c.observed) != len(c.claim.Identities) {
		return Binding{}, ErrBinding
	}
	c.used = true
	id, e := StablePeerID(c.claim.PublicKey)
	if e != nil {
		return Binding{}, e
	}
	return Binding{id, append([]byte(nil), c.claim.PublicKey...), append([]TransportIdentity(nil), c.claim.Identities...)}, nil
}
