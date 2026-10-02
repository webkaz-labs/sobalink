package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/transfer"
	"github.com/webkaz-labs/tsnet-bridge/internal/webui"
)

func (c *Core) StartWeb(assets fs.FS) (string, string, error) {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.RLock()
	web := c.web
	c.mu.RUnlock()
	if web == nil {
		var e error
		web, e = webui.Start(c.ctx, assets, c)
		if e != nil {
			return "", "", e
		}
		c.mu.Lock()
		c.web = web
		c.mu.Unlock()
		c.mu.RLock()
		var conflicts []string
		for id, a := range c.active {
			if a.spec.Direction == "share" && a.spec.Network == "tcp" && a.effective.Contains(web.Port()) {
				conflicts = append(conflicts, id)
			}
		}
		c.mu.RUnlock()
		c.stopServiceIDs(conflicts)
	}
	code, e := web.IssueCode()
	return web.URL(), code, e
}
func (c *Core) UICode() (any, error) {
	c.mu.RLock()
	web := c.web
	c.mu.RUnlock()
	if web == nil {
		return nil, errors.New("local UI is unavailable")
	}
	code, e := web.IssueCode()
	return map[string]string{"url": web.URL(), "code": code}, e
}
func (c *Core) IPC(ctx context.Context, raw string) (any, error) {
	if raw == "status" {
		return c.Snapshot(ctx)
	}
	if raw == "ui" {
		return c.UICode()
	}
	if raw == "stop" {
		c.cancel()
		return map[string]string{"state": "stopping"}, nil
	}
	var cmd webui.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		return nil, errors.New("unknown command; use soba help")
	}
	return c.Command(ctx, cmd)
}
func (c *Core) Done() <-chan struct{} { return c.ctx.Done() }

func (c *Core) startNetwork(ctx context.Context) error {
	if c.nodeCopy() != nil {
		return nil
	}
	p := c.profileCopy()
	if p.Settings.Network == "lan" {
		return errors.New("LAN pairing is not enabled in this draft; device-key verification is still under test")
	}
	if p.Settings.Network != "tailnet" {
		return errors.New("choose a network before connecting")
	}
	n, e := c.factory(c.dir, p.Settings.Hostname)
	if e != nil {
		return e
	}
	if e = n.Start(); e != nil {
		_ = n.Close()
		return errors.New("could not start network; private identity state was retained")
	}
	c.mu.Lock()
	c.node = n
	c.networkState = "starting"
	c.networkError = ""
	c.mu.Unlock()
	return nil
}

func (c *Core) maintain() {
	defer c.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
		}
		if !c.op.TryLock() {
			continue
		}
		if c.ctx.Err() != nil {
			c.op.Unlock()
			return
		}
		if c.nodeCopy() != nil {
			st, e := c.current(c.ctx)
			c.networkReady.Store(e == nil && st.Snapshot.Running)
			c.mu.Lock()
			if e != nil {
				c.networkState = "unavailable"
				c.networkError = "Network status is unavailable; retry the connection"
			} else {
				c.networkState = st.Backend
				c.networkError = ""
			}
			ps := c.peerServer
			c.mu.Unlock()
			if e == nil && st.Snapshot.Running {
				if ps == nil {
					if err := c.startPeerServer(st.IPs); err != nil {
						c.mu.Lock()
						c.networkError = "Peer listener unavailable: " + err.Error()
						c.mu.Unlock()
					}
				}
				c.revalidateServices(st)
				c.refreshPeers(st)
			} else {
				c.suspendServices()
			}
		}
		c.expireServices()
		c.op.Unlock()
	}
}

