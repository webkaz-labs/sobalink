package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/config"
	"github.com/webkaz-labs/tsnet-bridge/internal/discovery"
	"github.com/webkaz-labs/tsnet-bridge/internal/identity"
	"github.com/webkaz-labs/tsnet-bridge/internal/policy"
)

// DiscoveryTTL limits how long a caller may offer a previously observed share.
// Catalogs are never persisted or used as authorization for the data connection.
const DiscoveryTTL = 15 * time.Second
const discoveryPeerLimit = 128
const discoveryConcurrency = 4

type DiscoveredService struct {
	PeerID      string    `json:"peer_id"`
	PeerHost    string    `json:"peer_host"`
	ID          string    `json:"id"`
	Purpose     string    `json:"purpose"`
	Network     string    `json:"network"`
	Port        int       `json:"port"`
	ExpiresAt   time.Time `json:"expires_at"`
	CheckedAt   time.Time `json:"checked_at"`
	Application string    `json:"application"`
}
type DiscoveryPeer struct {
	PeerID    string    `json:"peer_id"`
	PeerHost  string    `json:"peer_host"`
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
}
type ServiceCatalog struct {
	Services  []DiscoveredService `json:"services"`
	Peers     []DiscoveryPeer     `json:"peers"`
	CheckedAt time.Time           `json:"checked_at"`
	Truncated bool                `json:"truncated"`
}

// Private authorization state is never serialized into the remote DTO.
type advertisedShare struct {
	service discovery.Service
	allowed []config.PeerRef
	pins    map[netip.Addr]string
	life    *lifetime
	done    <-chan struct{}
}
type discoveryResult struct {
	peer     DiscoveryPeer
	services []DiscoveredService
}
type localDiscovery struct {
	mu       sync.RWMutex
	shares   []advertisedShare
	listener net.Listener
	cancel   context.CancelFunc
	address  string
	state    string
}

// Called only by the serialized rule manager. All data seen by remote handlers
// is copied and guarded independently from the mutable runtime rule map.
func (s *Service) publishDiscovery() {
	if s.rules == nil {
		return
	}
	var shares []advertisedShare
	for _, r := range s.rules.entries {
		if r.config.Direction != "share" || !r.config.Discoverable || !r.desired || r.server == nil || r.life == nil || r.life.check() != nil || r.status.State != "ready" {
			continue
		}
		if r.discoveryID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				continue
			}
			r.discoveryID = hex.EncodeToString(id[:])
		}
		shares = append(shares, advertisedShare{
			service: discovery.Service{ID: r.discoveryID, Purpose: r.config.Purpose, Network: r.config.Network, Port: r.config.ListenPort, ExpiresAt: r.status.ExpiresAt, Application: "unverified"},
			allowed: append([]config.PeerRef(nil), r.config.AllowedPeers...), pins: r.discoveryPins, life: r.life, done: r.server.Done(),
		})
	}
	if s.discovery == nil {
		if len(shares) == 0 {
			return
		}
		s.discovery = &localDiscovery{state: "unavailable"}
	}
	d := s.discovery
	d.mu.Lock()
	d.shares = shares
	d.mu.Unlock()
	if len(shares) == 0 {
		s.closeDiscovery()
	}
	d.mu.RLock()
	state := d.state
	d.mu.RUnlock()
	s.mu.Lock()
	s.status.Discovery = state
	s.mu.Unlock()
}

func (s *Service) refreshDiscoveryListener(st identity.State) {
	for _, r := range s.rules.entries {
		if r.config.Discoverable && r.config.Direction == "share" && r.desired && r.server != nil && r.life != nil && r.life.check() == nil {
			s.ensureDiscovery(st)
			s.publishDiscovery()
			return
		}
	}
}

