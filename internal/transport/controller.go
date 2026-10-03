package transport

import "sync"

// Limits are finite resource budgets. Zero selects the documented default;
// negative values fail closed. Current callbacks must be concurrency-safe.
type Limits struct {
	TCPConnections, TCPPerPolicy, TCPPerPeer                        int64
	UDPSessions, UDPPerPolicy, UDPQueuedBytes, UDPPolicyQueuedBytes int64
	UDPQueuePackets                                                 int64
}

func DefaultLimits() Limits {
	return Limits{TCPConnections: defaultTCPConnections, TCPPerPolicy: defaultTCPPerPolicy, TCPPerPeer: 64,
		UDPSessions: defaultUDPSessions, UDPPerPolicy: defaultMaxUDPSessions, UDPQueuedBytes: defaultUDPQueuedBytes,
		UDPPolicyQueuedBytes: defaultUDPPolicyQueuedBytes, UDPQueuePackets: defaultUDPQueueSize}
}

// Controller accounts for all transports belonging to one resource owner.
// Reservations allocate only for admitted work, regardless of configured limits.
// Lower limits affect subsequent reservations, never the lifetime of held ones.
type Controller struct {
	current         func() Limits
	mu              sync.Mutex
	tcp, udp, bytes int64
	policies, peers map[string]int64
}

func NewController(current func() Limits) *Controller { return &Controller{current: current} }

var defaultController = NewController(nil)

func (c *Controller) limits() Limits {
	out := DefaultLimits()
	if c != nil && c.current != nil {
		next := c.current()
		for _, pair := range []struct{ dst, src *int64 }{
			{&out.TCPConnections, &next.TCPConnections}, {&out.TCPPerPolicy, &next.TCPPerPolicy},
			{&out.TCPPerPeer, &next.TCPPerPeer}, {&out.UDPSessions, &next.UDPSessions},
			{&out.UDPPerPolicy, &next.UDPPerPolicy}, {&out.UDPQueuedBytes, &next.UDPQueuedBytes},
			{&out.UDPPolicyQueuedBytes, &next.UDPPolicyQueuedBytes}, {&out.UDPQueuePackets, &next.UDPQueuePackets},
		} {
			if *pair.src != 0 {
				*pair.dst = *pair.src
			}
		}
	}
	return out
}

func controllerOrDefault(c *Controller) *Controller {
	if c == nil {
		return defaultController
	}
	return c
}

// AdmitTCP reserves the aggregate budget and any identified policy and peer.
// Empty peer IDs are used before authentication; AdmitTCPPeer binds them later.
func (c *Controller) AdmitTCP(policy, peer string) (func(), bool) {
	c = controllerOrDefault(c)
	limits := c.limits()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp >= limits.TCPConnections || (policy != "" && c.policies[policy] >= limits.TCPPerPolicy) || (peer != "" && c.peers[peer] >= limits.TCPPerPeer) {
		return nil, false
	}
	c.tcp++
	if policy != "" {
		if c.policies == nil {
			c.policies = make(map[string]int64)
		}
		c.policies[policy]++
	}
	if peer != "" {
		if c.peers == nil {
			c.peers = make(map[string]int64)
		}
		c.peers[peer]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.tcp--
			decrementUsage(c.policies, policy)
			decrementUsage(c.peers, peer)
		})
	}, true
}

// AdmitTCPPeer reserves the authenticated peer budget of an already-admitted
// TCP flow. Its release is independent of the aggregate/policy reservation.
func (c *Controller) AdmitTCPPeer(peer string) (func(), bool) {
	c = controllerOrDefault(c)
	limits := c.limits()
	c.mu.Lock()
	defer c.mu.Unlock()
	if peer == "" || c.peers[peer] >= limits.TCPPerPeer {
		return nil, false
	}
	if c.peers == nil {
		c.peers = make(map[string]int64)
	}
	c.peers[peer]++
	var once sync.Once
	return func() { once.Do(func() { c.mu.Lock(); decrementUsage(c.peers, peer); c.mu.Unlock() }) }, true
}

func decrementUsage(usage map[string]int64, key string) {
	if key == "" {
		return
	}
	if usage[key] <= 1 {
		delete(usage, key)
	} else {
		usage[key]--
	}
}

func (c *Controller) reserveSession(limit int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.udp >= limit {
		return false
	}
	c.udp++
	return true
}
func (c *Controller) releaseSession() { c.mu.Lock(); c.udp--; c.mu.Unlock() }
func (c *Controller) reserveBytes(n int, limit int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n < 0 || limit < c.bytes || int64(n) > limit-c.bytes {
		return false
	}
	c.bytes += int64(n)
	return true
}
func (c *Controller) releaseBytes(n int) { c.mu.Lock(); c.bytes -= int64(n); c.mu.Unlock() }

type Usage struct{ TCPConnections, UDPSessions, UDPQueuedBytes int64 }

func (c *Controller) Usage() Usage {
	c = controllerOrDefault(c)
	c.mu.Lock()
	defer c.mu.Unlock()
	return Usage{TCPConnections: c.tcp, UDPSessions: c.udp, UDPQueuedBytes: c.bytes}
}
