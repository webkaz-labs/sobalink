package lanlink

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"sync"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// RelayIdentity is secret state. Generate only on an explicit setup action and
// save atomically before starting a relay. It is never generated on startup.
type RelayIdentity struct {
	Key            key.NodePrivate `json:"key"`
	CertificatePEM []byte          `json:"certificate_pem"`
	PrivateKeyPEM  []byte          `json:"private_key_pem"`
}

func GenerateRelayIdentity(ip netip.Addr) (RelayIdentity, error) {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return RelayIdentity{}, errors.New("private or loopback relay address required")
	}
	priv, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return RelayIdentity{}, e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return RelayIdentity{}, e
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "sobalink relay"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{ip.AsSlice()}}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if e != nil {
		return RelayIdentity{}, e
	}
	pk, e := x509.MarshalPKCS8PrivateKey(priv)
	if e != nil {
		return RelayIdentity{}, e
	}
	return RelayIdentity{Key: key.NewNode(), CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})}, nil
}
func (i RelayIdentity) Certificate(ip netip.Addr) (tls.Certificate, string, error) {
	if i.Key.IsZero() {
		return tls.Certificate{}, "", errors.New("saved DERP identity required")
	}
	cert, e := tls.X509KeyPair(i.CertificatePEM, i.PrivateKeyPEM)
	if e != nil || len(cert.Certificate) != 1 {
		return tls.Certificate{}, "", errors.New("invalid saved relay certificate")
	}
	parsed, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil || parsed.VerifyHostname(ip.String()) != nil || time.Now().Before(parsed.NotBefore) || !time.Now().Before(parsed.NotAfter) {
		return tls.Certificate{}, "", errors.New("relay certificate does not match endpoint or is expired")
	}
	hash := sha256.Sum256(parsed.Raw)
	return cert, hex.EncodeToString(hash[:]), nil
}

// LocalRelay accepts only explicit private/loopback high ports. DERP admission
// validates a proved node key via a private loopback controller. This is not a
// public relay service and never contacts tailscaled or a public coordinator.
const embeddedBootstrapEnabled = true

var ErrEmbeddedBootstrap = errors.New("embedded relay requires authenticated bootstrap admission")

type LocalRelay struct {
	wg                          sync.WaitGroup
	derp                        *derpserver.Server
	server, admission           *http.Server
	listener, admissionListener net.Listener
	once                        sync.Once
	done                        chan struct{}
	stop                        func() bool
	mu                          sync.Mutex
}