func (s *Service) ensureDiscovery(st identity.State) {
	if s.discovery == nil {
		s.discovery = &localDiscovery{state: "unavailable"}
	}
	d := s.discovery
	inbound, ok := s.Node.(identity.InboundBackend)
	if !ok {
		return
	}
	if _, ok := s.Node.(identity.DiscoveryIdentity); !ok {
		return
	}
	var ip netip.Addr
	for _, candidate := range st.IPs {
		if config.TailnetIP(candidate) {
			ip = candidate
			if ip.Is4() {
				break
			}
		}
	}
	if !st.Snapshot.Running || !ip.IsValid() {
		return
	}
	address := config.Address(ip.String(), discovery.Port)
	d.mu.RLock()
	same := d.listener != nil && d.address == address
	d.mu.RUnlock()
	if same {
		return
	}
	s.closeDiscovery()
	listener, err := inbound.Listen("tcp", address)
	if err != nil {
		d.mu.Lock()
		d.state = "unavailable"
		d.mu.Unlock()
		return
	}
	parent := s.runCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	d.mu.Lock()
	d.listener = listener
	d.cancel = cancel
	d.address = address
	d.state = "listening"
	d.mu.Unlock()
	go func() {
		_ = discovery.Serve(ctx, listener, s.discoveryServices)
		d.mu.Lock()
		if d.listener == listener {
			d.listener = nil
			d.state = "unavailable"
		}
		d.mu.Unlock()
	}()
}
func (s *Service) closeDiscovery() {
	d := s.discovery
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel := d.cancel
	d.listener = nil
	d.cancel = nil
	d.address = ""
	d.state = "disabled"
	d.shares = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Serve owns listener and active-connection closure. Cancellation is
	// non-blocking even if a native listener is slow to close.
}

