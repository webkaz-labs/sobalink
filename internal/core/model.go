// Package core is the shared application boundary for soba's local Web and CLI.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/transport"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

const PeerPort = 54544
const DiscoveryPort = 54543

type Settings struct {
	Locale           string `json:"locale"`
	Theme            string `json:"theme"`
	Network          string `json:"network"`
	Hostname         string `json:"hostname"`
	ReceiveDirectory string `json:"receiveDirectory"`
}
type Trust struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Network         string `json:"network"`
	Generation      uint64 `json:"generation"`
	Autosave        bool   `json:"autosave"`
	Directory       string `json:"directory,omitempty"`
	Paused          bool   `json:"paused"`
	RevocationEpoch string `json:"revocationEpoch,omitempty"`
}
type Profile struct {
	Version  int            `json:"version"`
	Settings Settings       `json:"settings"`
	Peers    []Trust        `json:"peers"`
	Services []ServiceSpec  `json:"services"`
	Groups   []ServiceGroup `json:"groups,omitempty"`
}
type ServiceSpec struct {
	ID              string   `json:"id"`
	Backend         string   `json:"backend,omitempty"`
	Name            string   `json:"name"`
	Direction       string   `json:"direction"`
	Network         string   `json:"network"`
	Ports           string   `json:"ports"`
	ExcludePorts    string   `json:"excludePorts,omitempty"`
	LocalPort       int      `json:"localPort,omitempty"`
	LoopbackHost    string   `json:"loopbackHost,omitempty"`
	Lifetime        string   `json:"lifetime,omitempty"`
	PeerID          string   `json:"peerId,omitempty"`
	PeerIDs         []string `json:"peerIds,omitempty"`
	TTLSeconds      int      `json:"ttlSeconds"`
	Purpose         string   `json:"purpose"`
	Discoverable    bool     `json:"discoverable"`
	ServiceID       string   `json:"serviceId,omitempty"`
	ServiceRevision string   `json:"serviceRevision,omitempty"`
}
type Message struct {
	ID        string    `json:"id"`
	PeerID    string    `json:"peerId"`
	Direction string    `json:"direction"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Status    string    `json:"status"`
}
type NetworkBackend interface {
	identity.Backend
	identity.InboundBackend
	identity.DiscoveryIdentity
}
type NodeFactory func(string, string) (NetworkBackend, error)
type Options struct {
	Directory, Version string
	NodeFactory        NodeFactory
	SkipNetworkStart   bool
}

type Core struct {
	endpointJob                *endpointFollowingJob // mu: bounded coordinator, joined before op on Close
	managedCleanupPending      bool                  // protected by mu; ancillary durability only
	managedCleanupError        error                 // protected by mu; retained reopen/repair failures
	managedDenied              map[string]bool       // terminal and in-flight removal denial, protected by mu
	managedRemoval             *managedRemovalOwner  // reduction-only owner, protected by mu
	lanStartNonce              string
	lanStartWriteRevision      atomic.Uint64
	lanStartUncertain          atomic.Bool
	favoritesUncertain         bool // protected by op; cleared only by a confirmed whole-store write
	startup                    startupStore
	startupPending             map[string]string
	startupStates              map[string]string
	startupSuppressed          bool
	savedProxies               savedProxyStore
	savedProxyPending          map[string]string
	savedProxyRuns             map[string]savedProxyRun
	mu                         sync.RWMutex
	op                         sync.Mutex
	dir, version               string
	atomicWrite                func(string, []byte) error
	profile                    Profile
	capacity                   capacity.Policy
	resources                  *transport.Controller
	node                       NetworkBackend
	factory                    NodeFactory
	ctx                        context.Context
	cancel                     context.CancelFunc
	networkState, networkError string
	networkErrorCode           string
	networkFatal               string
	attemptedNetwork           string
	attemptedHostname          string
	trustGeneration            uint64
	transferNetwork            string
	networkReady               atomic.Bool
	peerServer                 *peerServer
	peerHTTPTransport          *peerHTTPFront
	transfers                  *transfer.Manager
	diskSpace                  *diskspace.Guard
	messages                   []Message
	messageHistoryUncertain    bool // protected by mu; cleared only by a successful whole-history save
	outgoing                   map[string]*outgoingBatch
	orphanSpoolBytes           int64
	orphanSpoolEntries         int64
	orphanSpoolError           string
	confirmed                  map[string]time.Time
	refreshMu                  sync.Mutex
	refreshCursor              string
	refreshAdmissionCursor     string
	peerRefreshRetries         *peerRefreshScheduler
	discovered                 map[string][]RemoteService
	discoveryObservations      map[string]DiscoveryObservation
	active                     map[string]*activeService
	serviceStates              map[string]string
	serviceFailures            map[string]serviceFailure
	serviceDiagnostics         map[string]ServiceDiagnostic
	proxies                    map[string]*activeProxy
	proxyStart                 proxyStarter
	diagnosticsDial            transport.Dialer
	portProposalListen         func(context.Context, string, string) (io.Closer, error)
	rangeState                 *rangeState
	requestMu                  sync.Mutex
	inflightRequests           map[string]*pendingRequest
	planSources                sourcePlanner
	requests                   map[string]requestResult
	web                        *webui.Server
	wg                         sync.WaitGroup
	closing                    bool
	closeOnce                  sync.Once
	closeErr                   error
	directLAN                  *directLANStore
	contextControl             *contextControlOwner
	contextUpgrade             *contextUpgradeJob
	lan                        *lanStore
	lanFactory                 func(*lanStore) (lanNetworkBackend, error)
	lanAddresses               func() ([]LANLocalAddress, error)
}
type pendingRequest struct {
	signature string
	done      chan struct{}
	result    requestResult
}

type requestResult struct {
	signature string
	value     any
	err       error
}

func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func DefaultDir() (string, error) { d, e := os.UserConfigDir(); return filepath.Join(d, "sobalink"), e }

func Open(parent context.Context, opts Options) (*Core, error) {
	if opts.Directory == "" {
		return nil, errors.New("private state directory required")
	}
	if err := config.SecureDir(opts.Directory); err != nil {
		return nil, err
	}
	p := Profile{Version: 1, Settings: Settings{Locale: "auto", Theme: "system", Network: "none", Hostname: "sobalink-" + randomID()[:8]}, Peers: []Trust{}, Services: []ServiceSpec{}}
	limits, err := readCapacityPolicy(opts.Directory)
	if err != nil {
		return nil, err
	}
	profileLoadErr := readBoundedPrivateJSON(filepath.Join(opts.Directory, "sobalink.json"), limits.Number("resources", "profileBytes"), &p)
	if profileLoadErr != nil && !errors.Is(profileLoadErr, os.ErrNotExist) {
		return nil, profileLoadErr
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	if err := validateCapacityBackend(p.Settings.Network, limits); err != nil {
		return nil, err
	}
	loadedProfileRevision := privateRevision(p)
	ctx, cancel := context.WithCancel(parent)
	c := &Core{lanStartNonce: randomID(), dir: opts.Directory, version: opts.Version, profile: p, ctx: ctx, cancel: cancel, networkState: "idle", outgoing: map[string]*outgoingBatch{}, confirmed: map[string]time.Time{}, peerRefreshRetries: newPeerRefreshScheduler(), discovered: map[string][]RemoteService{}, active: map[string]*activeService{}, serviceStates: map[string]string{}, requests: map[string]requestResult{}}
	c.capacity = limits
	for _, peer := range p.Peers {
		if peer.Generation > c.trustGeneration {
			c.trustGeneration = peer.Generation
		}
	}
	if err := c.loadStartup(true); err != nil {
		cancel()
		return nil, err
	}
	c.factory = opts.NodeFactory
	if c.factory == nil {
		c.factory = func(dir, name string) (NetworkBackend, error) { return identity.New(dir, name) }
	}
	lan, err := readLANStoreWithPolicy(filepath.Join(opts.Directory, "lan.json"), limits)
	if err != nil {
		cancel()
		return nil, err
	}
	c.lan = lan
	direct, err := readDirectLANStore(filepath.Join(opts.Directory, "direct-lan.json"), limits.Number("resources", "lanStateBytes"), limits.Number("logical", "trustedPeers"))
	if err != nil {
		cancel()
		return nil, err
	}
	c.directLAN = direct
	if direct != nil {
		direct.write = c.writeAtomic
	}
	cleanupErr := c.reconcileTerminalDirectLAN()
	if err := c.reconcileDirectLANTrust(); err != nil {
		cancel()
		return nil, err
	}
	if err := c.reconcileLANTrust(); err != nil {
		cancel()
		return nil, err
	}
	c.armReconciledStartup(opts.SkipNetworkStart)
	c.inventoryOutgoingSpool()
	p = c.profileCopy()
	if cleanupErr == nil && (errors.Is(profileLoadErr, os.ErrNotExist) || privateRevision(p) != loadedProfileRevision) {
		if err := c.writeProfile(p); err != nil {
			cancel()
			return nil, err
		}
	}
	for _, peer := range p.Peers {
		if peer.Generation > c.trustGeneration {
			c.trustGeneration = peer.Generation
		}
	}
	accountingLimits := receiveAccountingLimits(limits)
	m, err := transfer.NewManager(transfer.Options{Context: ctx, Limits: receiveTransferLimits(limits), PolicyStore: receiveStore{c}, ExistingState: profileLoadErr == nil, AccountingLimits: accountingLimits, AccountingStore: transfer.FileReceiveAccountingStore{Path: filepath.Join(opts.Directory, "receive-accounting.json"), Limits: accountingLimits}})
	if err != nil {
		cancel()
		return nil, err
	}
	c.transfers = m
	c.transferNetwork = p.Settings.Network
	for _, peer := range p.Peers {
		if peer.Network != p.Settings.Network || c.managedPeerDenied(peer.ID) {
			continue
		}
		if err := c.bindTransferPeer(peer); err != nil {
			m.Close()
			cancel()
			return nil, err
		}
	}
	if err := c.loadMessages(); err != nil {
		m.Close()
		cancel()
		return nil, err
	}
	c.wg.Add(1)
	go c.maintain()
	// Explicit saved network choice permits reconnect; no saved service grant
	// or transfer is restarted or renewed after process restart.
	if cleanupErr == nil && !opts.SkipNetworkStart && (p.Settings.Network == "tailnet" || p.Settings.Network == "lan" || p.Settings.Network == "direct-lan" || p.Settings.Network == "mixed") {
		c.op.Lock()
		startErr := c.startNetwork(ctx)
		c.op.Unlock()
		if err := startErr; err != nil {
			c.mu.Lock()
			c.networkState = "error"
			c.networkError = err.Error()
			c.networkErrorCode = networkErrorCode(err)
			c.mu.Unlock()
		}
	}
	return c, nil
}

func validateProfile(p Profile) error {
	if p.Version != 1 {
		return errors.New("unsupported sobalink profile version")
	}
	if p.Settings.Network != "none" && p.Settings.Network != "tailnet" && p.Settings.Network != "lan" && p.Settings.Network != "direct-lan" && p.Settings.Network != "mixed" {
		return errors.New("network must be none, tailnet, lan, direct-lan or mixed")
	}
	if p.Settings.Locale != "auto" && p.Settings.Locale != "ja" && p.Settings.Locale != "en" {
		return errors.New("locale must be auto, ja or en")
	}
	if p.Settings.Theme != "system" && p.Settings.Theme != "light" && p.Settings.Theme != "dark" {
		return errors.New("theme must be system, light or dark")
	}
	if !config.ValidName(p.Settings.Hostname) || len(p.Settings.Hostname) > 63 || strings.ContainsAny(p.Settings.Hostname, "_ ") {
		return errors.New("invalid node hostname")
	}
	seen := map[string]bool{}
	for _, p := range p.Peers {
		if !config.ValidPeerID(p.ID) || p.Generation == 0 || seen[p.ID] || len(p.Name) > 253 || (p.Network != "tailnet" && p.Network != "lan" && p.Network != "direct-lan" && p.Network != "mixed") {
			return errors.New("invalid trusted peer")
		}
		seen[p.ID] = true
		if p.Autosave && (!filepath.IsAbs(p.Directory) || p.Directory == "") {
			return errors.New("autosave requires an absolute destination")
		}
	}
	for _, service := range p.Services {
		// Positive legacy TTLs remain finite without rewriting saved records or
		// changing their revision merely because the profile was reopened.
		if service.Lifetime != "" {
			if _, err := serviceLifetime(service.Lifetime, service.TTLSeconds, service.Direction); err != nil {
				return err
			}
		}
		if _, err := serviceLoopback(service.LoopbackHost); err != nil {
			return err
		}
	}
	return validateGroups(p)
}
func (c *Core) saveProfile(p Profile) error {
	if err := validateProfile(p); err != nil {
		return err
	}
	old := c.profileCopy()
	if err := c.validateGroupCapacity(p, old); err != nil {
		return err
	}
	if len(p.Services) > len(old.Services) && int64(len(p.Services)) > c.limit("logical", "savedServices") {
		return &localCommandError{"service_capacity", "saved service limit reached; raise savedServices in capacity settings"}
	}
	if len(p.Peers) > len(old.Peers) && int64(len(p.Peers)) > c.limit("logical", "trustedPeers") {
		return &localCommandError{"peer_capacity", "trusted peer limit reached; raise trustedPeers in capacity settings"}
	}
	return c.writeProfile(p)
}
func cloneProfile(p Profile) Profile {
	p.Groups = cloneGroups(p.Groups)
	p.Peers = append([]Trust(nil), p.Peers...)
	p.Services = append([]ServiceSpec(nil), p.Services...)
	for i := range p.Services {
		p.Services[i].PeerIDs = append([]string(nil), p.Services[i].PeerIDs...)
	}
	return p
}
func (c *Core) profileCopy() Profile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneProfile(c.profile)
}
func (c *Core) trust(id string) (Trust, bool) {
	if c.managedPeerDenied(id) {
		return Trust{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.profile.Peers {
		if p.ID == id && p.Network == c.profile.Settings.Network {
			return p, true
		}
	}
	return Trust{}, false
}
func (c *Core) nodeCopy() NetworkBackend { c.mu.RLock(); defer c.mu.RUnlock(); return c.node }

func (c *Core) bindTransferPeer(p Trust) error {
	if c.managedPeerDenied(p.ID) {
		return &localCommandError{"direct_lan_peer_revoked", "this peer has a terminal direct LAN removal record"}
	}
	peer := transfer.Peer{ID: p.ID, Generation: p.Generation}
	if err := c.transfers.BindPeer(peer); err != nil {
		return err
	}
	return c.transfers.PausePeer(p.ID, p.Paused)
}

func (c *Core) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.close() })
	return c.closeErr
}
func (c *Core) beginWork() (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.ctx.Err() != nil {
		return nil, errors.New("application is stopping")
	}
	c.wg.Add(1)
	return c.wg.Done, nil
}
func (c *Core) close() error {
	c.mu.Lock()
	c.closing = true
	peerHTTP := c.peerHTTPTransport
	control := c.contextControl
	upgrade := c.contextUpgrade
	endpoint := c.endpointJob
	c.mu.Unlock()
	if control != nil {
		control.requestClose()
	}
	if peerHTTP != nil {
		peerHTTP.close()
	}
	c.cancel()
	// Join the bounded controller before taking its mutation lock.
	if upgrade != nil {
		upgrade.cancel()
		<-upgrade.done
	}
	if endpoint != nil {
		endpoint.cancel()
		<-endpoint.done
	}
	c.op.Lock()
	defer c.op.Unlock()
	c.stopAllServices()
	c.stopAllProxies()
	c.mu.Lock()
	ps := c.peerServer
	c.peerServer = nil
	node := c.node
	c.node = nil
	web := c.web
	c.web = nil
	c.mu.Unlock()
	var errs []error
	errs = append(errs, c.stopContextControlLocked())
	if ps != nil {
		errs = append(errs, ps.Close())
	}
	if web != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		errs = append(errs, web.Close(ctx))
		cancel()
	}
	errs = append(errs, c.transfers.Close())
	if node != nil {
		errs = append(errs, node.Close())
	}
	c.wg.Wait()
	return errors.Join(errs...)
}

func (c *Core) current(ctx context.Context) (identity.State, error) {
	c.mu.RLock()
	closing := c.closing
	c.mu.RUnlock()
	if closing {
		return identity.State{}, errors.New("application is stopping")
	}
	if err := ctx.Err(); err != nil {
		return identity.State{}, err
	}
	if err := c.ctx.Err(); err != nil {
		return identity.State{}, err
	}
	node := c.nodeCopy()
	if node == nil {
		return identity.State{}, errors.New("choose and activate a network first")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := node.State(ctx)
	if ctx.Err() != nil {
		return identity.State{}, ctx.Err()
	}
	if c.ctx.Err() != nil {
		return identity.State{}, c.ctx.Err()
	}
	return state, err
}
func (c *Core) currentPeer(ctx context.Context, id string) (policy.Peer, error) {
	st, e := c.current(ctx)
	if e != nil || !st.Snapshot.Running {
		return policy.Peer{}, errors.New("network is not connected")
	}
	for _, p := range st.Snapshot.Peers {
		if p.ID == id && !p.Expired {
			return p, nil
		}
	}
	return policy.Peer{}, errors.New("peer is no longer present with the approved identity")
}

func (c *Core) authenticated(ctx context.Context, source netip.AddrPort) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := c.ctx.Err(); err != nil {
		return "", err
	}
	n := c.nodeCopy()
	if n == nil {
		return "", errors.New("network unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	id, e := n.WhoIs(ctx, source)
	if e != nil || ctx.Err() != nil || c.ctx.Err() != nil {
		return "", errors.New("current peer identity unavailable")
	}
	peer, e := c.currentPeer(ctx, id)
	if e != nil {
		return "", e
	}
	for _, ip := range peer.IPs {
		if ip == source.Addr() {
			return id, nil
		}
	}
	return "", errors.New("peer address no longer matches identity")
}

func (c *Core) dial(ctx context.Context, id, network string, port int) (net.Conn, error) {
	peer, e := c.currentPeer(ctx, id)
	if e != nil {
		return nil, e
	}
	if len(peer.IPs) == 0 {
		return nil, errors.New("peer has no current address")
	}
	n := c.nodeCopy()
	if n == nil {
		return nil, errors.New("network unavailable")
	}
	p := &policy.Policy{Rules: []policy.Rule{{PeerID: id, Host: peer.DNSName, Port: port, Network: network}}, Source: func(ctx context.Context) (policy.Snapshot, error) { s, e := c.current(ctx); return s.Snapshot, e }, DialIP: n.DialIP}
	if peer.DNSName == "" {
		p.Rules[0].Host = peer.IPs[0].String()
	}
	return p.Dial(ctx, network, config.Address(p.Rules[0].Host, port))
}

func decodePayload(raw json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid command payload")
	}
	return nil
}
