package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
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
		c.reserveServicePort("tcp", web.Port())
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
		return c.stopApplication(), nil
	}
	var cmd webui.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		return nil, errors.New("unknown command; use soba help")
	}
	return c.Command(ctx, cmd)
}
func (c *Core) Done() <-chan struct{} { return c.ctx.Done() }

// The owning process closes Core after Done, as with the existing IPC stop.
// Keeping one shutdown path closes relay, peer, transfer and service work too.
func (c *Core) stopApplication() map[string]string {
	c.networkReady.Store(false)
	c.mu.Lock()
	c.networkState = "stopping"
	c.mu.Unlock()
	c.cancel()
	return map[string]string{"state": "stopping"}
}

func (c *Core) startNetwork(ctx context.Context) error {
	if c.nodeCopy() != nil {
		return nil
	}
	p := c.profileCopy()
	if p.Settings.Network != "tailnet" && p.Settings.Network != "lan" {
		return errors.New("choose a network before connecting")
	}
	if c.attemptedNetwork == "" && c.transferNetwork != p.Settings.Network {
		if err := c.resetTransferNetwork(p); err != nil {
			return err
		}
	}
	var n NetworkBackend
	var e error
	if p.Settings.Network == "lan" {
		store := c.lanStoreCopy()
		if store == nil {
			return errors.New("explicit LAN setup is required before connecting")
		}
		factory := c.lanFactory
		if factory == nil {
			factory = c.newLANBackend
		}
		n, e = factory(store)
	} else {
		n, e = c.factory(c.dir, p.Settings.Hostname)
	}
	if e != nil {
		return codedLANError(e)
	}
	// Both engines can change process-global netstack settings during Start,
	// including a Start that later fails. A failed attempt must not permit a
	// different engine or node identity to initialize in this process.
	c.mu.Lock()
	c.attemptedNetwork, c.attemptedHostname = p.Settings.Network, p.Settings.Hostname
	c.mu.Unlock()
	if e = n.Start(); e != nil {
		_ = n.Close()
		return errors.New("could not start network; private identity state was retained")
	}
	c.mu.Lock()
	c.node = n
	c.networkState = "starting"
	c.networkError = ""
	c.networkErrorCode = ""
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
				if c.networkFatal == "" {
					c.networkErrorCode = ""
				}
				if c.networkFatal != "" {
					c.networkState, c.networkError = "error", c.networkFatal
				}
			} else {
				c.networkState = st.Backend
				c.networkError = ""
				c.networkErrorCode = ""
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
				c.revalidateProxies(st)
				c.refreshPeers(st)
				c.runStartup(c.ctx)
			} else {
				c.suspendServices()
				c.suspendSavedProxies()
				c.stopAllProxies()
			}
		}
		c.expireServices()
		c.expireProxies()
		c.op.Unlock()
	}
}

