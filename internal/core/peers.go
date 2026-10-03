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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
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
		reply(w, 200, map[string]any{"protocol": 1, "product": "sobalink", "capabilities": []string{"text", "batch", "ranges", "discovery-v3"}})
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/.well-known/sobalink/services/v2" || r.URL.Path == discoveryV3Path) {
		c.serveServiceDiscovery(w, r, peerID)
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
		bounds, err := messageframe.ForText(c.MessageTextBytes())
		if err != nil {
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": "invalid message budget"})
			return
		}
		err = readJSON(r, bounds.PeerRequestBytes, &msg)
		if errors.Is(err, errPeerRequestTooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "request_too_large", "error": "message request is too large; shorten the text or reduce JSON padding"})
			return
		}
		if err != nil || !config.ValidPeerID(msg.ID) {
			peerFailure(w, 400)
			return
		}
		if int64(len(msg.Text)) > c.MessageTextBytes() {
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "message_too_large", "error": "message exceeds the configured UTF-8 byte limit; shorten the text or review capacity settings"})
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
				if c.messageHistoryUncertain {
					if err := c.saveMessageHistoryLocked(c.messages); err != nil {
						reply(w, http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(err, true).Error()})
						return
					}
				}
				reply(w, 200, map[string]string{"id": m.ID, "status": "received"})
				return
			}
		}
		message := Message{ID: msg.ID, PeerID: peerID, Text: msg.Text, Direction: "incoming", CreatedAt: time.Now().UTC(), Status: "received"}
		if e := c.appendMessageLocked(message); e != nil {
			reply(w, http.StatusInsufficientStorage, map[string]string{"code": "message_history_unavailable", "error": messageHistoryError(e, errors.Is(e, config.ErrAtomicCommitted)).Error()})
			return
		}
		reply(w, 200, map[string]string{"id": message.ID, "status": "received"})
	case r.Method == "POST" && r.URL.Path == "/v1/offers":
		var manifest transfer.Manifest
		if readJSON(r, c.receiveManifestJSONBytes(), &manifest) != nil {
			peerFailure(w, 400)
			return
		}
		batch, e := c.transfers.Offer(peer, manifest)
		if diskspace.IsCapacityError(e) {
			replyDiskSpace(w, e)
			return
		}
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
			fileCtx, finishFile := c.operationContext(r.Context(), "fileTransferSeconds")
			defer finishFile()
			deadline, _ := fileCtx.Deadline()
			controller := http.NewResponseController(w)
			_ = controller.SetReadDeadline(deadline)
			_ = controller.SetWriteDeadline(deadline)
			ack, e := c.transfers.ReceiveFile(fileCtx, peer, batch.ID, parts[2], r.Body)
			if diskspace.IsCapacityError(e) {
				replyDiskSpace(w, e)
				return
			}
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
	if method == "PUT" && strings.HasPrefix(path, "/v1/batches/") {
		// File upload and save confirmation share the caller's selected lifetime.
		transport.ResponseHeaderTimeout = 0
	}
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
		if resp.StatusCode == http.StatusInsufficientStorage && (path == "/v1/offers" || strings.HasPrefix(path, "/v1/batches/")) {
			return peerDiskSpaceError(resp.Body)
		}
		if method == "GET" && strings.HasPrefix(path, "/.well-known/sobalink/services/") && resp.StatusCode == http.StatusRequestEntityTooLarge {
			return &localCommandError{"discovery_capacity", "peer discovery exceeds its configured response budget; review discovery capacity on the sharing peer"}
		}
		if method == "GET" && (path == "/v1/hello" || strings.HasPrefix(path, "/.well-known/sobalink/services/")) && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUpgradeRequired) {
			return &localCommandError{"discovery_unsupported", "peer did not provide a supported discovery endpoint; use a known service with manual settings"}
		}
		if path == "/v1/messages" {
			switch resp.StatusCode {
			case http.StatusRequestEntityTooLarge:
				return &localCommandError{"message_peer_too_large", "peer refused the message size; shorten the text and retry"}
			case http.StatusInsufficientStorage:
				return &localCommandError{"message_peer_storage_unavailable", "peer could not confirm message history durability; check the receiver's history, free storage and private state permissions before retrying; do not blindly resend with a new message ID"}
			}
		}
		return fmt.Errorf("peer refused this action (HTTP %d); check approval and receive settings", resp.StatusCode)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
		return e
	}
	responseLimit := int64(256 << 10)
	if path == "/v1/offers" || strings.HasPrefix(path, "/v1/batches/") {
		responseLimit = c.transferResponseJSONBytes()
	}
	if strings.HasPrefix(path, discoveryV3Path) {
		responseLimit = c.limit("resources", "discoveryBytes")
	}
	limited := &io.LimitedReader{R: resp.Body, N: responseLimit + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		if limited.N <= 0 && strings.HasPrefix(path, "/.well-known/sobalink/services/") {
			return &localCommandError{"discovery_capacity", "peer discovery exceeds the configured response budget; review discoveryBytes"}
		}
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected data after peer response")
	}
	if limited.N <= 0 {
		if strings.HasPrefix(path, "/.well-known/sobalink/services/") {
			return &localCommandError{"discovery_capacity", "peer discovery exceeds the configured response budget; review discoveryBytes"}
		}
		return errors.New("peer response exceeds its configured framing budget")
	}
	if page, ok := out.(*servicePage); ok {
		page.receivedBytes = responseLimit + 1 - limited.N
	}
	return nil
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

