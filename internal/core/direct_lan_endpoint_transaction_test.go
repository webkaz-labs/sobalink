package core

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
	"github.com/webkaz-labs/sobalink/internal/transportorigin"
)

// Explicit fake-old-owner boundary: no Node, runtimeGeneration, sockets,
// traffic, application bytes or replacement transport is constructed here.
// The fake records only coordinator ordering and lock-release behavior.
type inertEndpointOldOwner struct {
	token         *transportorigin.Token
	began, waited bool
	onWait        func() error
}

func (o *inertEndpointOldOwner) identity() *transportorigin.Token { return o.token }
func (o *inertEndpointOldOwner) begin() error                     { o.began = true; return nil }
func (o *inertEndpointOldOwner) wait(context.Context) error {
	o.waited = true
	if o.onWait != nil {
		return o.onWait()
	}
	return nil
}
func endpointTransactionFixture(t *testing.T) (*Core, *directLANStore, *directLANBackend, *inertEndpointOldOwner, endpointmeta.Mutation, time.Time) {
	t.Helper()
	c, s, key := localEndpointExportFixture(t, nil)
	// Receipt comes only from the actual successful publisher, never a literal
	// fabricated receipt or reopening a file. Setup is a same-state private save.
	c.op.Lock()
	s.mu.Lock()
	if err := s.writeContextPublicationLocked(c.lanStartNonce, cloneDirectLANState(s.state), &contextSaveLiveness{ctx: c.ctx}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	c.op.Unlock()
	now := time.Now()
	state := s.copy()
	proof := managedProjectionProof(t, &state, false, "set")
	digest, err := proof.Digest()
	if err != nil {
		t.Fatal(err)
	}
	approval := &endpointmeta.Approval{Kind: "exact", ProofDigest: digest, Endpoint: proof.Update.Endpoint, Granted: now.UTC().Format(time.RFC3339Nano), Lifetime: "finite", Expires: proof.Update.Expires}
	mutation, outcome, err := endpointmeta.ProposeReceive(*state.Metadata, key, proof, approval, now)
	if err != nil || outcome != "candidate" {
		t.Fatal(outcome, err)
	}
	b := &directLANBackend{store: s, ctx: c.ctx, ready: true} // no embedded Node
	c.node = b
	old := &inertEndpointOldOwner{token: transportorigin.NewToken()}
	return c, s, b, old, mutation, now
}

func TestEndpointTransactionDurableReceiptAndUnlockedJoin(t *testing.T) {
	c, s, b, old, m, now := endpointTransactionFixture(t)
	previous := s.contextPublication
	writes := 0
	s.write = func(path string, data []byte) error { writes++; return config.AtomicWritePrivate(path, data) }
	old.onWait = func() error {
		if !old.began || writes != 1 || s.state.Metadata.PendingChange == nil || s.contextPublication != nil {
			t.Fatal("join did not follow durable fence and receipt invalidation")
		}
		if !c.op.TryLock() {
			t.Fatal("Core.op held across join")
		}
		c.op.Unlock()
		if !s.mu.TryLock() {
			t.Fatal("store.mu held across join")
		}
		s.mu.Unlock()
		if !c.mu.TryLock() {
			t.Fatal("Core.mu held across join")
		}
		c.mu.Unlock()
		if !b.mu.TryLock() {
			t.Fatal("backend.mu held across join")
		}
		b.mu.Unlock()
		// Competing whole-file writes cannot publish during the unlocked interval.
		s.mu.Lock()
		err := s.writeStateLocked(cloneDirectLANState(s.state))
		s.mu.Unlock()
		if !errors.Is(err, endpointmeta.ErrReview) || writes != 1 {
			t.Fatal("competing writer escaped slot", err)
		}
		return nil
	}
	tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time { return now })
	if err != nil || tx == nil || !tx.joined || !tx.durable || writes != 2 || s.state.Metadata.PendingChange != nil || tx.receipt == nil || tx.receipt == previous || tx.receipt != s.contextPublication {
		t.Fatal("durable result", err)
	}
	if b.endpointTransaction != tx || s.endpointTransaction != tx {
		t.Fatal("slot released without staged publication")
	}
	reopened, err := readDirectLANStore(s.path, s.bytes, s.peers)
	if err != nil || reopened.contextPublication != nil {
		t.Fatal("read minted receipt", err)
	}
	c.op.Lock()
	s.mu.Lock()
	err = c.endpointTransactionCurrentLocked(tx, now)
	s.mu.Unlock()
	c.op.Unlock()
	if err != nil {
		t.Fatal("exact saved transaction rejected", err)
	}
}

