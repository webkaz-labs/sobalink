package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/httpbound"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type peerServer struct {
	http        *http.Server
	listeners   []net.Listener
	mu          sync.Mutex
	connections map[net.Conn]string
	slots       chan struct{}
}
type connectionKey struct{}

type verifiedBody struct {
	io.ReadCloser
	check func() error
	last  time.Time
}

func (b *verifiedBody) Read(p []byte) (int, error) {
	if time.Since(b.last) >= time.Second {
		if err := b.check(); err != nil {
			return 0, err
		}
		b.last = time.Now()
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF || time.Since(b.last) >= time.Second {
		if current := b.check(); current != nil {
			clear(p[:n])
			return 0, current
		}
		b.last = time.Now()
	}
	return n, err
}

func (c *Core) startPeerServer(ips []netip.Addr) error {
	n := c.nodeCopy()
	if n == nil {
		return errors.New("network unavailable")
	}
	var ip netip.Addr
	for _, a := range ips {
		if a.Is4() {
			ip = a
			break
		}
	}
	if !ip.IsValid() && len(ips) > 0 {
		ip = ips[0]
	}
	if !ip.IsValid() {
		return errors.New("no current self address")
	}
	p := &peerServer{connections: map[net.Conn]string{}, slots: make(chan struct{}, 16)}
	p.http = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.peerHTTP(p, w, r) }), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10, BaseContext: func(net.Listener) context.Context { return c.ctx }, ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		p.mu.Lock()
		p.connections[conn] = ""
		p.mu.Unlock()
		return context.WithValue(ctx, connectionKey{}, conn)
	}, ConnState: func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed || state == http.StateHijacked {
			p.mu.Lock()
			delete(p.connections, conn)
			p.mu.Unlock()
		}
	}}
	gate := httpbound.New(16)
	for _, port := range []int{PeerPort, DiscoveryPort} {
		l, e := n.Listen("tcp", config.Address(ip.String(), port))
		if e != nil {
			_ = p.Close()
			return e
		}
		p.listeners = append(p.listeners, gate.Wrap(l))
	}
	c.mu.Lock()
	c.peerServer = p
	c.mu.Unlock()
	for _, l := range p.listeners {
		go func() { _ = p.http.Serve(l) }()
	}
	return nil
}
func (p *peerServer) Close() error {
	for _, l := range p.listeners {
		_ = l.Close()
	}
	if p.http != nil {
		return p.http.Close()
	}
	return nil
}
func (p *peerServer) revoke(id string) {
	p.mu.Lock()
	var conns []net.Conn
	for conn, peer := range p.connections {
		if peer == id {
			conns = append(conns, conn)
		}
	}
	p.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func peerFailure(w http.ResponseWriter, status int) {
	reply(w, status, map[string]string{"error": "request refused"})
}
func readJSON(r *http.Request, limit int64, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON required")
	}
	limited := &io.LimitedReader{R: r.Body, N: limit + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	e := d.Decode(v)
	if limited.N == 0 {
		return errPeerRequestTooLarge
	}
	if e != nil {
		return e
	}
	var extra any
	e = d.Decode(&extra)
	if limited.N == 0 {
		return errPeerRequestTooLarge
	}
	if e != io.EOF {
		return errors.New("trailing request data")
	}
	return nil
}

var errPeerRequestTooLarge = errors.New("peer request exceeds its JSON envelope limit")

func (c *Core) peerHTTP(p *peerServer, w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		peerFailure(w, 503)
		return
	}
	// Browser-originated requests have no role in the peer protocol.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		peerFailure(w, 403)
		return
	}
	remote, e := netip.ParseAddrPort(r.RemoteAddr)
	if e != nil {
		peerFailure(w, 403)
		return
	}
	peerID, e := c.authenticated(r.Context(), remote)
	if e != nil {
		peerFailure(w, 403)
		return
	}
	verify := func() error {
		id, e := c.authenticated(r.Context(), remote)
		if e != nil || id != peerID {
			return errors.New("current request identity changed")
		}
		return nil
	}
	r.Body = &verifiedBody{ReadCloser: r.Body, check: verify, last: time.Now()}
	if conn, ok := r.Context().Value(connectionKey{}).(net.Conn); ok {
		p.mu.Lock()
		p.connections[conn] = peerID
		p.mu.Unlock()
	}
	if r.Method == "GET" && r.URL.Path == "/v1/hello" {
		reply(w, 200, map[string]any{"protocol": 1, "product": "sobalink", "capabilities": []string{"text", "batch", "ranges"}})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/.well-known/sobalink/services/v2" {
		reply(w, 200, map[string]any{"version": 2, "services": c.permittedServices(peerID)})
		return
	}
	t, ok := c.trust(peerID)
	if !ok || t.Paused {
		peerFailure(w, 403)
		return
	}
	peer := transfer.Peer{ID: t.ID, Generation: t.Generation}
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/messages":
		var msg struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		err := readJSON(r, int64(messageframe.PeerRequestBytes), &msg)
		if errors.Is(err, errPeerRequestTooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "request_too_large", "error": "message request is too large; shorten the text or reduce JSON padding"})
			return
		}
		if err != nil || !config.ValidPeerID(msg.ID) {
			peerFailure(w, 400)
			return
		}
		if len(msg.Text) > messageframe.TextBytes {
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "message_too_large", "error": "message exceeds 16384 UTF-8 bytes; shorten the text and retry"})
			return
		}
		if !validText(msg.Text) {
			peerFailure(w, 400)
			return
		}
		if verify() != nil {
			peerFailure(w, 403)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closing || c.ctx.Err() != nil || r.Context().Err() != nil {
			peerFailure(w, 403)
			return
		}
		currentTrust := false
		for _, current := range c.profile.Peers {
			if current.ID == t.ID && current.Network == t.Network && current.Network == c.profile.Settings.Network && current.Generation == t.Generation && !current.Paused {
				currentTrust = true
				break
			}
		}
		if !currentTrust {
			peerFailure(w, 403)
			return
		}
		for _, m := range c.messages {
			if m.ID == msg.ID && m.PeerID == peerID && m.Direction == "incoming" {
				if m.Text != msg.Text {
					peerFailure(w, 409)
					return
				}
				reply(w, 200, map[string]string{"id": m.ID, "status": "received"})
				return
			}
		}
		message := Message{ID: msg.ID, PeerID: peerID, Text: msg.Text, Direction: "incoming", CreatedAt: time.Now().UTC(), Status: "received"}
		if e := c.appendMessageLocked(message); e != nil {
			reply(w, http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": "message could not be saved; check free storage and private state permissions"})
			return
		}
		reply(w, 200, map[string]string{"id": message.ID, "status": "received"})
	case r.Method == "POST" && r.URL.Path == "/v1/offers":
		var manifest transfer.Manifest
		if readJSON(r, int64(transferManifestJSONBytes), &manifest) != nil {
			peerFailure(w, 400)
			return
		}
		batch, e := c.transfers.Offer(peer, manifest)
		if e != nil {
			peerFailure(w, 400)
			return
		}
		reply(w, 200, batchWire(batch))
	case strings.HasPrefix(r.URL.Path, "/v1/batches/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/batches/"), "/")
		if len(parts) < 1 || !config.ValidPeerID(parts[0]) {
			peerFailure(w, 400)
			return
		}
		batch, e := c.transfers.Get(parts[0])
		if e != nil || batch.Peer != peer {
			peerFailure(w, 404)
			return
		}
		if len(parts) == 1 && r.Method == "GET" {
			reply(w, 200, batchWire(batch))
			return
		}
		if len(parts) == 2 && parts[1] == "cancel" && r.Method == "POST" {
			batch, e = c.transfers.Cancel(batch.ID)
			if e != nil {
				peerFailure(w, 409)
				return
			}
			reply(w, 200, batchWire(batch))
			return
		}
		if len(parts) == 3 && parts[1] == "retry" && r.Method == "POST" {
			batch, e = c.transfers.RetryFile(batch.ID, parts[2])
			if e != nil {
				peerFailure(w, 409)
				return
			}
			reply(w, 200, batchWire(batch))
			return
		}
		if len(parts) == 3 && parts[1] == "files" && r.Method == "PUT" {
			var size int64 = -1
			for _, file := range batch.Files {
				if file.Entry.ID == parts[2] {
					size = file.Entry.Size
					break
				}
			}
			if size < 0 {
				peerFailure(w, 404)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, size+1)
			ack, e := c.transfers.ReceiveFile(r.Context(), peer, batch.ID, parts[2], r.Body)
			if e != nil {
				peerFailure(w, 409)
				return
			}
			reply(w, 200, ack)
			return
		}
		peerFailure(w, 404)
	default:
		peerFailure(w, 404)
	}
}