type peerRefreshAdmissionContext struct {
	scheduler *peerRefreshScheduler
	ticket    *peerRefreshTicket
}

type peerRefreshAdmissionKey struct{}

func (c *Core) probePeer(ctx context.Context, id string) (probeErr error) {
	callerCtx := ctx
	var discovered []RemoteService
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	defer func() {
		if _, err := c.currentPeer(callerCtx, id); callerCtx.Err() == nil && err == nil {
			publish := func() {
				if probeErr == nil {
					c.mu.Lock()
					c.confirmed[id] = time.Now()
					c.discovered[id] = discovered
					c.mu.Unlock()
				}
				c.recordDiscoveryObservation(id, probeErr)
			}
			if admission, ok := callerCtx.Value(peerRefreshAdmissionKey{}).(peerRefreshAdmissionContext); ok {
				admission.scheduler.publish(admission.ticket, publish)
				return
			}
			publish()
			c.mu.RLock()
			retries := c.peerRefreshRetries
			c.mu.RUnlock()
			if probeErr == nil && retries != nil {
				retries.forget(id)
			}
		}
	}()
	var hello struct {
		Protocol     int      `json:"protocol"`
		Product      string   `json:"product"`
		Capabilities []string `json:"capabilities"`
	}
	if e := c.peerJSON(ctx, id, "GET", "/v1/hello", nil, &hello); e != nil {
		return e
	}
	if hello.Protocol != 1 || hello.Product != "sobalink" {
		return &localCommandError{"discovery_unsupported", "peer discovery protocol is unsupported; use manual settings for a known service"}
	}
	for _, capability := range hello.Capabilities {
		if capability == "discovery-v3" {
			services, err := c.queryServicePages(ctx, id)
			if err != nil {
				return err
			}
			discovered = services
			return nil
		}
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
	discovered = services.Services
	return nil
}
func (c *Core) refreshPeers(st identity.State) {
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	defer cancel()
	c.refreshPeerBatchClock(ctx, st, c.probePeer, time.Now, true)
}

// The batch and concurrency limits bound one refresh pass, not peer eligibility.
// Sorted IDs and a persistent cursor ensure later peers also get examined when
// earlier probes consume the time budget. Only examined candidates advance it.
func (c *Core) refreshPeerBatch(ctx context.Context, st identity.State, probe func(context.Context, string) error) {
	c.refreshPeerBatchClock(ctx, st, probe, time.Now, false)
}

func (c *Core) refreshPeerBatchWithBackoff(ctx context.Context, st identity.State, probe func(context.Context, string) error, now time.Time) {
	c.refreshPeerBatchClock(ctx, st, probe, func() time.Time { return now }, true)
}

func (c *Core) refreshPeerBatchClock(ctx context.Context, st identity.State, probe func(context.Context, string) error, now func() time.Time, background bool) {
	if !c.refreshMu.TryLock() {
		return
	}
	defer c.refreshMu.Unlock()
	ids := make([]string, 0, len(st.Snapshot.Peers))
	for _, peer := range st.Snapshot.Peers {
		if !peer.Expired && peer.ID != "" {
			ids = append(ids, peer.ID)
		}
	}
	sort.Strings(ids)
	ids = slices.Compact(ids)
	var retries *peerRefreshScheduler
	if background {
		c.mu.Lock()
		if c.peerRefreshRetries == nil {
			c.peerRefreshRetries = newPeerRefreshScheduler()
		}
		retries = c.peerRefreshRetries
		c.mu.Unlock()
		active := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			active[id] = struct{}{}
		}
		retries.prune(active, now())
		retries.beginBatch()
	}
	isPresent := func(id string) bool {
		index := sort.SearchStrings(ids, id)
		return index < len(ids) && ids[index] == id
	}
	c.mu.Lock()
	for id := range c.discoveryObservations {
		if !isPresent(id) {
			delete(c.discoveryObservations, id)
		}
	}
	for id := range c.confirmed {
		if !isPresent(id) {
			delete(c.confirmed, id)
		}
	}
	for id := range c.discovered {
		if !isPresent(id) {
			delete(c.discovered, id)
		}
	}
	c.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	start := sort.SearchStrings(ids, c.refreshCursor)
	if start < len(ids) && ids[start] == c.refreshCursor {
		start++
	}
	if start == len(ids) {
		start = 0
	}
	type refreshCandidate struct {
		id        string
		admission bool
	}
	candidates := make([]refreshCandidate, 0, min(128, len(ids)))
	selected := make(map[string]struct{}, min(128, len(ids)))
	if background && retries.full() {
		admissionStart := sort.SearchStrings(ids, c.refreshAdmissionCursor)
		if admissionStart < len(ids) && ids[admissionStart] == c.refreshAdmissionCursor {
			admissionStart++
		}
		if admissionStart == len(ids) {
			admissionStart = 0
		}
		for i := 0; i < len(ids) && len(candidates) < peerRefreshOverflowPerBatch; i++ {
			id := ids[(admissionStart+i)%len(ids)]
			if retries.cached(id) {
				continue
			}
			candidates = append(candidates, refreshCandidate{id: id, admission: true})
			selected[id] = struct{}{}
		}
	}
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	defer wg.Wait()
	for examined := 0; examined < len(ids) && len(candidates) < min(128, len(ids)); examined++ {
		id := ids[(start+examined)%len(ids)]
		if _, exists := selected[id]; !exists {
			candidates = append(candidates, refreshCandidate{id: id})
			selected[id] = struct{}{}
		}
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		id := candidate.id
		advance := func() {
			if candidate.admission {
				c.refreshAdmissionCursor = id
			} else {
				c.refreshCursor = id
			}
		}
		c.mu.RLock()
		recent := now().Sub(c.confirmed[id]) < 8*time.Second && !c.confirmed[id].IsZero()
		c.mu.RUnlock()
		if recent {
			advance()
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		if ctx.Err() != nil {
			<-slots
			return
		}
		var ticket *peerRefreshTicket
		if background {
			ticket = retries.admit(id, now())
			if ticket == nil {
				<-slots
				advance()
				continue
			}
		}
		advance()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			probeCtx := ctx
			if background {
				probeCtx = context.WithValue(ctx, peerRefreshAdmissionKey{}, peerRefreshAdmissionContext{retries, ticket})
			}
			err := probe(probeCtx, id)
			if background {
				if ctx.Err() == nil {
					retries.complete(ticket, now(), err == nil)
				} else {
					retries.cancel(ticket)
				}
			}
		}()
	}
}

