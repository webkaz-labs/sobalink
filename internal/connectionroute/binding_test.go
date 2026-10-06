package connectionroute

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestBindingRequiresBothAuthenticatedIdentities(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	ids := []TransportIdentity{{"tailnet", "n-synthetic"}, {"lan", "synthetic-public-key"}}
	c, e := NewChallenge(pub, ids, now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := Sign(c.Claim(), priv)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Observe(TransportIdentity{"tailnet", "same-display-name"}, proof, now); e == nil {
		t.Fatal("display-name substitution accepted")
	}
	if e = c.Observe(ids[0], proof, now); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Finish(now); e == nil {
		t.Fatal("single transport proved two routes")
	}
	if e = c.Observe(ids[1], proof, now); e != nil {
		t.Fatal(e)
	}
	b, e := c.Finish(now)
	if e != nil || b.PeerID == "" {
		t.Fatal(b, e)
	}
	if _, e = c.Finish(now); e == nil {
		t.Fatal("challenge reused")
	}
	other, _ := NewChallenge(pub, ids, now, time.Minute)
	if e = other.Observe(ids[0], proof, now); e == nil {
		t.Fatal("proof replayed across nonce")
	}
}
func TestBindingExpiryAndDetachedClaim(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	ids := []TransportIdentity{{"tailnet", "n-synthetic"}, {"direct-lan", "synthetic-public-key"}}
	c, _ := NewChallenge(pub, ids, now, time.Minute)
	v := c.Claim()
	v.Nonce[0] ^= 1
	proof, _ := Sign(v, priv)
	if c.Observe(ids[0], proof, now) == nil {
		t.Fatal("mutable claim changed challenge")
	}
	proof, _ = Sign(c.Claim(), priv)
	if c.Observe(ids[0], proof, now.Add(time.Minute)) == nil {
		t.Fatal("expiry accepted")
	}
	if _, e := NewChallenge(pub, []TransportIdentity{ids[0], ids[0]}, now, time.Minute); e == nil {
		t.Fatal("duplicate backend accepted")
	}
}
