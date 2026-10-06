package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/webkaz-labs/sobalink/internal/capacity"
)

type directLANRuntimeResources struct{ Flows, Listeners, Invitations, PacketQueue int }

func directRuntimeResources(p capacity.Policy) directLANRuntimeResources {
	bounded := func(v int64) int {
		if v > int64(math.MaxInt) {
			return math.MaxInt
		}
		return int(v)
	}
	tcp, udp := p.Number("resources", "tcpConnections"), p.Number("resources", "udpSessions")
	total := tcp + udp
	if total < tcp || total > int64(math.MaxInt)-8 {
		total = int64(math.MaxInt) - 8
	}
	return directLANRuntimeResources{bounded(total + 8), bounded(p.Number("resources", "materializedListeners") + 2), bounded(p.Number("resources", "transferPending")), bounded(p.Number("resources", "udpQueuePackets"))}
}
func (s *directLANStore) currentCapacity() *lanStoreLimits {
	if limits := s.limits.Load(); limits != nil {
		return limits
	}
	return &lanStoreLimits{peers: s.peers, bytes: s.bytes}
}
func (s *directLANStore) transportPeerLimit() int {
	v := s.currentCapacity().peers
	if v >= int64(math.MaxInt) {
		return 0
	}
	return int(v)
}

// Core.mu is held; direct pairing persistence only takes the private-store
// mutex. The atomic limits callback never re-enters Core or the store lock.
func (c *Core) applyDirectLANCapacityLocked(policy capacity.Policy, publish func() error) error {
	s := c.directLAN
	if s == nil {
		return publish()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limits := selectedLANLimits(policy)
	data, e := json.MarshalIndent(s.state, "", "  ")
	if e != nil {
		return e
	}
	bytes := int64(len(data)) + 1
	if info, e := os.Lstat(s.path); e == nil {
		bytes = max(bytes, info.Size())
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if bytes > limits.bytes {
		return &localCommandError{"policy_in_use", fmt.Sprintf("lanStateBytes must hold the current %d-byte private direct LAN state", bytes)}
	}
	e = publish()
	if atomicPublished(e) {
		s.limits.Store(limits)
	}
	return e
}