func StartLocalRelay(ctx context.Context, address netip.AddrPort, identity RelayIdentity, allow func(string) bool, bootstrap ...func([]byte) error) (*LocalRelay, error) {
	// Fail closed unless the authenticated bootstrap handler is explicitly wired.
	if !embeddedBootstrapEnabled || len(bootstrap) != 1 || bootstrap[0] == nil {
		return nil, ErrEmbeddedBootstrap
	}
	if !address.IsValid() || address.Port() < 1024 || address.Addr().Zone() != "" || (!address.Addr().IsPrivate() && !address.Addr().IsLoopback()) || allow == nil {
		return nil, errors.New("explicit private relay high port and admission policy required")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := ValidateBuild(); e != nil {
		return nil, e
	}
	cert, _, e := identity.Certificate(address.Addr())
	if e != nil {
		return nil, e
	}
	admission, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	ln, e := (&net.ListenConfig{}).Listen(ctx, "tcp", address.String())
	if e != nil {
		admission.Close()
		return nil, e
	}
	var token [32]byte
	if _, e = rand.Read(token[:]); e != nil {
		admission.Close()
		ln.Close()
		return nil, e
	}
	path := "/admit/" + hex.EncodeToString(token[:])
	d := derpserver.New(identity.Key, logger.Discard)
	d.SetVerifyClientURL("http://" + admission.Addr().String() + path)
	d.SetVerifyClientURLFailOpen(false)
	ctl := &http.Server{Handler: admissionHandler(path, allow), ErrorLog: logger.StdLogger(logger.Discard), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxHeaderBytes: 4096}
	mux := http.NewServeMux()
	mux.HandleFunc("/derp/latency-check", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/derp", derpserver.Handler(d))
	mux.Handle("/sobalink/pair/bootstrap", relayBootstrapHandler(bootstrap[0]))
	srv := &http.Server{Handler: mux, ErrorLog: logger.StdLogger(logger.Discard), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	tlsListener := tls.NewListener(&limitedRelayListener{Listener: ln, slots: make(chan struct{}, 64), lease: 2 * time.Minute}, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
	relay := &LocalRelay{derp: d, server: srv, admission: ctl, listener: tlsListener, admissionListener: admission, done: make(chan struct{})}
	relay.wg.Add(2)
	relay.mu.Lock()
	relay.stop = context.AfterFunc(ctx, func() { relay.Close() })
	relay.mu.Unlock()
	go func() {
		defer relay.wg.Done()
		ctl.Serve(&limitedRelayListener{Listener: admission, slots: make(chan struct{}, 16)})
		relay.shutdown()
	}()
	go func() { defer relay.wg.Done(); srv.Serve(tlsListener); relay.shutdown() }()
	return relay, nil
}
func admissionHandler(path string, allow func(string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(b) > 4096 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var req tailcfg.DERPAdmitClientRequest
		if strictJSON(b, &req) != nil || req.NodePublic.IsZero() {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tailcfg.DERPAdmitClientResponse{Allow: allow(keyString(req.NodePublic))})
	})
}
func (r *LocalRelay) Close() error {
	r.mu.Lock()
	stop := r.stop
	r.mu.Unlock()
	if stop != nil {
		stop()
	}
	r.shutdown()
	r.wg.Wait()
	return nil
}
func (r *LocalRelay) shutdown() {
	r.once.Do(func() {
		r.server.Close()
		r.admission.Close()
		r.derp.Close()
		r.listener.Close()
		r.admissionListener.Close()
		close(r.done)
	})
}

type limitedRelayListener struct {
	net.Listener
	slots chan struct{}
	lease time.Duration
}

func (l *limitedRelayListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.slots <- struct{}{}:
			wrapped := &limitedRelayConn{Conn: c, release: func() { <-l.slots }}
			if l.lease > 0 {
				wrapped.mu.Lock()
				wrapped.timer = time.AfterFunc(l.lease, func() { wrapped.Close() })
				wrapped.mu.Unlock()
			}
			return wrapped, nil
		default:
			c.Close()
		}
	}
}

type limitedRelayConn struct {
	mu    sync.Mutex
	timer *time.Timer
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedRelayConn) Close() error {
	c.mu.Lock()
	timer := c.timer
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	e := c.Conn.Close()
	c.once.Do(c.release)
	return e
}

// AllowRelayKey authorizes the server and role keys required by this node's
// current pairs; pending handshakes are bounded. Relay admission is checked when
// a DERP connection starts; application revocation is separately immediate.
func (n *Node) AllowRelayKey(s string) bool {
	if s == n.PublicKey() {
		return true
	}
	n.mu.Lock()
	b := n.cfg.Trust
	b.mu.Lock()
	approved := false
	for id, r := range n.clients {
		if s == id || s == r.remote.IncomingClientKey || s == keyString(r.remote.ClientPrivate.Public()) {
			approved = b.peers[id] != nil
			break
		}
	}
	b.mu.Unlock()
	n.mu.Unlock()
	if approved {
		return true
	}
	if n.cfg.Trust.pending(s) {
		return true
	}
	return n.allowedBootstrapRole(s) // A proved, unexpired, explicit role only.
}
func (i RelayIdentity) Endpoint(address netip.AddrPort) (TrustedRelay, error) {
	_, pin, e := i.Certificate(address.Addr())
	if e != nil {
		return TrustedRelay{}, e
	}
	r := TrustedRelay{Address: address, CertificateSHA256: pin}
	if e = r.Validate(); e != nil {
		return TrustedRelay{}, fmt.Errorf("relay endpoint: %w", e)
	}
	return r, nil
}

// ReservedPorts exposes only numeric backend ports, never the private admission
// URL/token. Applications must exclude these from broad local-service shares.
func (r *LocalRelay) ReservedPorts() []uint16 {
	if r == nil {
		return nil
	}
	seen := make(map[uint16]bool)
	for _, ln := range []net.Listener{r.listener, r.admissionListener} {
		if ln == nil || ln.Addr() == nil {
			continue
		}
		if ap, e := netip.ParseAddrPort(ln.Addr().String()); e == nil && ap.Port() != 0 {
			seen[ap.Port()] = true
		}
	}
	ports := make([]uint16, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}
