package directlan

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"time"
)

const protocolName = "sobalink-directlan/1"
const contextProtocolName = "sobalink-directlan/2"

func certificate(id Identity, now time.Time) (tls.Certificate, error) {
	priv, err := id.private()
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "sobalink direct LAN"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, priv.Public(), priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

func certificateKey(raw [][]byte, now time.Time) (string, error) {
	if len(raw) != 1 {
		return "", ErrIdentity
	}
	c, e := x509.ParseCertificate(raw[0])
	if e != nil {
		return "", ErrIdentity
	}
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize || now.Before(c.NotBefore) || !now.Before(c.NotAfter) || c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) != nil {
		return "", ErrIdentity
	}
	return hex.EncodeToString(pub), nil
}
func tlsConfig(cert tls.Certificate, pin string, server bool) *tls.Config {
	return tlsConfigProtocol(cert, pin, server, protocolName)
}

func tlsConfigProtocol(cert tls.Certificate, pin string, server bool, protocol string) *tls.Config {
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{protocol}, SessionTicketsDisabled: true}
	// Standard Web PKI/DNS names are deliberately inapplicable: the exchanged
	// exact Ed25519 key is the trust anchor. TLS CertificateVerify proves key
	// possession; VerifyConnection enforces that pin on every connection.
	if server {
		cfg.ClientAuth = tls.RequireAnyClientCert
	} else {
		cfg.InsecureSkipVerify = true
	}
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if s.NegotiatedProtocol != protocol {
			return ErrIdentity
		}
		raw := make([][]byte, len(s.PeerCertificates))
		for i, c := range s.PeerCertificates {
			raw[i] = c.Raw
		}
		k, e := certificateKey(raw, time.Now())
		if e != nil {
			return e
		}
		if pin != "" && k != pin {
			return ErrIdentity
		}
		return nil
	}
	return cfg
}

// Server ALPN advertisement is not downgrade authorization. Classification is
// checked only after authenticating the certificate and stays generation-owned.
func (n *Node) ordinaryServerTLS(g *runtimeGeneration) *tls.Config {
	cfg := tlsConfig(n.cert, "", true)
	cfg.NextProtos = []string{contextProtocolName, protocolName}
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		raw := make([][]byte, len(s.PeerCertificates))
		for i, cert := range s.PeerCertificates {
			raw[i] = cert.Raw
		}
		key, err := certificateKey(raw, time.Now())
		if err != nil || key == n.PublicKey() {
			return ErrIdentity
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.ordinaryPeerProtocolLocked(g, key, s.NegotiatedProtocol)
	}
	return cfg
}

// Called only after certificate authentication in both TLS admission and frame
// dispatch. Terminal denial precedes the unknown-legacy pairing exception.
func (n *Node) ordinaryPeerProtocolLocked(g *runtimeGeneration, key, protocol string) error {
	if g == nil || g.cfg.deniedKey(key) || n.readyLocked() != nil || n.generation.Load() != g {
		return ErrUntrusted
	}
	expected := protocolName
	if g.cfg.managedKey(key) {
		expected = contextProtocolName
		if n.peers[key] == nil || n.peers[key].g != g {
			return ErrUntrusted
		}
	} else if n.peers[key] == nil && g.cfg.Persist == nil {
		return ErrUntrusted
	}
	if protocol != expected {
		return ErrIdentity
	}
	return nil
}
