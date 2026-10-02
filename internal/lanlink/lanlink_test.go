package lanlink

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cfg() RelayConfig {
	return RelayConfig{netip.MustParseAddrPort("127.0.0.1:54444"), []netip.Prefix{netip.MustParsePrefix("127.0.0.0/24")}, strings.Repeat("a", 64)}
}
func peer() Peer { return Peer{strings.Repeat("b", 64), "test peer"} }
func approve(t *testing.T, b *Book) uint64 {
	t.Helper()
	now := time.Now()
	token, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Redeem(context.Background(), token, peer().Key, cfg().Listen, now); e != nil {
		t.Fatal(e)
	}
	v, e := b.Epoch(peer().Key)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestConfigNoExternal(t *testing.T) {
	c := cfg()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"0.0.0.0:54444", "8.8.8.8:54444", "127.0.0.1:80"} {
		bad := c
		bad.Listen = netip.MustParseAddrPort(s)
		if bad.Validate() == nil {
			t.Fatal(s)
		}
	}
	if c.PermitsUnderlay(netip.MustParseAddrPort("8.8.8.8:443")) {
		t.Fatal("external permitted")
	}
	if !c.PermitsUnderlay(c.Listen) {
		t.Fatal("local denied")
	}
	c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	if c.Validate() == nil {
		t.Fatal("broad prefix accepted")
	}
}
func TestInviteBoundExpiredCancelledOneTime(t *testing.T) {
	b := NewBook()
	now := time.Now()
	issue := func() string {
		s, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := issue()
	if b.Redeem(context.Background(), s, strings.Repeat("c", 64), cfg().Listen, now) == nil {
		t.Fatal("wrong peer")
	}
	if b.Redeem(context.Background(), s, peer().Key, netip.MustParseAddrPort("127.0.0.2:54444"), now) == nil {
		t.Fatal("wrong relay")
	}
	if e := b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now); e != nil {
		t.Fatal(e)
	}
	if b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now) == nil {
		t.Fatal("reused")
	}
	s = issue()
	if b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now.Add(time.Minute)) == nil {
		t.Fatal("expired")
	}
	s = issue()
	b.CancelInvite(s)
	if b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now) == nil {
		t.Fatal("cancelled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := b.Issue(ctx, peer(), cfg(), now, time.Minute); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

type closer struct{ closed atomic.Int64 }

func (c *closer) Close() error { c.closed.Add(1); return nil }
func TestRevokeClosesAndRejectsStaleEpoch(t *testing.T) {
	b := NewBook()
	v := approve(t, b)
	c := new(closer)
	release, e := b.Track(peer().Key, v, c)
	if e != nil {
		t.Fatal(e)
	}
	b.Revoke(peer().Key)
	release()
	if c.closed.Load() != 1 {
		t.Fatal(c.closed.Load())
	}
	approve(t, b)
	stale := new(closer)
	if _, e := b.Track(peer().Key, v, stale); !errors.Is(e, ErrUntrusted) {
		t.Fatal(e)
	}
	if stale.closed.Load() != 1 {
		t.Fatal("late flow leaked")
	}
}
func TestTrustSnapshotNoInvites(t *testing.T) {
	b := NewBook()
	approve(t, b)
	s := b.Snapshot()
	other := NewBook()
	if e := other.Restore(s); e != nil {
		t.Fatal(e)
	}
	if _, e := other.Epoch(peer().Key); e != nil {
		t.Fatal(e)
	}
	if other.Restore(s) == nil {
		t.Fatal("overwrote live book")
	}
	bad := NewBook()
	s.Peers = append(s.Peers, s.Peers[0])
	if bad.Restore(s) == nil {
		t.Fatal("duplicate imported")
	}
}
func TestConcurrentRevokeAdmission(t *testing.T) {
	b := NewBook()
	v := approve(t, b)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := new(closer)
			release, e := b.Track(peer().Key, v, c)
			if e == nil {
				release()
				c.Close()
			}
		}()
	}
	b.Revoke(peer().Key)
	wg.Wait()
	if _, e := b.Epoch(peer().Key); e == nil {
		t.Fatal("still trusted")
	}
}
func TestUnavailableDoesNotDial(t *testing.T) {
	var d DataPlane = Unavailable{}
	if _, e := d.DialPeer(context.Background(), peer().Key, "tcp", 80); !errors.Is(e, ErrBackendUnavailable) {
		t.Fatal(e)
	}
	if _, e := d.DialPacketPeer(context.Background(), peer().Key, 80); !errors.Is(e, ErrBackendUnavailable) {
		t.Fatal(e)
	}
}

var _ io.Closer = (*closer)(nil)

func TestConcurrentInviteRedeemOnce(t *testing.T) {
	b := NewBook()
	now := time.Now()
	s, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var successes atomic.Int64
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
}
func TestRevocationCancelsOutstandingInvite(t *testing.T) {
	b := NewBook()
	now := time.Now()
	s, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	b.Revoke(peer().Key)
	if b.Redeem(context.Background(), s, peer().Key, cfg().Listen, now) == nil {
		t.Fatal("revoked invitation admitted")
	}
}
func TestBoundsAndNameControls(t *testing.T) {
	b := NewBook()
	now := time.Now()
	p := peer()
	p.Name = "name\x1b[31m"
	if _, e := b.Issue(context.Background(), p, cfg(), now, time.Minute); e == nil {
		t.Fatal("control character accepted")
	}
	for range 32 {
		if _, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := b.Issue(context.Background(), peer(), cfg(), now, time.Minute); e == nil {
		t.Fatal("unbounded invitations")
	}
	if _, e := b.Issue(context.Background(), peer(), cfg(), now.Add(time.Minute), time.Minute); e != nil {
		t.Fatal("expired entries not pruned", e)
	}
}
func TestPrefixScopeAndIPv6(t *testing.T) {
	c := cfg()
	c.Listen = netip.MustParseAddrPort("10.2.0.1:54444")
	c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/7")}
	if c.Validate() == nil {
		t.Fatal("prefix extends to public addresses")
	}
	c.Listen = netip.MustParseAddrPort("[fd00::1]:54444")
	c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix("fd00::/64")}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
}