func validText(text string) bool {
	return len(strings.TrimSpace(text)) > 0 && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

// Legacy framing fixture retained for regression checks; actual history
// storage uses the selected finite messageStorageBytes budget.
const maxMessageHistoryBytes = max(64<<10, 6*messageframe.TextBytes+2*messageframe.IDBytes+len(`[{"id":"","peerId":"","direction":"outgoing","text":"","createdAt":"2006-01-02T15:04:05.999999999+00:00","status":"received"}]`)+1)

func readMessageHistory(path string, limit int64, history *[]Message) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("message history must be a regular file, not a symlink")
	}
	if info.Size() > limit {
		return &localCommandError{"message_history_too_large", "message history exceeds the configured storage budget; raise messageStorageBytes or review explicit history cleanup"}
	}
	f, err := (transfer.Source{LocalPath: path}).Open()
	if err != nil {
		return err
	}
	defer f.Close()
	limited := &io.LimitedReader{R: f, N: limit + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	err = d.Decode(history)
	if limited.N == 0 {
		return &localCommandError{"message_history_too_large", "message history exceeds the configured storage budget; raise messageStorageBytes or review explicit history cleanup"}
	}
	if err != nil {
		return err
	}
	var extra any
	err = d.Decode(&extra)
	if limited.N == 0 {
		return &localCommandError{"message_history_too_large", "message history exceeds the configured storage budget; raise messageStorageBytes or review explicit history cleanup"}
	}
	if err != io.EOF {
		return errors.New("unexpected data after message history")
	}
	return nil
}

