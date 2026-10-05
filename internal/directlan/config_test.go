package directlan

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testIdentity(n byte) Identity {
	return Identity{Seed: hex.EncodeToString(bytes.Repeat([]byte{n}, ed25519.SeedSize))}
}
func testConfig(n byte) Config {
	return Config{Identity: testIdentity(n), Listen: netip.MustParseAddrPort("127.0.0.1:40400"), AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Persist: func([]Peer) error { return nil }}
}
func TestConfigRejectsEscape(t *testing.T) {
	good := testConfig(1)
	if e := good.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, addr := range []string{"8.8.8.8:40400", "0.0.0.0:40400", "127.0.0.1:443", "[::ffff:127.0.0.1]:40400", "[fe80::1%eth0]:40400", "[ff02::1]:40400", "127.0.0.2:40400", "127.0.0.1:0"} {
		t.Run(addr, func(t *testing.T) {
			c := good
			c.Listen = netip.MustParseAddrPort(addr)
			if c.Validate() == nil {
				t.Fatal("accepted forbidden endpoint")
			}
		})
	}
	for _, prefix := range []string{"0.0.0.0/0", "8.0.0.0/8", "126.0.0.0/7", "10.0.1.1/16", "::/0", "::ffff:127.0.0.0/104"} {
		t.Run(prefix, func(t *testing.T) {
			c := good
			c.AllowedPrefixes = []netip.Prefix{netip.MustParsePrefix(prefix)}
			if c.Validate() == nil {
				t.Fatal("accepted forbidden prefix")
			}
		})
	}
	if good.permits(netip.MustParseAddrPort("127.0.0.2:40000"), false) {
		t.Fatal("source escaped selected prefix")
	}
}
func TestIdentityAndCertificatePin(t *testing.T) {
	id := testIdentity(1)
	if id.Validate() != nil || len(id.PublicKey()) != 64 {
		t.Fatal("identity")
	}
	if (Identity{Seed: strings.Repeat("A", 64)}).Validate() == nil {
		t.Fatal("noncanonical seed")
	}
	cert, e := certificate(id, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	key, e := certificateKey(cert.Certificate, time.Now())
	if e != nil || key != id.PublicKey() {
		t.Fatal(key, e)
	}
	if _, e := certificateKey(cert.Certificate, time.Now().Add(366*24*time.Hour)); e == nil {
		t.Fatal("expired cert")
	}
	if _, e := certificateKey(append(cert.Certificate, cert.Certificate...), time.Now()); e == nil {
		t.Fatal("chain accepted")
	}
	tampered := append([]byte(nil), cert.Certificate[0]...)
	tampered[len(tampered)-1] ^= 1
	if _, e := certificateKey([][]byte{tampered}, time.Now()); e == nil {
		t.Fatal("signature tampering")
	}
}
func TestMutualTLSOverPipe(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct_pin", true: "wrong_pin"}[wrong], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			a.SetDeadline(time.Now().Add(3 * time.Second))
			b.SetDeadline(time.Now().Add(3 * time.Second))
			ca, _ := certificate(testIdentity(1), time.Now())
			cb, _ := certificate(testIdentity(2), time.Now())
			server := tls.Server(a, tlsConfig(ca, "", true))
			pin := testIdentity(1).PublicKey()
			if wrong {
				pin = testIdentity(3).PublicKey()
			}
			client := tls.Client(b, tlsConfig(cb, pin, false))
			done := make(chan error, 1)
			go func() { done <- server.Handshake() }()
			e := client.Handshake()
			if wrong {
				if e == nil {
					t.Fatal("accepted wrong pin")
				}
				b.Close()
				<-done
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			k, e := certificateKey([][]byte{server.ConnectionState().PeerCertificates[0].Raw}, time.Now())
			if e != nil || k != testIdentity(2).PublicKey() {
				t.Fatal("client proof", e)
			}
		})
	}
}

type partialWriter struct{ bytes.Buffer }