func (c *Core) peerRequest(ctx context.Context, id, method, path string, body io.Reader, contentType string, out any) error {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			return nil, errors.New("TCP required")
		}
		return c.dial(ctx, id, "tcp", PeerPort)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("peer redirects are refused") }}
	req, e := http.NewRequestWithContext(ctx, method, "http://peer.invalid"+path, body)
	if e != nil {
		return e
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("peer did not respond; check its network and sobalink permissions")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if path == "/v1/messages" {
			switch resp.StatusCode {
			case http.StatusRequestEntityTooLarge:
				return &localCommandError{"message_peer_too_large", "peer refused the message size; shorten the text and retry"}
			case http.StatusInsufficientStorage:
				return &localCommandError{"message_peer_storage_unavailable", "peer could not save the message; check its free storage and private state permissions before retrying"}
			}
		}
		return fmt.Errorf("peer refused this action (HTTP %d); check approval and receive settings", resp.StatusCode)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
		return e
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 256<<10))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func (c *Core) peerJSON(ctx context.Context, id, method, path string, input, out any) error {
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	return c.peerRequest(ctx, id, method, path, body, "application/json", out)
}

func (c *Core) probePeer(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var hello struct {
		Protocol     int      `json:"protocol"`
		Product      string   `json:"product"`
		Capabilities []string `json:"capabilities"`
	}
	if e := c.peerJSON(ctx, id, "GET", "/v1/hello", nil, &hello); e != nil {
		return e
	}
	if hello.Protocol != 1 || hello.Product != "sobalink" {
		return errors.New("peer protocol is unsupported")
	}
	var services struct {
		Version  int             `json:"version"`
		Services []RemoteService `json:"services"`
	}
	if e := c.peerJSON(ctx, id, "GET", "/.well-known/sobalink/services/v2", nil, &services); e != nil {
		return e
	}
	if services.Version != 2 || len(services.Services) > 64 {
		return errors.New("invalid peer service response")
	}
	for _, service := range services.Services {
		if err := validateRemote(service); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.confirmed[id] = time.Now()
	c.discovered[id] = services.Services
	c.mu.Unlock()
	return nil
}
func (c *Core) refreshPeers(st identity.State) {
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	defer cancel()
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, p := range st.Snapshot.Peers {
		if i >= 128 || ctx.Err() != nil {
			break
		}
		if p.Expired || p.ID == "" {
			continue
		}
		c.mu.RLock()
		recent := time.Since(c.confirmed[p.ID]) < 8*time.Second
		c.mu.RUnlock()
		if recent {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(id string) { defer wg.Done(); defer func() { <-slots }(); _ = c.probePeer(ctx, id) }(p.ID)
	}
	wg.Wait()
}

func validText(text string) bool {
	return len(strings.TrimSpace(text)) > 0 && len(text) <= messageframe.TextBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

// The ordinary configuration limit stays at 64 KiB. Retention can keep one
// message even when its escaped text exceeds the 48 KiB history target, so the
// private history file needs room for that message and its bounded metadata.
// The old 64 KiB allowance preserves readable legacy indented history files.
const maxMessageHistoryBytes = max(64<<10, 6*messageframe.TextBytes+2*messageframe.IDBytes+len(`[{"id":"","peerId":"","direction":"outgoing","text":"","createdAt":"2006-01-02T15:04:05.999999999+00:00","status":"received"}]`)+1)

func readMessageHistory(path string, history *[]Message) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("message history must be a regular file, not a symlink")
	}
	if info.Size() > int64(maxMessageHistoryBytes) {
		return &localCommandError{"message_history_too_large", "message history exceeds its bounded storage envelope; restore a valid history file"}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	limited := &io.LimitedReader{R: f, N: int64(maxMessageHistoryBytes) + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	err = d.Decode(history)
	if limited.N == 0 {
		return &localCommandError{"message_history_too_large", "message history exceeds its bounded storage envelope; restore a valid history file"}
	}
	if err != nil {
		return err
	}
	var extra any
	err = d.Decode(&extra)
	if limited.N == 0 {
		return &localCommandError{"message_history_too_large", "message history exceeds its bounded storage envelope; restore a valid history file"}
	}
	if err != io.EOF {
		return errors.New("unexpected data after message history")
	}
	return nil
}

func writeMessageHistory(path string, history []Message) error {
	b, err := json.Marshal(history)
	if err != nil {
		return err
	}
	if len(b)+1 > maxMessageHistoryBytes {
		return &localCommandError{"message_history_too_large", "message history exceeds its bounded storage envelope; restore a valid history file"}
	}
	return config.AtomicWrite(path, append(b, '\n'))
}

func (c *Core) loadMessages() error {
	var history []Message
	if e := readMessageHistory(filepath.Join(c.dir, "messages.json"), &history); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return e
	}
	if len(history) > 128 {
		return errors.New("message history exceeds limit")
	}
	for _, m := range history {
		if !validText(m.Text) || !config.ValidPeerID(m.PeerID) || !config.ValidPeerID(m.ID) ||
			(m.Direction != "incoming" && m.Direction != "outgoing") ||
			(m.Status != "pending" && m.Status != "received" && m.Status != "sent" && m.Status != "failed") {
			return errors.New("invalid message history")
		}
	}
	c.messages = history
	return nil
}
func (c *Core) appendMessageLocked(m Message) error {
	next := append(append([]Message(nil), c.messages...), m)
	cut := time.Now().Add(-30 * 24 * time.Hour)
	for len(next) > 1 {
		b, e := json.Marshal(next)
		if e != nil {
			return e
		}
		if len(next) <= 128 && len(b) < 48<<10 && next[0].CreatedAt.After(cut) {
			break
		}
		next = next[1:]
	}
	if e := writeMessageHistory(filepath.Join(c.dir, "messages.json"), next); e != nil {
		return e
	}
	c.messages = next
	return nil
}
func (c *Core) sendMessageCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var v struct {
		PeerID string `json:"peerId"`
		Text   string `json:"text"`
	}
	if e := decodePayload(raw, &v); e != nil {
		return nil, e
	}
	if len(v.Text) > messageframe.TextBytes {
		return nil, &localCommandError{"message_too_large", "message exceeds 16384 UTF-8 bytes; shorten the text and retry"}
	}
	if !validText(v.Text) {
		return nil, errors.New("message must contain 1..16384 UTF-8 bytes")
	}
	t, ok := c.trust(v.PeerID)
	if !ok || t.Paused {
		return nil, errors.New("approve this exact peer and resume communication first")
	}
	msg := Message{ID: randomID(), PeerID: v.PeerID, Direction: "outgoing", Text: v.Text, CreatedAt: time.Now().UTC(), Status: "pending"}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var ack struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	e := c.peerJSON(ctx, v.PeerID, "POST", "/v1/messages", map[string]string{"id": msg.ID, "text": msg.Text}, &ack)
	if e != nil {
		msg.Status = "failed"
	} else if ack.ID != msg.ID || ack.Status != "received" {
		e = errors.New("peer did not confirm receipt")
		msg.Status = "failed"
	} else {
		msg.Status = "sent"
	}
	c.mu.Lock()
	saveErr := c.appendMessageLocked(msg)
	c.mu.Unlock()
	if saveErr != nil {
		message := "local message history could not be saved; check free storage and private state permissions, and check the receiver before retrying"
		if msg.Status == "sent" {
			message = "peer acknowledged receipt, but local message history could not be saved; check free storage and private state permissions; do not resend the delivered message"
		}
		return msg, fmt.Errorf("%w: %v", &localCommandError{"message_history_unavailable", message}, saveErr)
	}
	return msg, e
}
