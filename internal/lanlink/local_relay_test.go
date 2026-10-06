package lanlink

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestLocalRelayDoneTracksEitherServingLoop(t *testing.T) {
	for _, ending := range []string{"relay", "admission", "context", "already-canceled"} {
		t.Run(ending, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listener, admission := newFakeListener(), newFakeListener()
			relay := &LocalRelay{derp: derpserver.New(key.NewNode(), logger.Discard), server: &http.Server{}, admission: &http.Server{}, listener: listener, admissionListener: admission, done: make(chan struct{})}
			t.Cleanup(func() { relay.Close() })
			select {
			case <-relay.Done():
				t.Fatal("unstarted relay already terminated")
			default:
			}
			if ending == "already-canceled" {
				cancel()
			}
			relay.serve(ctx)
			switch ending {
			case "relay":
				listener.Close()
			case "admission":
				admission.Close()
			case "context":
				cancel()
			}
			select {
			case <-relay.Done():
			case <-time.After(time.Second):
				t.Fatal("serving termination was not observable")
			}
			if (ending == "relay" || ending == "admission") && ctx.Err() != nil {
				t.Fatal("relay termination canceled its owner")
			}
			for _, closed := range []<-chan struct{}{listener.closed, admission.closed} {
				select {
				case <-closed:
				default:
					t.Fatal("owned listener survived termination")
				}
			}
			joined := make(chan struct{})
			go func() { relay.Close(); relay.Close(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("serving loops did not join on repeated Close")
			}
		})
	}
}

func TestRelayCertificatePinnedToSelectedIP(t *testing.T) {
	ip := netip.MustParseAddr("127.0.0.1")
	identity, e := GenerateRelayIdentity(ip)
	if e != nil {
		t.Fatal(e)
	}
	cert, pin, e := identity.Certificate(ip)
	if e != nil || len(cert.Certificate) != 1 || !validKey(pin) {
		t.Fatal("certificate validation", e)
	}
	if _, _, e = identity.Certificate(netip.MustParseAddr("127.0.0.2")); e == nil {
		t.Fatal("wrong SAN accepted")
	}
	if _, e = GenerateRelayIdentity(netip.MustParseAddr("8.8.8.8")); e == nil {
		t.Fatal("public listener identity accepted")
	}
	endpoint, e := identity.Endpoint(netip.MustParseAddrPort("127.0.0.1:54546"))
	if e != nil || endpoint.CertificateSHA256 != pin {
		t.Fatal(e)
	}
}
func TestRelayAdmissionPolicyNoSocket(t *testing.T) {
	allowed := key.NewNode().Public()
	h := admissionHandler("/admit/secret", func(s string) bool { return s == keyString(allowed) })
	for _, pub := range []key.NodePublic{allowed, key.NewNode().Public()} {
		body, _ := json.Marshal(tailcfg.DERPAdmitClientRequest{NodePublic: pub, Source: netip.MustParseAddr("127.0.0.1")})
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/admit/secret", bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response tailcfg.DERPAdmitClientResponse
		if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
			t.Fatal(e)
		}
		if response.Allow != (pub == allowed) {
			t.Fatal("admission mismatch")
		}
	}
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/admit/secret", bytes.NewReader(make([]byte, 4097))))
	if bad.Code != http.StatusBadRequest {
		t.Fatal("oversize body accepted")
	}
	bad = httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/admit/secret", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatal("unexpected method accepted")
	}
}

func TestPendingOverlayAdmissionDoesNotOpenRelayToUnknownKeys(t *testing.T) {
	host, client, _, _, _, _, _ := pairFixture(t)
	unknown := key.NewNode().Public()
	if !host.allowClient(unknown) {
		t.Fatal("overlay pairing fixture unexpectedly denied")
	}
	if host.AllowRelayKey(keyString(unknown)) {
		t.Fatal("unknown DERP key inherited overlay exception")
	}
	if !host.AllowRelayKey(client.PublicKey()) || !host.AllowRelayKey(host.PublicKey()) {
		t.Fatal("known server key denied")
	}
}