func (w *partialWriter) Write(b []byte) (int, error) {
	if len(b) > 2 {
		b = b[:2]
	}
	return w.Buffer.Write(b)
}
func TestFramesBoundedAndAtomic(t *testing.T) {
	var w partialWriter
	for _, b := range [][]byte{nil, []byte("a"), bytes.Repeat([]byte{42}, MaxDatagram)} {
		if e := writeFrame(&w, b, MaxDatagram); e != nil {
			t.Fatal(e)
		}
		got, e := readFrame(&w, MaxDatagram)
		if e != nil || !bytes.Equal(got, b) {
			t.Fatal("frame mismatch", e)
		}
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], MaxDatagram+1)
	if _, e := readFrame(bytes.NewReader(h[:]), MaxDatagram); !errors.Is(e, errFrame) {
		t.Fatal("oversize header")
	}
	if e := writeFrame(io.Discard, make([]byte, MaxDatagram+1), MaxDatagram); !errors.Is(e, errFrame) {
		t.Fatal("oversize write")
	}
	for _, data := range []string{`{"version":1,"ok":true,"ok":false}`, `{"version":1,"ok":true,"unknown":1}`, `{"version":1,"ok":true} {}`, ` {"version":1,"ok":true}`} {
		var r response
		if strictJSON([]byte(data), &r) == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestWireGuardKeysAndExactUnderlayPolicy(t *testing.T) {
	cfg := testConfig(61)
	peer := Peer{Key: testIdentity(62).PublicKey(), TunnelKey: testIdentity(62).TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.1:40401")}
	cfg.Peers = []Peer{peer}
	if e := cfg.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"", strings.Repeat("0", 64), cfg.Identity.TunnelKey()} {
		bad := cfg
		bad.Peers = append([]Peer(nil), cfg.Peers...)
		bad.Peers[0].TunnelKey = key
		if bad.Validate() == nil {
			t.Fatal("unsafe/legacy tunnel key accepted")
		}
	}
	duplicate := peer
	duplicate.Key = testIdentity(63).PublicKey()
	cfg.Peers = append(cfg.Peers, duplicate)
	if cfg.Validate() == nil {
		t.Fatal("duplicate tunnel key")
	}
	bind := &lanBind{cfg: testConfig(61)}
	bind.policy.Store(&bindPolicy{endpoints: map[netip.AddrPort]bool{peer.Endpoint: true}})
	if _, e := bind.ParseEndpoint(peer.Endpoint.String()); e != nil {
		t.Fatal(e)
	}
	for _, address := range []string{"peer.example.test:40401", "8.8.8.8:40401", "127.0.0.1:40402", "[::ffff:127.0.0.1]:40401"} {
		if _, e := bind.ParseEndpoint(address); e == nil {
			t.Fatal("unapproved endpoint", address)
		}
	}
	if bind.SetMark(1) == nil {
		t.Fatal("socket mark allowed")
	}
}

func TestLogicalPeerLimitIsNotFixedAt256(t *testing.T) {
	cfg := testConfig(71)
	for i := 0; i < 300; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("synthetic peer %d", i)))
		id := Identity{Seed: hex.EncodeToString(seed[:])}
		cfg.Peers = append(cfg.Peers, Peer{Key: id.PublicKey(), TunnelKey: id.TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.1:40500")})
	}
	if e := cfg.Validate(); e != nil {
		t.Fatal("extra fixed logical peer ceiling", e)
	}
	cfg.PeerLimit = 299
	if !errors.Is(cfg.Validate(), ErrCapacity) {
		t.Fatal("explicit peer limit ignored")
	}
}
func TestRuntimeResourceSelectionsAndLivePeerAdmission(t *testing.T) {
	cfg := testConfig(72)
	cfg.FlowLimit = 11
	cfg.InvitationLimit = 2
	cfg.ListenerLimit = 1
	cfg.PacketQueueLimit = 3
	var limit atomic.Int64
	cfg.PeerLimitCurrent = func() int { return int(limit.Load()) }
	n, e := NewNode(cfg)
	if e != nil {
		t.Fatal(e)
	}
	n.started = true
	defer n.Close()
	if cap(n.dispatchSlots) != 11 {
		t.Fatal("flow selection ignored")
	}
	first, e := n.ListenPeer(context.Background(), "tcp", 40600)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	if _, e = n.ListenPeer(context.Background(), "tcp", 40601); !errors.Is(e, ErrCapacity) {
		t.Fatal("listener resource ignored", e)
	}
	n.mu.Lock()
	p := Peer{Key: testIdentity(73).PublicKey(), TunnelKey: testIdentity(73).TunnelKey(), Endpoint: netip.MustParseAddrPort("127.0.0.1:40501")}
	n.peers[p.Key] = &peerState{p}
	n.mu.Unlock()
	limit.Store(1)
	if _, e = n.IssueInvitation(context.Background(), Peer{Key: testIdentity(74).PublicKey()}, time.Minute); !errors.Is(e, ErrCapacity) {
		t.Fatal("live logical limit ignored", e)
	}
	limit.Store(0)
	if _, e = n.IssueInvitation(context.Background(), Peer{Key: testIdentity(74).PublicKey()}, time.Minute); e != nil {
		t.Fatal("unlimited logical admission remained capped", e)
	}
}

func TestPrefixMetadataHasNoFixedEightEntryCeiling(t *testing.T) {
	cfg := testConfig(83)
	for i := 0; i < 9; i++ {
		cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, netip.MustParsePrefix(fmt.Sprintf("10.80.%d.0/24", i)))
	}
	if e := cfg.Validate(); e != nil {
		t.Fatal("valid prefix metadata retained fixed eight-entry ceiling", e)
	}
	cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, netip.MustParsePrefix("::1/128"))
	cfg.Peers = []Peer{{Key: testIdentity(84).PublicKey(), TunnelKey: testIdentity(84).TunnelKey(), Endpoint: netip.MustParseAddrPort("[::1]:40401")}}
	if e := cfg.Validate(); e == nil {
		t.Fatal("IPv4 bound endpoint accepted IPv6 peer")
	}
	cfg.Listen = netip.MustParseAddrPort("[::1]:40400")
	cfg.Peers[0].Endpoint = netip.MustParseAddrPort("127.0.0.1:40401")
	if e := cfg.Validate(); e == nil {
		t.Fatal("IPv6 bound endpoint accepted IPv4 peer")
	}
}