func TestEndpointTransactionRejectsBeforeOldOwner(t *testing.T) {
	for _, guard := range []string{"binding", "proof", "highwater", "approval", "receipt", "capacity", "cancel"} {
		t.Run(guard, func(t *testing.T) {
			c, s, b, old, m, now := endpointTransactionFixture(t)
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()
			switch guard {
			case "binding":
				m.PairBinding = "invalid"
			case "proof":
				m.State.ReceivedProof.Signature = "invalid"
			case "highwater":
				m.State.ReceivedHighwater = "2"
			case "approval":
				m.State.Approval = nil
			case "receipt":
				s.contextPublication = nil
			case "capacity":
				limits := *s.currentCapacity()
				limits.bytes = 1
				s.limits.Store(&limits)
				if s.currentCapacity().bytes != 1 {
					t.Fatal("fixture did not select the one-byte budget")
				}
			case "cancel":
				cancel()
			}
			before := offlineEndpointRead(t, s.path)
			writes := 0
			s.write = func(string, []byte) error { writes++; return errors.New("unexpected write") }
			tx, err := c.saveEndpointTransactionWithOwner(ctx, b, m, old, func() time.Time { return now })
			if err == nil || tx != nil || old.began || old.waited || writes != 0 || b.endpointTransaction != nil || s.endpointTransaction != nil || string(before) != string(offlineEndpointRead(t, s.path)) {
				t.Fatal("invalid proposal disrupted old owner", err)
			}
		})
	}
}

func TestEndpointTransactionRetainsFailedOrCancelledJoin(t *testing.T) {
	for _, guard := range []string{"cleanup", "cancel", "stop", "root", "capacity-pointer", "file", "monotonic"} {
		t.Run(guard, func(t *testing.T) {
			c, s, b, old, m, now := endpointTransactionFixture(t)
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()
			old.onWait = func() error {
				switch guard {
				case "cleanup":
					return errors.New("synthetic retained cleanup failure")
				case "cancel":
					cancel()
				case "stop":
					b.endpointStopped.Store(true)
				case "root":
					c.node = &directLANBackend{store: s, ctx: c.ctx}
				case "capacity-pointer":
					copy := *s.currentCapacity()
					s.limits.Store(&copy)
				case "file":
					return config.AtomicWritePrivate(s.path, append(offlineEndpointRead(t, s.path), '\n'))
				case "monotonic":
					for key, bound := range s.endpointDeadlines {
						bound.monotonic = now.Add(-time.Second)
						s.endpointDeadlines[key] = bound
					}
				}
				return nil
			}
			tx, err := c.saveEndpointTransactionWithOwner(ctx, b, m, old, func() time.Time { return now })
			if err == nil || tx == nil || tx.failure == nil || tx.durable || s.contextPublication != nil || s.state.Metadata.PendingChange == nil || s.endpointTransaction != tx || b.endpointTransaction != tx {
				t.Fatal("failure released retained transaction", err)
			}
			if _, err := c.saveEndpointTransactionWithOwner(ctx, b, m, old, func() time.Time { return now }); err == nil {
				t.Fatal("second transaction entered retained slot")
			}
		})
	}
}

func TestEndpointTransactionUncertainPublicationHasNoReceipt(t *testing.T) {
	for _, phase := range []int{1, 2} {
		for _, published := range []bool{false, true} {
			t.Run(string(rune('0'+phase))+map[bool]string{false: "not-published", true: "published"}[published], func(t *testing.T) {
				c, s, b, old, m, now := endpointTransactionFixture(t)
				writes := 0
				s.write = func(path string, data []byte) error {
					writes++
					if writes == phase {
						if published {
							if err := config.AtomicWritePrivate(path, data); err != nil {
								return err
							}
							return config.ErrAtomicCommitted
						}
						return errors.New("synthetic prepublication failure")
					}
					return config.AtomicWritePrivate(path, data)
				}
				tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time { return now })
				if err == nil || tx == nil || tx.durable || tx.receipt != nil || s.contextPublication != nil || !s.recovery || s.endpointTransaction != tx {
					t.Fatal("uncertainty certified authority", err)
				}
				finalAdopted := s.state.Metadata.PendingChange == nil && s.state.Metadata.Peers[0].EndpointState.ReceivedHighwater == "1"
				if finalAdopted != (phase == 2 && published) {
					t.Fatal("atomic adoption outcome changed")
				}
			})
		}
	}
}

