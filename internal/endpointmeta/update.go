package endpointmeta

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"time"
)

const UpdatePrefix = "soba-directlan-update1."
const UpdateDomain = "sobalink directlan endpoint update v1"
const proofDomain = "sobalink directlan endpoint proof v1\x00"

type UpdateBody struct {
	Version            int    `json:"version"`
	Domain             string `json:"domain"`
	PairBinding        string `json:"pair_binding"`
	Issuer             string `json:"issuer"`
	Recipient          string `json:"recipient"`
	IssuerTunnelKey    string `json:"issuer_tunnel_key"`
	RecipientTunnelKey string `json:"recipient_tunnel_key"`
	Sequence           string `json:"sequence"`
	PriorEndpoint      string `json:"prior_endpoint"`
	Operation          string `json:"operation"`
	Endpoint           string `json:"endpoint"`
	ScopeDigest        string `json:"scope_digest"`
	Issued             string `json:"issued"`
	Lifetime           string `json:"lifetime"`
	Expires            string `json:"expires"`
}

func (u UpdateBody) validate() error {
	if u.Version != 1 || u.Domain != UpdateDomain || !validHex(u.PairBinding) || !validHex(u.Issuer) || !validHex(u.Recipient) || u.Issuer == u.Recipient || !validTunnel(u.IssuerTunnelKey) || !validTunnel(u.RecipientTunnelKey) || u.IssuerTunnelKey == u.RecipientTunnelKey || !validHex(u.ScopeDigest) {
		return ErrInvalid
	}
	if _, err := sequence(u.Sequence, false); err != nil {
		return err
	}
	if _, err := endpoint(u.PriorEndpoint); err != nil {
		return err
	}
	switch u.Operation {
	case "set":
		if _, err := endpoint(u.Endpoint); err != nil {
			return err
		}
	case "withdraw":
		if u.Endpoint != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return validity(u.Issued, u.Lifetime, u.Expires)
}

type Envelope struct {
	Update    UpdateBody `json:"update"`
	Signature string     `json:"signature"`
}

func (e Envelope) validate() error {
	if err := e.Update.validate(); err != nil {
		return err
	}
	_, err := rawBytes(e.Signature, ed25519.SignatureSize)
	return err
}

// Sign encodes metadata with a caller-supplied key. It neither selects a local
// endpoint nor establishes permission to export a production device's claim.
func Sign(u UpdateBody, key ed25519.PrivateKey) (Envelope, error) {
	if len(key) != ed25519.PrivateKeySize || hex.EncodeToString(key.Public().(ed25519.PublicKey)) != u.Issuer {
		return Envelope{}, ErrIdentity
	}
	b, err := Encode(u)
	if err != nil {
		return Envelope{}, err
	}
	e := Envelope{u, base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, append([]byte(proofDomain), b...)))}
	if _, err := Encode(e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func ParseEnvelope(b []byte) (Envelope, error) {
	var e Envelope
	err := decode(b, &e)
	return e, err
}

func ParseUpdateText(s string) (Envelope, error) {
	b, err := decodeText(s, UpdatePrefix)
	if err != nil {
		return Envelope{}, err
	}
	return ParseEnvelope(b)
}

func (e Envelope) Text() (string, error) {
	b, err := Encode(e)
	if err != nil {
		return "", err
	}
	return UpdatePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func (e Envelope) Digest() (string, error) {
	b, err := Encode(e)
	if err != nil {
		return "", err
	}
	return digest("sobalink directlan endpoint digest v1", b), nil
}

// Verify checks canonical structure, signature, immutable context, direction,
// and context scopes. It deliberately does not apply current-time freshness:
// saved expired proofs still carry replay evidence.
func Verify(e Envelope, pair PairContext, recipient string) error {
	if _, err := Encode(e); err != nil {
		return err
	}
	binding, err := pair.Binding()
	if err != nil {
		return err
	}
	u := e.Update
	if u.PairBinding != binding || u.Recipient != recipient {
		return ErrIdentity
	}
	issuer, dest := pair.HostKey, pair.JoinerKey
	issuerTunnel, destTunnel := pair.HostTunnelKey, pair.JoinerTunnelKey
	issuerScope, destScope := pair.HostScope, pair.JoinerScope
	if recipient == pair.HostKey {
		issuer, dest = pair.JoinerKey, pair.HostKey
		issuerTunnel, destTunnel = pair.JoinerTunnelKey, pair.HostTunnelKey
		issuerScope, destScope = pair.JoinerScope, pair.HostScope
	}
	scopeDigest, _ := destScope.Digest()
	if u.Issuer != issuer || u.Recipient != dest || u.IssuerTunnelKey != issuerTunnel || u.RecipientTunnelKey != destTunnel || u.ScopeDigest != scopeDigest {
		return ErrIdentity
	}
	if !issuerScope.Contains(u.PriorEndpoint) || !destScope.Contains(u.PriorEndpoint) {
		return ErrPolicy
	}
	if u.Operation == "set" && (!issuerScope.Contains(u.Endpoint) || !destScope.Contains(u.Endpoint)) {
		return ErrPolicy
	}
	b, _ := Encode(u)
	sig, _ := rawBytes(e.Signature, ed25519.SignatureSize)
	key, _ := hex.DecodeString(u.Issuer)
	if !ed25519.Verify(key, append([]byte(proofDomain), b...), sig) {
		return ErrIdentity
	}
	return nil
}

// Inspect is observational. It never advances counters or changes approvals.
func Inspect(e Envelope, pair PairContext, recipient string, now time.Time) error {
	if err := Verify(e, pair, recipient); err != nil {
		return err
	}
	u := e.Update
	if !current(u.Issued, u.Lifetime, u.Expires, now) {
		return ErrExpired
	}
	return nil
}
