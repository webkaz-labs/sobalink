package lanlink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"github.com/webkaz-labs/sobalink/internal/deadline"
	"io"
	"net"
	"net/http"
	"time"

	"tailscale.com/net/tlsdial"
)

// This provisional relay membership is not an application trust approval.
// It can only be created by the same server-key-authenticated pairing transcript.
type relayBootstrap struct {
	sender  string
	token   [32]byte
	expires time.Time
}

func (n *Node) AuthorizeRelayBootstrap(frame []byte) error {
	if len(frame) > maxPairMessage {
		return ErrInvite
	}
	var outer pairEnvelope
	if strictJSON(frame, &outer) != nil {
		return ErrInvite
	}
	var request pairRequest
	if _, e := openMessage(n.cfg.Identity, frame, outer.Sender, &request); e != nil {
		return e
	}
	req, _, _, e := n.readRequest(frame, request.RoleKey)
	if e != nil {
		return e
	}
	hash := sha256.Sum256([]byte(req.Token))
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.cfg.Trust
	b.mu.Lock()
	defer b.mu.Unlock()
	if n.closed {
		return net.ErrClosed
	}
	inv, ok := b.invites[hash]
	now := time.Now()
	if !ok || inv.peer.Key != req.Sender || !deadline.Active(now, inv.expires) {
		return ErrInvite
	}
	if n.bootstrap == nil {
		n.bootstrap = make(map[string]relayBootstrap)
	}
	for role, entry := range n.bootstrap {
		if !deadline.Active(now, entry.expires) {
			delete(n.bootstrap, role)
			continue
		}
		if entry.token == hash && role != req.RoleKey {
			return errors.New("invitation is already bound to another pending role")
		}
	}
	if _, ok := n.bootstrap[req.RoleKey]; ok {
		return nil
	}
	if len(n.bootstrap) >= 16 {
		return errors.New("too many pending relay roles")
	}
	expires := now.Add(2 * time.Minute)
	if inv.expires.Before(expires) {
		expires = inv.expires
	}
	n.bootstrap[req.RoleKey] = relayBootstrap{req.Sender, hash, expires}
	return nil
}
func (n *Node) allowedBootstrapRole(role string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	entry, ok := n.bootstrap[role]
	if !ok || !deadline.Active(time.Now(), entry.expires) {
		return false
	}
	b := n.cfg.Trust
	b.mu.Lock()
	defer b.mu.Unlock()
	inv, ok := b.invites[entry.token]
	return ok && inv.peer.Key == entry.sender && deadline.Active(time.Now(), inv.expires)
}
func relayBootstrapHandler(authorize func([]byte) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method unavailable", http.StatusMethodNotAllowed)
			return
		}
		data, e := io.ReadAll(io.LimitReader(r.Body, maxPairMessage+1))
		if e != nil || len(data) > maxPairMessage || authorize(data) != nil {
			http.Error(w, "pairing unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
}

// This explicit selected-relay control request is not application forwarding.
// It uses a literal address, pinned certificate, no proxy/DNS/redirect/fallback.
func postRelayBootstrap(ctx context.Context, relay TrustedRelay, frame []byte) error {
	return relayBootstrapRoundTrip(ctx, relay, frame, (&net.Dialer{}).DialContext)
}
func relayBootstrapRoundTrip(ctx context.Context, relay TrustedRelay, frame []byte, dial func(context.Context, string, string) (net.Conn, error)) error {
	if e := relay.Validate(); e != nil {
		return e
	}
	if len(frame) > maxPairMessage {
		return ErrInvite
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: relay.Address.Addr().String()}
	tlsdial.SetConfigExpectedCertHash(cfg, relay.CertificateSHA256)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: cfg, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != relay.Address.String() {
			return nil, errors.New("bootstrap destination changed")
		}
		return dial(ctx, "tcp", address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("bootstrap redirects forbidden") }}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+relay.Address.String()+"/sobalink/pair/bootstrap", bytes.NewReader(frame))
	if e != nil {
		return e
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, e := client.Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("selected relay did not authorize the pairing bootstrap")
	}
	return nil
}