func (s *Service) discoveryServices(ctx context.Context, source netip.AddrPort) ([]discovery.Service, error) {
	auth, ok := s.Node.(identity.DiscoveryIdentity)
	if !ok || !config.TailnetIP(source.Addr()) || source.Port() == 0 {
		return nil, errIdentity
	}
	caller, err := auth.WhoIs(ctx, source)
	if err != nil || !config.ValidPeerID(caller) {
		return nil, errIdentity
	}
	st, err := s.Node.State(ctx)
	if err != nil || ctx.Err() != nil || !st.Snapshot.Running {
		return nil, errIdentity
	}
	// WhoIs alone is not a grant. Also match the currently observed address to
	// exactly this stable ID before consulting the grant's start-time pins.
	sourceValid := authorizeSource(st.Snapshot, []config.PeerRef{{ID: caller}}, source.Addr()) == nil
	d := s.discovery
	if d == nil {
		return nil, errIdentity
	}
	d.mu.RLock()
	shares := append([]advertisedShare(nil), d.shares...)
	d.mu.RUnlock()
	services := make([]discovery.Service, 0, len(shares))
	for _, share := range shares {
		if share.life.check() != nil {
			continue
		}
		if validateAllowed(st.Snapshot, share.allowed) != nil || discoveryPinsRevoked(st.Snapshot, share.pins) {
			share.life.fail("peer-identity-changed")
			continue
		}
		if !sourceValid || share.pins[source.Addr()] != caller || authorizePinnedSource(st.Snapshot, share.allowed, share.pins, source.Addr()) != nil {
			continue
		}
		select {
		case <-share.done:
			continue
		default:
		}
		service := share.service
		share.life.mu.RLock()
		if expiry := share.life.expires; !expiry.IsZero() && expiry.UTC().Before(service.ExpiresAt) {
			service.ExpiresAt = expiry.UTC()
		}
		if lease := share.life.lease; !lease.IsZero() && lease.UTC().Before(service.ExpiresAt) {
			service.ExpiresAt = lease.UTC()
		}
		share.life.mu.RUnlock()
		if !service.ExpiresAt.After(time.Now()) {
			continue
		}
		services = append(services, service)
	}
	if !sourceValid {
		return nil, errIdentity
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	return services, nil
}

func discoveryHost(p policy.Peer) string {
	if p.DNSName != "" && config.ValidHost(p.DNSName) {
		return strings.TrimSuffix(p.DNSName, ".")
	}
	for _, ip := range p.IPs {
		if config.TailnetIP(ip) {
			return ip.String()
		}
	}
	return ""
}

// Each catalog is a new, bounded read. No background scanner, persistent cache,
// control server or application-port probing is involved. Running independently
// from rule commands keeps stop and expiry responsive while peers time out.
func (s *Service) discoverServices(ctx context.Context, onlyID string) (ServiceCatalog, error) {
	out := ServiceCatalog{Services: []DiscoveredService{}, Peers: []DiscoveryPeer{}, CheckedAt: time.Now().UTC()}
	if s.Status().Backend != "Running" {
		return out, errors.New("tailnet service information unavailable; sign in and retry")
	}
	if onlyID != "" && !config.ValidPeerID(onlyID) {
		return out, errors.New("invalid discovery peer ID")
	}
	s.mu.Lock()
	if s.discoveryQueries == nil {
		s.discoveryQueries = make(chan struct{}, 1)
	}
	slots := s.discoveryQueries
	s.mu.Unlock()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		return out, errors.New("service discovery is already running; retry shortly")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	st, err := s.Node.State(ctx)
	if err != nil || !st.Snapshot.Running || ctx.Err() != nil {
		return out, errors.New("tailnet service information unavailable; sign in and retry")
	}
	peers := []policy.Peer{}
	seen := map[string]bool{}
	for _, p := range st.Snapshot.Peers {
		if !config.ValidPeerID(p.ID) || p.Expired || discoveryHost(p) == "" || seen[p.ID] || (onlyID != "" && p.ID != onlyID) {
			continue
		}
		seen[p.ID] = true
		peers = append(peers, p)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	if len(peers) > discoveryPeerLimit {
		out.Truncated = true
		peers = peers[:discoveryPeerLimit]
	}
	results := make(chan discoveryResult, len(peers))
	jobs := make(chan policy.Peer)
	var wg sync.WaitGroup
	for range discoveryConcurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if ctx.Err() != nil {
					results <- discoveryResult{peer: DiscoveryPeer{PeerID: p.ID, PeerHost: discoveryHost(p), State: "unavailable", CheckedAt: time.Now().UTC()}}
					continue
				}
				results <- s.queryDiscoveryPeer(ctx, p)
			}
		}()
	}
	for _, p := range peers {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(results)
	for r := range results {
		out.Peers = append(out.Peers, r.peer)
		out.Services = append(out.Services, r.services...)
	}
	sort.Slice(out.Peers, func(i, j int) bool { return out.Peers[i].PeerID < out.Peers[j].PeerID })
	sort.Slice(out.Services, func(i, j int) bool {
		a, b := out.Services[i], out.Services[j]
		if a.PeerHost != b.PeerHost {
			return a.PeerHost < b.PeerHost
		}
		if a.Purpose != b.Purpose {
			return a.Purpose < b.Purpose
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.ID < b.ID
	})
	if len(out.Services) > 128 {
		out.Truncated = true
		out.Services = out.Services[:128]
	}
	out.CheckedAt = time.Now().UTC()
	if ctx.Err() != nil {
		out.Truncated = true
	}
	return out, nil
}

func (s *Service) queryDiscoveryPeer(ctx context.Context, peer policy.Peer) discoveryResult {
	host := discoveryHost(peer)
	out := discoveryResult{peer: DiscoveryPeer{PeerID: peer.ID, PeerHost: host, State: "unavailable"}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := &policy.Policy{Rules: []policy.Rule{{PeerID: peer.ID, Host: host, Port: discovery.Port, Network: "tcp"}}, Source: func(ctx context.Context) (policy.Snapshot, error) { st, e := s.Node.State(ctx); return st.Snapshot, e }, DialIP: s.Node.DialIP}
	endpoint, err := p.Resolve(ctx, "tcp", config.Address(host, discovery.Port))
	if err == nil {
		var response discovery.Response
		response, err = discovery.Query(ctx, p.Dial, endpoint.String())
		if err == nil {
			// Identity can change while the body is read; validate once more before
			// accepting even an otherwise well-formed response as current.
			err = p.Validate(ctx, "tcp", endpoint.String())
			if err == nil {
				out.peer.State = "confirmed"
				checked := time.Now().UTC()
				for _, service := range response.Services {
					out.services = append(out.services, DiscoveredService{PeerID: peer.ID, PeerHost: host, ID: service.ID, Purpose: service.Purpose, Network: service.Network, Port: service.Port, ExpiresAt: service.ExpiresAt, CheckedAt: checked, Application: "unverified"})
				}
			}
		}
	}
	if errors.Is(err, discovery.ErrUnsupported) {
		out.peer.State = "unsupported"
	}
	out.peer.CheckedAt = time.Now().UTC()
	return out
}

// A successful current snapshot may revoke a start-time identity/IP pin. An
// unrelated caller cannot cause revocation; only authoritative peer data can.
func discoveryPinsRevoked(snapshot policy.Snapshot, pins map[netip.Addr]string) bool {
	for ip, id := range pins {
		if id == "" {
			continue
		}
		for _, peer := range snapshot.Peers {
			for _, address := range peer.IPs {
				if address == ip && (peer.ID != id || peer.Expired) {
					return true
				}
			}
		}
	}
	return false
}
