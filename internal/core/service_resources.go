package core

import (
	"github.com/webkaz-labs/sobalink/internal/ranges"
	"github.com/webkaz-labs/sobalink/internal/transport"
)

func (c *Core) serviceResources() *transport.Controller {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resources == nil {
		c.resources = transport.NewController(c.transportResourceLimits)
	}
	return c.resources
}

func (c *Core) transportResourceLimits() transport.Limits {
	p := c.capacityPolicy()
	n := func(key string) int64 { return p.Number("resources", key) }
	return transport.Limits{TCPConnections: n("tcpConnections"), TCPPerPolicy: n("tcpPerPolicy"), TCPPerPeer: n("tcpPerPeer"), UDPSessions: n("udpSessions"), UDPPerPolicy: n("udpPerPolicy"), UDPQueuedBytes: n("udpQueuedBytes"), UDPPolicyQueuedBytes: n("udpPolicyQueuedBytes"), UDPQueuePackets: n("udpQueuePackets")}
}

func (c *Core) rangeFlowLimits() ranges.Limits {
	p := c.capacityPolicy()
	return ranges.Limits{Global: p.Number("resources", "tcpConnections"), PerPolicy: p.Number("resources", "tcpPerPolicy"), PerPeer: p.Number("resources", "tcpPerPeer")}
}