func TestRelayBootstrapRequiresAuthenticatedInvitation(t *testing.T) {
	host, client, inv, role, frame, _, _ := pairFixture(t)
	roleID := keyString(role.Public())
	if host.AllowRelayKey(roleID) {
		t.Fatal("unproved role admitted")
	}
	if e := host.AuthorizeRelayBootstrap(frame); e != nil {
		t.Fatal(e)
	}
	if !host.AllowRelayKey(roleID) {
		t.Fatal("proved pending role denied")
	}
	other := key.NewNode()
	otherFrame, _, _, e := client.makeRequest(inv.Host, inv.Token, other)
	if e != nil {
		t.Fatal(e)
	}
	if host.AuthorizeRelayBootstrap(otherFrame) == nil {
		t.Fatal("one invitation admitted multiple roles")
	}
	host.CancelInvitation(inv.Token)
	if host.AllowRelayKey(roleID) {
		t.Fatal("cancelled bootstrap still admitted")
	}
	unknown := key.NewNode().Public()
	if host.AllowRelayKey(keyString(unknown)) {
		t.Fatal("arbitrary relay key admitted")
	}
}
func TestBootstrapHTTPIsBoundedAndDoesNotGrantApplicationTrust(t *testing.T) {
	host, client, _, role, frame, _, _ := pairFixture(t)
	h := relayBootstrapHandler(host.AuthorizeRelayBootstrap)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "https://127.0.0.1/sobalink/pair/bootstrap", bytes.NewReader(frame)))
	if w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if _, e := host.cfg.Trust.Epoch(client.PublicKey()); e == nil {
		t.Fatal("bootstrap granted application trust")
	}
	if !host.AllowRelayKey(keyString(role.Public())) {
		t.Fatal("expected temporary relay admission")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "https://127.0.0.1/sobalink/pair/bootstrap", bytes.NewReader(make([]byte, maxPairMessage+1))))
	if w.Code != http.StatusForbidden {
		t.Fatal("oversize bootstrap accepted")
	}
}
func TestRelayLeaseClosesTransportAndReleasesSlot(t *testing.T) {
	ln := newFakeListener()
	limited := &limitedRelayListener{Listener: ln, slots: make(chan struct{}, 1), lease: 10 * time.Millisecond}
	raw := newFakeDatagrams(6200)
	ln.accept <- raw
	conn, e := limited.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	select {
	case <-raw.closed:
	case <-time.After(time.Second):
		t.Fatal("relay transport lease did not expire")
	}
	conn.Close() // Join the once-only release after observing the underlying close.
	if len(limited.slots) != 0 {
		t.Fatal("relay slot leaked")
	}
}

func TestBootstrapPinnedTLSWithoutOSSockets(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-pin", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			ip := netip.MustParseAddr("127.0.0.1")
			identity, e := GenerateRelayIdentity(ip)
			if e != nil {
				t.Fatal(e)
			}
			cert, pin, e := identity.Certificate(ip)
			if e != nil {
				t.Fatal(e)
			}
			endpoint := TrustedRelay{Address: netip.MustParseAddrPort("127.0.0.1:54546"), CertificateSHA256: pin}
			if mode == "wrong-pin" {
				endpoint.CertificateSHA256 = strings.Repeat("f", 64)
			}
			ln := newFakeListener()
			var requests atomic.Int64
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "redirect" {
					w.Header().Set("Location", "https://example.invalid/")
					w.WriteHeader(http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}), ErrorLog: log.New(io.Discard, "", 0)}
			done := make(chan struct{})
			go func() { defer close(done); server.Serve(ln) }()
			t.Cleanup(func() { server.Close(); ln.Close(); <-done })
			var dials atomic.Int64
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				if address != endpoint.Address.String() {
					t.Error("unexpected address")
				}
				left, right := net.Pipe()
				ln.accept <- tls.Server(right, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
				return left, nil
			}
			e = relayBootstrapRoundTrip(context.Background(), endpoint, []byte("sealed proof"), dial)
			if mode == "valid" && e != nil {
				t.Fatal(e)
			}
			if mode != "valid" && e == nil {
				t.Fatal("invalid TLS/redirect accepted")
			}
			if dials.Load() != 1 {
				t.Fatal("unexpected fallback dial")
			}
			if mode == "wrong-pin" && requests.Load() != 0 {
				t.Fatal("proof sent before TLS pin verification")
			}
		})
	}
}

func TestRelayOldRolesNotReapprovedByCanonicalKeyReuse(t *testing.T) {
	host, client, _, role, frame, _, _ := pairFixture(t)
	req, peer, _, e := host.readRequest(frame, keyString(role.Public()))
	if e != nil {
		t.Fatal(e)
	}
	oldOutbound := key.NewNode()
	old := RemotePeer{Peer: peer, Address: req.Address, ClientPrivate: oldOutbound, IncomingClientKey: req.RoleKey}
	if e = host.commitPair(context.Background(), old, req.Token, nil); e != nil {
		t.Fatal(e)
	}
	if !host.AllowRelayKey(req.RoleKey) || !host.AllowRelayKey(keyString(oldOutbound.Public())) {
		t.Fatal("old pair not admitted before revoke")
	}
	if e = host.Revoke(client.PublicKey()); e != nil {
		t.Fatal(e)
	}
	replacement := old
	replacement.ClientPrivate = key.NewNode()
	replacement.IncomingClientKey = keyString(key.NewNode().Public())
	if e = host.commitPair(context.Background(), replacement, "", registeredPairAttemptFixture(t, host, client.PublicKey())); e != nil {
		t.Fatal(e)
	}
	if host.AllowRelayKey(req.RoleKey) || host.AllowRelayKey(keyString(oldOutbound.Public())) {
		t.Fatal("old role inherited fresh canonical approval")
	}
	if !host.AllowRelayKey(replacement.IncomingClientKey) || !host.AllowRelayKey(keyString(replacement.ClientPrivate.Public())) {
		t.Fatal("new role denied")
	}
}

type addressedListener struct {
	net.Listener
	address net.Addr
}

func (l addressedListener) Addr() net.Addr { return l.address }
func TestRelayReservedPortsIncludePrivateAdmission(t *testing.T) {
	relay := &LocalRelay{listener: addressedListener{newFakeListener(), &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54546}}, admissionListener: addressedListener{newFakeListener(), &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 38111}}}
	ports := relay.ReservedPorts()
	if len(ports) != 2 || ports[0] != 38111 || ports[1] != 54546 {
		t.Fatal("private backend port not reserved", ports)
	}
}