func (c *Core) Snapshot(ctx context.Context) (map[string]any, error) {
	p := c.profileCopy()
	c.mu.RLock()
	state, reason, reasonCode := c.networkState, c.networkError, c.networkErrorCode
	messages := append([]Message(nil), c.messages...)
	confirmed := map[string]time.Time{}
	for id, t := range c.confirmed {
		confirmed[id] = t
	}
	c.mu.RUnlock()
	peers := []map[string]any{}
	networkRead := false
	reservedPorts := []uint16{54543, 54544, 54545}
	c.mu.RLock()
	for _, endpoint := range c.proxyReservedPorts() {
		reservedPorts = append(reservedPorts, endpoint.Port())
	}
	if c.web != nil {
		reservedPorts = append(reservedPorts, c.web.Port())
	}
	c.mu.RUnlock()
	if st, e := c.current(ctx); e == nil {
		networkRead = true
		reservedPorts = append(reservedPorts, st.ReservedPorts...)
		lanNames := map[string]string{}
		if node, ok := c.nodeCopy().(lanNetworkBackend); ok {
			for _, peer := range node.PublicPeers() {
				lanNames[peer.Key] = peer.Name
			}
		}
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
			if publicName := lanNames[peer.ID]; publicName != "" {
				name = publicName
			}
			if name == "" {
				name = peer.ID
			}
			bridge := freshDiscoveryCheck(confirmed[peer.ID], time.Now())
			peers = append(peers, map[string]any{"id": peer.ID, "name": name, "networks": []string{p.Settings.Network}, "online": peer.Online || bridge, "verified": st.Snapshot.Running, "trusted": ok, "path": "unknown", "bridge": bridge, "discovery": c.discoveryObservation(peer.ID), "address": address, "fingerprint": peer.ID, "autosave": map[string]any{"enabled": trusted.Autosave, "paused": trusted.Paused, "directory": trusted.Directory}})
		}
	}
	if !networkRead && p.Settings.Network == "lan" {
		// Offline management must still let the user revoke saved pairings
		// after an address/certificate/startup failure. Saved identity metadata
		// is not current verification or an online/reachability claim.
		if saved := c.lanStoreCopy(); saved != nil {
			for _, peer := range saved.copy().Trust.Peers {
				trusted, ok := c.trust(peer.Key)
				peers = append(peers, map[string]any{"id": peer.Key, "name": peer.Name, "networks": []string{"lan"}, "online": false, "verified": false, "trusted": ok, "path": "unknown", "bridge": false, "address": "", "fingerprint": peer.Key, "autosave": map[string]any{"enabled": trusted.Autosave, "paused": trusted.Paused, "directory": trusted.Directory}})
			}
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
	return map[string]any{"version": c.version, "processId": os.Getpid(), "self": map[string]any{"name": p.Settings.Hostname, "status": state, "error": reason, "errorCode": reasonCode, "receiveDirectory": p.Settings.ReceiveDirectory}, "peers": peers, "messages": messages, "transfers": c.transferViews(), "services": services, "shares": shares, "proxies": c.proxyViews(), "startup": c.startupView(), "savedProxies": c.savedProxyView(), "availableServices": c.discoveredViews(), "reservedPorts": reservedPorts, "settings": p.Settings, "servicePresets": servicePresets(), "limits": c.capacityView(), "lan": c.lanStatus()}, nil
}

// Command deduplicates requests independently of the mutation lock. Slow file
// staging and task heartbeats must not hold up unrelated control operations.
func (c *Core) Command(ctx context.Context, cmd webui.Command) (any, error) {
	if len(cmd.RequestID) < 1 || len(cmd.RequestID) > 128 {
		return nil, errors.New("requestId is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Credential reveal is an explicit read, never retained in request history.
	if cmd.Name == "proxy.reveal" {
		return c.executeCommand(ctx, cmd)
	}
	digest := sha256.Sum256(append([]byte(cmd.Name+"\x00"), cmd.Payload...))
	sig := hex.EncodeToString(digest[:])
	c.requestMu.Lock()
	if previous, ok := c.requests[cmd.RequestID]; ok {
		c.requestMu.Unlock()
		if previous.signature != sig {
			return nil, errors.New("request ID was already used for different content")
		}
		return previous.value, previous.err
	}
	if pending := c.inflightRequests[cmd.RequestID]; pending != nil {
		c.requestMu.Unlock()
		if pending.signature != sig {
			return nil, errors.New("request ID was already used for different content")
		}
		select {
		case <-pending.done:
			return pending.result.value, pending.result.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.ctx.Err() != nil {
		c.requestMu.Unlock()
		return nil, errors.New("application is stopping")
	}
	if c.inflightRequests == nil {
		c.inflightRequests = make(map[string]*pendingRequest)
	}
	pending := &pendingRequest{signature: sig, done: make(chan struct{})}
	c.inflightRequests[cmd.RequestID] = pending
	c.requestMu.Unlock()

	value, err := c.executeCommand(ctx, cmd)
	// In particular, SendPaths has released its beginWork registration before
	// publishing here. Close may hold c.op while joining that work.
	c.requestMu.Lock()
	if c.requests == nil {
		c.requests = make(map[string]requestResult)
	}
	if len(c.requests) >= 256 {
		for key := range c.requests {
			delete(c.requests, key)
			break
		}
	}
	result := requestResult{signature: sig, value: value, err: err}
	c.requests[cmd.RequestID] = result
	pending.result = result
	delete(c.inflightRequests, cmd.RequestID)
	close(pending.done)
	c.requestMu.Unlock()
	return value, err
}

func (c *Core) executeCommand(ctx context.Context, cmd webui.Command) (any, error) {
	switch cmd.Name {
	case "transfer.send", "services.renew", "application.stop":
		// These operations provide their own atomic admission under c.mu and
		// do not mutate the serialized profile or transport configuration.
	default:
		c.op.Lock()
		defer c.op.Unlock()
	}
	// A caller may have timed out while waiting for another mutation. Never
	// apply a queued edit after its caller has cancelled it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.ctx.Err() != nil {
		return nil, errors.New("application is stopping")
	}
	return c.command(ctx, cmd)
}

func (c *Core) command(ctx context.Context, cmd webui.Command) (any, error) {
	switch cmd.Name {
	case "startup.list", "startup.preview", "startup.save", "startup.disable":
		return c.startupCommand(ctx, cmd.Name, cmd.Payload)
	case "proxy.save", "proxy.generate", "proxy.saved.list", "proxy.saved.start", "proxy.saved.delete", "proxy.saved.disable", "proxy.reveal":
		return c.savedProxyCommand(ctx, cmd.Name, cmd.Payload)
	case "rustdesk.preview", "rustdesk.save", "rustdesk.settings", "client.settings":
		return c.clientHelperCommand(ctx, cmd.Name, cmd.Payload)
	case "proxy.preview", "proxy.start", "proxy.stop", "proxy.list":
		return c.proxyCommand(ctx, cmd.Name, cmd.Payload)
	case "diagnostics.run":
		return c.diagnoseCommand(ctx, cmd.Payload)
	case "service.save":
		return c.saveDefinition(cmd.Payload)
	case "service.delete":
		return c.deleteDefinition(cmd.Payload)
	case "profile.export", "profile.import.preview", "profile.import":
		return c.profileDefinitionsCommand(cmd.Name, cmd.Payload)
	case "group.list", "group.save":
		return c.groupCommand(cmd.Name, cmd.Payload)
	case "service.selection", "services.start", "services.stop", "services.renew", "services.ready":
		return c.selectionCommand(ctx, cmd.Name, cmd.Payload)
	case "service.stop-shares":
		return c.stopSharesCommand(cmd.Payload)
	case "policy.config", "policy.preview", "policy.apply":
		return c.capacityCommand(cmd.Name, cmd.Payload)
	case "service.list":
		return c.listServices(cmd.Payload)
	case "lan.addresses", "lan.inspect", "lan.identity", "lan.invite", "lan.cancel", "lan.join", "lan.revoke":
		return c.lanCommand(ctx, cmd.Name, cmd.Payload)
	case "application.stop":
		var input struct{}
		if err := decodePayload(cmd.Payload, &input); err != nil {
			return nil, err
		}
		return c.stopApplication(), nil
	case "network.configure":
		var v struct {
			Mode     string        `json:"mode"`
			Hostname string        `json:"hostname"`
			LAN      *LANSelection `json:"lan,omitempty"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		p := c.profileCopy()
		if v.Mode != "tailnet" && v.Mode != "lan" && v.Mode != "none" {
			return nil, errors.New("choose tailnet, lan or none")
		}
		if err := validateCapacityBackend(v.Mode, c.capacityPolicy()); err != nil {
			return nil, err
		}
		if v.Mode != "lan" && v.LAN != nil {
			return nil, errors.New("LAN relay settings require LAN mode")
		}
		c.mu.RLock()
		attemptedMode, attemptedName := c.attemptedNetwork, c.attemptedHostname
		c.mu.RUnlock()
		if attemptedMode != "" && (v.Mode != attemptedMode || (v.Hostname != "" && v.Hostname != attemptedName)) {
			return nil, &lanCommandError{"network_restart_required", "stop soba, then start with --offline before changing the active network or node name"}
		}
		if c.nodeCopy() != nil && (v.Mode != p.Settings.Network || (v.Hostname != "" && v.Hostname != p.Settings.Hostname)) {
			return nil, &lanCommandError{"network_restart_required", "stop soba, then start with --offline before changing the active network or node name"}
		}
		p.Settings.Network = v.Mode
		if v.Hostname != "" {
			p.Settings.Hostname = v.Hostname
		}
		if e := validateProfile(p); e != nil {
			return nil, e
		}
		if v.Mode == "lan" {
			if e := c.configureLAN(v.LAN); e != nil {
				return nil, codedLANError(e)
			}
		}
		saveErr := c.saveProfile(p)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		if saveErr != nil {
			return nil, saveErr
		}
		if v.Mode == "tailnet" || v.Mode == "lan" {
			err := c.startNetwork(ctx)
			if err != nil {
				c.mu.Lock()
				c.networkState, c.networkError, c.networkErrorCode = "error", err.Error(), networkErrorCode(err)
				c.mu.Unlock()
			}
			return nil, err
		}
		return nil, nil
	case "network.login", "network.login.status":
		return c.loginCommand(ctx, cmd.Name == "network.login", cmd.Payload)
	case "network.logout":
		var input struct{}
		if err := decodePayload(cmd.Payload, &input); err != nil {
			return nil, err
		}
		return c.logoutTailnet(ctx)
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
		var revocationErr error
		if !v.Trusted {
			revocationErr = c.revokeStartupPeer(v.PeerID)
			if revocationErr != nil && !errors.Is(revocationErr, config.ErrAtomicCommitted) {
				return nil, revocationErr
			}
		}
		_, had := c.trust(v.PeerID)
		if had == v.Trusted {
			if !v.Trusted {
				c.revokePeer(v.PeerID)
			}
			return nil, revocationErr
		}
		if v.Trusted {
			for _, existing := range p.Peers {
				if existing.ID == v.PeerID && existing.Network != p.Settings.Network {
					return nil, errors.New("this peer ID belongs to an approval in another network; switch to that network to review it first")
				}
			}
			if int64(len(p.Peers)) >= c.limit("logical", "trustedPeers") {
				return nil, &localCommandError{"peer_capacity", "trusted peer limit reached; raise trustedPeers in capacity settings"}
			}
			if c.trustGeneration == ^uint64(0) {
				return nil, errors.New("peer approval generation is exhausted")
			}
			c.trustGeneration++
			g := c.trustGeneration
			p.Peers = append(p.Peers, Trust{ID: peer.ID, Name: peer.DNSName, Network: p.Settings.Network, Generation: g, RevocationEpoch: c.reviewPeerEpochs([]string{peer.ID})[peer.ID]})
		} else {
			filtered := p.Peers[:0]
			for _, t := range p.Peers {
				if t.ID != v.PeerID || t.Network != p.Settings.Network {
					filtered = append(filtered, t)
				}
			}
			p.Peers = filtered
		}
		if v.Trusted {
			// Preflight is read-only: no receiver binding exists until the
			// profile is durable. All binding mutations and Close share c.op.
			t := p.Peers[len(p.Peers)-1]
			if e := c.transfers.ValidatePeerBinding(transfer.Peer{ID: t.ID, Generation: t.Generation}); e != nil {
				return nil, e
			}
		}
		if !v.Trusted {
			// Journal publication closes authority before any fallible profile save.
			c.mu.Lock()
			c.profile = p
			c.mu.Unlock()
			c.revokePeer(v.PeerID)
		}
		saveErr := c.saveProfile(p)
		if !atomicPublished(saveErr) {
			if revocationErr != nil {
				return nil, privateAtomicError(revocationErr, saveErr)
			}
			return nil, errors.Join(revocationErr, saveErr)
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		if v.Trusted {
			t, _ := c.trust(v.PeerID)
			return nil, errors.Join(saveErr, c.bindTransferPeer(t))
		}
		if revocationErr != nil {
			return nil, privateAtomicError(revocationErr, saveErr)
		}
		return nil, saveErr
	case "peer.autosave":
		var v struct {
			PeerID    string  `json:"peerId"`
			Enabled   *bool   `json:"enabled"`
			Paused    *bool   `json:"paused"`
			Directory *string `json:"directory"`
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
		// Omitted values are merged while c.op is held. A client changing one
		// setting must not write back a stale snapshot of the other settings.
		next := t
		if v.Enabled != nil {
			next.Autosave = *v.Enabled
		}
		if v.Paused != nil {
			next.Paused = *v.Paused
		}
		if v.Directory != nil {
			next.Directory = *v.Directory
		} else if v.Enabled != nil && *v.Enabled && next.Directory == "" {
			next.Directory = c.profileCopy().Settings.ReceiveDirectory
		}
		if (next.Autosave || next.Directory != "") && !filepath.IsAbs(next.Directory) {
			return nil, &localCommandError{"autosave_directory_required", "select an absolute receive directory before enabling automatic saving"}
		}
		peer := transfer.Peer{ID: t.ID, Generation: t.Generation}
		var policy *transfer.ReceivePolicy
		if v.Enabled != nil || (v.Directory != nil && next.Autosave) {
			policy = &transfer.ReceivePolicy{Peer: peer, Destination: next.Directory, AutoAccept: next.Autosave}
		}
		var saveErr error
		if err := c.transfers.UpdateReceiveSettings(peer, policy, next.Paused, func(policies []transfer.ReceivePolicy) error {
			saveErr = (receiveStore{c}).savePolicies(policies, &next)
			if atomicPublished(saveErr) {
				return nil
			}
			return saveErr
		}); err != nil {
			return nil, err
		}
		if next.Paused {
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
		return nil, saveErr
	case "discovery.refresh":
		return c.refreshDiscoveryCommand(ctx, cmd.Payload)
	case "peer.reconnect":
		var v struct {
			PeerID string `json:"peerId"`
		}
		if e := decodePayload(cmd.Payload, &v); e != nil {
			return nil, e
		}
		return nil, c.reconnectPeerServices(ctx, v.PeerID)
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
		saveErr := c.saveProfile(p)
		if !atomicPublished(saveErr) {
			return nil, saveErr
		}
		c.mu.Lock()
		c.profile = p
		c.mu.Unlock()
		return nil, saveErr
	case "message.history.preview", "message.history.cleanup":
		return c.messageHistoryCommand(cmd.Name, cmd.Payload)
	case "message.list", "transfer.list":
		return c.listHistory(cmd.Name, cmd.Payload)
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
	case "service.config":
		return c.serviceConfiguration(cmd.Payload)
	case "service.stop":
		return c.stopServiceCommand(cmd.Payload)
	default:
		return nil, fmt.Errorf("unknown command %q", cmd.Name)
	}
}

func (c *Core) revokePeer(id string) {
	c.mu.RLock()
	retries := c.peerRefreshRetries
	c.mu.RUnlock()
	if retries != nil {
		retries.forget(id)
	}
	c.stopPeerProxies(id)
	_ = c.transfers.RevokePeer(id)
	c.stopPeerServices(id)
	c.mu.Lock()
	ps := c.peerServer
	delete(c.confirmed, id)
	delete(c.discovered, id)
	delete(c.discoveryObservations, id)
	for _, b := range c.outgoing {
		if b.PeerID == id {
			b.stop()
		}
	}
	c.mu.Unlock()
	if ps != nil {
		ps.revoke(id)
	}
}

// Keep profile utilities reused by command-only integrations in one boundary.
var _ = config.ValidPeerID