func TestEndpointTransactionPreservesFirstDeadline(t *testing.T) {
	c, s, b, old, m, now := endpointTransactionFixture(t)
	var first map[directLANEndpointDeadlineKey]directLANEndpointDeadline
	old.onWait = func() error {
		first = make(map[directLANEndpointDeadlineKey]directLANEndpointDeadline)
		for key, bound := range s.endpointDeadlines {
			first[key] = bound
		}
		return nil
	}
	observations := 0
	tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, m, old, func() time.Time {
		observations++
		if observations == 1 {
			return now
		}
		return now.Add(time.Second)
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, bound := range tx.deadlines {
		if !bound.monotonic.Equal(first[key].monotonic) || !bound.monotonic.Equal(s.endpointDeadlines[key].monotonic) {
			t.Fatal("deadline refreshed")
		}
	}
	if len(tx.deadlines) != 2 {
		t.Fatal("proof and exact approval bounds missing")
	}
}

// An unrelated expired or terminal record is retained evidence, not positive
// runtime authority. Its elapsed proof deadline cannot deny a valid target.
func TestEndpointTransactionIgnoresInactiveRetainedDeadlines(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "inactive", true: "terminal"}[terminal], func(t *testing.T) {
			c, s, b, old, mutation, now := endpointTransactionFixture(t)
			state := s.copy()
			remote := directlan.Identity{Seed: strings.Repeat("03", 32)}
			pair := *state.Metadata.Peers[0].PairContext
			pair.JoinerKey, pair.JoinerTunnelKey, pair.JoinerEndpoint = remote.PublicKey(), remote.TunnelKey(), "127.0.0.3:22003"
			initial, err := endpointmeta.InitialState(pair, state.Identity.PublicKey())
			if err != nil {
				t.Fatal(err)
			}
			seed, err := hex.DecodeString(remote.Seed)
			if err != nil {
				t.Fatal(err)
			}
			body := mutation.State.ReceivedProof.Update
			body.Issuer, body.IssuerTunnelKey, body.PairBinding = remote.PublicKey(), remote.TunnelKey(), initial.PairBinding
			body.PriorEndpoint, body.Endpoint = pair.JoinerEndpoint, pair.JoinerEndpoint
			body.Issued, body.Expires = now.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano), now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			proof, err := endpointmeta.Sign(body, ed25519.NewKeyFromSeed(seed))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := proof.Digest()
			if err != nil {
				t.Fatal(err)
			}
			initial.ReceivedVersion, initial.ReceivedHighwater, initial.ReceivedProof, initial.AuthorityRevision = 1, "1", &proof, "1"
			initial.ReceiveStatus, initial.LastEndpoint = "eligible", pair.JoinerEndpoint
			initial.Approval = &endpointmeta.Approval{Kind: "exact", ProofDigest: digest, Endpoint: body.Endpoint, Granted: body.Issued, Lifetime: "finite", Expires: body.Expires}
			peer := directlan.Peer{Key: remote.PublicKey(), TunnelKey: remote.TunnelKey(), Name: "synthetic-retained", Endpoint: netip.MustParseAddrPort(pair.JoinerEndpoint)}
			record := endpointmeta.PeerRecord{Peer: directLANPeerWire(peer), Revision: "1", PairContext: &pair, ContextConfirmed: true, EndpointState: &initial}
			if terminal {
				state.Version, state.Metadata.Version = 4, 4
				record.PairRevocation = &endpointmeta.PairRevocation{PairBinding: initial.PairBinding, Revision: "1", RevokedAt: state.Metadata.ObservedAt}
			}
			state.Peers = append(state.Peers, peer)
			state.Metadata.Peers = append(state.Metadata.Peers, record)
			c.op.Lock()
			s.mu.Lock()
			err = s.writeContextPublicationLocked(c.lanStartNonce, state, &contextSaveLiveness{ctx: c.ctx})
			s.mu.Unlock()
			c.op.Unlock()
			if err != nil {
				t.Fatal("synthetic retained record", err)
			}
			tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, mutation, old, func() time.Time { return now })
			if err != nil || tx == nil || !tx.durable {
				t.Fatal("unrelated retained deadline denied target", err)
			}
			if !reflect.DeepEqual(s.state.Metadata.Peers[1], record) {
				t.Fatal("retained evidence changed")
			}
			for key := range tx.deadlines {
				if key.binding == initial.PairBinding {
					t.Fatal("inactive record captured as runtime authority")
				}
			}
		})
	}
}

func TestEndpointTransactionRechecksTimeAtDisruptionAndResult(t *testing.T) {
	for _, phase := range []string{"pre-signal", "final-write"} {
		t.Run(phase, func(t *testing.T) {
			c, s, b, old, mutation, now := endpointTransactionFixture(t)
			expires, err := time.Parse(time.RFC3339Nano, mutation.State.ReceivedProof.Update.Expires)
			if err != nil {
				t.Fatal(err)
			}
			at := now
			calls, writes := 0, 0
			s.write = func(path string, data []byte) error {
				writes++
				if err := config.AtomicWritePrivate(path, data); err != nil {
					return err
				}
				if phase == "final-write" && writes == 2 {
					at = expires.Add(time.Second)
				}
				return nil
			}
			tx, err := c.saveEndpointTransactionWithOwner(c.ctx, b, mutation, old, func() time.Time {
				calls++
				if phase == "pre-signal" && calls == 2 {
					at = expires.Add(time.Second)
				}
				return at
			})
			if err == nil {
				t.Fatal("expired input accepted using stale observation")
			}
			if phase == "pre-signal" {
				if tx != nil || old.began || writes != 0 || b.endpointTransaction != nil || s.endpointTransaction != nil {
					t.Fatal("expired preflight disrupted old owner")
				}
			} else {
				if tx == nil || !tx.durable || tx.failure == nil || tx.receipt == nil || s.contextPublication != tx.receipt || s.state.Metadata.PendingChange != nil || b.endpointTransaction != tx {
					t.Fatal("durable expiry was rolled back or treated as current")
				}
			}
		})
	}
}