func (c *Core) Snapshot(ctx context.Context) (map[string]any, error) {
	p := c.profileCopy()
	c.mu.RLock()
	state, reason := c.networkState, c.networkError
	messages := append([]Message(nil), c.messages...)
	confirmed := map[string]time.Time{}
	for id, t := range c.confirmed {
		confirmed[id] = t
	}
	c.mu.RUnlock()
	peers := []map[string]any{}
	reservedPorts := []uint16{54543, 54544, 54545}
	c.mu.RLock()
	if c.web != nil {
		reservedPorts = append(reservedPorts, c.web.Port())
	}
	c.mu.RUnlock()
	if st, e := c.current(ctx); e == nil {
		reservedPorts = append(reservedPorts, st.ReservedPorts...)
		for _, peer := range st.Snapshot.Peers {
			if peer.Expired || peer.ID == "" {
				continue
			}
			trusted, ok := c.trust(peer.ID)
			address := ""
			if len(peer.IPs) > 0 {
				address = peer.IPs[0].String()
			}
			name := strings.TrimSuffix(peer.DNSName, ".")
			if name == "" {
				name = peer.ID
			}
			bridge := time.Since(confirmed[peer.ID]) < 15*time.Second
			peers = append(peers, map[string]any{"id": peer.ID, "name": name, "networks": []string{p.Settings.Network}, "online": peer.Online || bridge, "verified": st.Snapshot.Running, "trusted": ok, "path": "unknown", "bridge": bridge, "address": address, "fingerprint": peer.ID, "autosave": map[string]any{"enabled": trusted.Autosave, "paused": trusted.Paused, "directory": trusted.Directory}})
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i]["name"].(string) < peers[j]["name"].(string) })
	if messages == nil {
		messages = []Message{}
	}
	services, shares := []map[string]any{}, []map[string]any{}
	for _, v := range c.serviceViews() {
		if v["direction"] == "share" {
			shares = append(shares, v)
		} else {
			services = append(services, v)
		}
	}
	return map[string]any{"version": c.version, "self": map[string]any{"name": p.Settings.Hostname, "status": state, "error": reason, "receiveDirectory": p.Settings.ReceiveDirectory}, "peers": peers, "messages": messages, "transfers": c.transferViews(), "services": services, "shares": shares, "availableServices": c.discoveredViews(), "reservedPorts": reservedPorts, "settings": p.Settings}, nil
}

func (c *Core) Command(ctx context.Context, cmd webui.Command) (any, error) {
	if len(cmd.RequestID) < 1 || len(cmd.RequestID) > 128 {
		return nil, errors.New("requestId is required")
	}
	digest := sha256.Sum256(append([]byte(cmd.Name+"\x00"), cmd.Payload...))
	sig := hex.EncodeToString(digest[:])
	c.op.Lock()
	defer c.op.Unlock()
	if previous, ok := c.requests[cmd.RequestID]; ok {
		if previous.signature != sig {
			return nil, errors.New("request ID was already used for different content")
		}
		return previous.value, previous.err
	}
	if c.ctx.Err() != nil {
		return nil, errors.New("application is stopping")
	}
	value, err := c.command(ctx, cmd)
	if len(c.requests) >= 256 {
		for key := range c.requests {
			delete(c.requests, key)
			break
		}
	}
	c.requests[cmd.RequestID] = requestResult{signature: sig, value: value, err: err}
	return value, err
}