func writeMessageHistory(path string, limit int64, history []Message) error {
	return writeMessageHistoryWith(config.AtomicWrite, path, limit, history)
}

func writeMessageHistoryWith(write func(string, []byte) error, path string, limit int64, history []Message) error {
	b, err := json.Marshal(history)
	if err != nil {
		return err
	}
	if int64(len(b))+1 > limit {
		return &localCommandError{"message_history_too_large", "message history exceeds the configured storage budget; raise messageStorageBytes or review explicit history cleanup"}
	}
	return write(path, append(b, '\n'))
}

func (c *Core) loadMessages() error {
	var history []Message
	if e := readMessageHistory(filepath.Join(c.dir, "messages.json"), c.limit("resources", "messageStorageBytes"), &history); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		return e
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
	// Retention choices are cleanup recommendations, never permission to erase
	// older records while accepting a message or applying a different policy.
	saveErr := c.saveMessageHistoryLocked(next)
	if atomicPublished(saveErr) {
		c.messages = next
	}
	return saveErr
}

func (c *Core) sendMessageCommand(ctx context.Context, raw json.RawMessage) (any, error) {
	var v struct {
		PeerID string `json:"peerId"`
		Text   string `json:"text"`
	}
	if e := decodePayload(raw, &v); e != nil {
		return nil, e
	}
	if int64(len(v.Text)) > c.MessageTextBytes() {
		return nil, &localCommandError{"message_too_large", "message exceeds the configured UTF-8 byte limit; shorten the text or review capacity settings"}
	}
	if !validText(v.Text) {
		return nil, errors.New("message must contain valid nonempty UTF-8 text")
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
		if errors.Is(saveErr, config.ErrAtomicCommitted) {
			message = "local message history was replaced, but durability could not be confirmed; inspect history and the receiver before retrying; do not resend a delivered message"
		}
		return msg, privateAtomicError(&localCommandError{"message_history_unavailable", message}, saveErr)
	}
	return msg, e
}