func (c *Core) command(ctx context.Context, cmd webui.Command) (any, error) {
	switch cmd.Name {
	case "network.configure":
		var v struct {
			Mode     string `json:"mode"`
			Hostname string `json:"hostname"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		p := c.profileCopy()
		if v.Mode == "lan" {
			return nil, errors.New("LAN pairing is not enabled in this draft; device-key verification is still under test")
		}
		if v.Mode != "tailnet" && v.Mode != "none" {
			return nil, errors.New("choose tailnet or none")
		}
		if c.nodeCopy() != nil && (v.Mode != p.Settings.Network || (v.Hostname != "" && v.Hostname != p.Settings.Hostname)) {
			return nil, errors.New("stop soba before changing the active network or node name")
		}
		p.Settings.Network = v.Mode
		if v.Hostname != "" {
			p.Settings.Hostname = v.Hostname
		}
		if e := c.saveProfile(p); e != nil {
			return nil, e
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		if v.Mode == "tailnet" {
			return nil, c.startNetwork(ctx)
		}
		return nil, nil
	case "network.login":
		n := c.nodeCopy()
		if n == nil {
			return nil, errors.New("activate a network first")
		}
		if e := n.Login(ctx); e != nil {
			return nil, errors.New("could not request interactive login")
		}
		for i := 0; i < 20; i++ {
			st, e := c.current(ctx)
			if e == nil && st.AuthURL != "" {
				return map[string]string{"authUrl": st.AuthURL}, nil
			}
			if e == nil && st.Snapshot.Running {
				return map[string]string{"state": "connected"}, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		return nil, errors.New("login link is not ready; retry login shortly")
	case "peer.trust":
		var v struct {
			PeerID  string `json:"peerId"`
			Trusted bool   `json:"trusted"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		p := c.profileCopy()
		peer, e := c.currentPeer(ctx, v.PeerID)
		if v.Trusted && e != nil {
			return nil, e
		}
		old, had := c.trust(v.PeerID)
		if had == v.Trusted {
			if !v.Trusted {
				c.revokePeer(v.PeerID)
			}
			return nil, nil
		}
		if v.Trusted {
			if len(p.Peers) >= 128 {
				return nil, errors.New("trusted peer capacity reached")
			}
			g := uint64(time.Now().UnixNano())
			if g == 0 {
				g = 1
			}
			p.Peers = append(p.Peers, Trust{ID: peer.ID, Name: peer.DNSName, Network: p.Settings.Network, Generation: g})
		} else {
			filtered := p.Peers[:0]
			for _, t := range p.Peers {
				if t.ID != v.PeerID {
					filtered = append(filtered, t)
				}
			}
			p.Peers = filtered
		}
		if e := c.saveProfile(p); e != nil {
			return nil, e
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		if v.Trusted {
			t, _ := c.trust(v.PeerID)
			return nil, c.bindTransferPeer(t)
		}
		_ = old
		c.revokePeer(v.PeerID)
		return nil, nil
	case "peer.autosave":
		var v struct {
			PeerID    string `json:"peerId"`
			Enabled   bool   `json:"enabled"`
			Paused    bool   `json:"paused"`
			Directory string `json:"directory"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		t, ok := c.trust(v.PeerID)
		if !ok {
			return nil, errors.New("approve this exact peer identity first")
		}
		if _, e := c.currentPeer(ctx, v.PeerID); e != nil {
			return nil, e
		}
		if v.Enabled && !filepath.IsAbs(v.Directory) {
			return nil, errors.New("select an absolute receive directory")
		}
		if v.Enabled {
			if err := c.transfers.SetReceivePolicy(transfer.ReceivePolicy{Peer: transfer.Peer{ID: t.ID, Generation: t.Generation}, Destination: v.Directory, AutoAccept: true}); err != nil {
				return nil, err
			}
		} else {
			if err := c.transfers.RemoveReceivePolicy(t.ID); err != nil {
				return nil, err
			}
		}
		p := c.profileCopy()
		for i := range p.Peers {
			if p.Peers[i].ID == t.ID {
				p.Peers[i].Paused = v.Paused
			}
		}
		if e := c.saveProfile(p); e != nil {
			return nil, e
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		err := c.transfers.PausePeer(t.ID, v.Paused)
		if v.Paused {
			c.mu.RLock()
			ps := c.peerServer
			var outgoing []*outgoingBatch
			for _, b := range c.outgoing {
				if b.PeerID == t.ID {
					outgoing = append(outgoing, b)
				}
			}
			c.mu.RUnlock()
			if ps != nil {
				ps.revoke(t.ID)
			}
			for _, b := range outgoing {
				b.stop()
			}
		}
		return nil, err
	case "peer.reconnect":
		var v struct {
			PeerID string `json:"peerId"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		return nil, c.probePeer(ctx, v.PeerID)
	case "settings.update":
		var v struct {
			Locale           string  `json:"locale"`
			Theme            string  `json:"theme"`
			ReceiveDirectory *string `json:"receiveDirectory"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		p := c.profileCopy()
		if v.Locale != "" {
			p.Settings.Locale = v.Locale
		}
		if v.Theme != "" {
			p.Settings.Theme = v.Theme
		}
		if v.ReceiveDirectory != nil {
			if *v.ReceiveDirectory != "" && !filepath.IsAbs(*v.ReceiveDirectory) {
				return nil, errors.New("receive directory must be absolute")
			}
			p.Settings.ReceiveDirectory = *v.ReceiveDirectory
		}
		if e := c.saveProfile(p); e != nil {
			return nil, e
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		return nil, nil
	case "message.send":
		return c.sendMessageCommand(ctx, cmd.Payload)
	case "transfer.accept", "transfer.decline", "transfer.cancel", "transfer.retry", "transfer.forget":
		return c.transferCommand(ctx, cmd.Name, cmd.Payload)
	case "transfer.send":
		var v struct {
			PeerID string   `json:"peerId"`
			Paths  []string `json:"paths"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		return c.SendPaths(ctx, v.PeerID, v.Paths)
	case "service.share", "service.connect":
		return c.startServiceCommand(ctx, cmd.Name, cmd.Payload)
	case "service.stop":
		return c.stopServiceCommand(cmd.Payload)
	default:
		return nil, fmt.Errorf("unknown command %q", cmd.Name)
	}
}

func (c *Core) revokePeer(id string) {
	_ = c.transfers.RevokePeer(id)
	c.stopPeerServices(id)
	c.mu.Lock()
	for _, b := range c.outgoing {
		if b.PeerID == id {
			b.stop()
		}
	}
	c.mu.Unlock()
}

// Keep profile utilities reused by command-only integrations in one boundary.
var _ = config.ValidPeerID
