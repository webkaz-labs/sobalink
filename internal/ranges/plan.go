package ranges

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

var (
	tailnet4    = netip.MustParsePrefix("100.64.0.0/10")
	tailnet6    = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	reserved, _ = NewSet([]Interval{{DiscoveryPort, PairingPort}})
)

// Policy shares ports on one exact embedded-node address and numeric loopback.
// TargetPort optionally maps a single exposed port to a different local port.
type Policy struct {
	ID         string
	Network    string
	Address    netip.Addr
	Ports      Set
	Exclude    Set
	Loopback   netip.Addr
	TargetPort uint16
	PeerIDs    []string
	ExpiresAt  time.Time
}

type compiledPolicy struct {
	policy    Policy
	effective Set
}

// Plan owns an immutable, compact validated snapshot. BuildPlan copies all
// mutable inputs. TCP policies in a plan need one shared fallback dispatcher
// and zero per-port listeners, regardless of the number of covered ports.
type Plan struct {
	selfIPs   map[netip.Addr]struct{}
	policies  []compiledPolicy
	intervals int
}

type Description struct {
	ID             string
	Network        string
	Address        netip.Addr
	Ports          string
	Exclude        string
	Effective      string
	EffectivePorts uint32
	Intervals      int
	ExpiresAt      time.Time
}

func tailnetIP(ip netip.Addr) bool {
	return ip.Zone() == "" && ip != netip.MustParseAddr("100.100.100.100") && (tailnet4.Contains(ip) || tailnet6.Contains(ip))
}

func loopbackIP(ip netip.Addr) bool {
	return ip == netip.MustParseAddr("127.0.0.1") || ip == netip.IPv6Loopback()
}

func BuildPlan(selfIPs []netip.Addr, policies []Policy) (*Plan, error) {
	if len(selfIPs) > 16 {
		return nil, errors.New("too many embedded node addresses")
	}
	if len(policies) > MaxPolicies {
		return nil, errors.New("too many range policies (maximum 64)")
	}
	p := &Plan{selfIPs: make(map[netip.Addr]struct{}, len(selfIPs)), policies: make([]compiledPolicy, 0, len(policies))}
	for _, ip := range selfIPs {
		if !tailnetIP(ip) {
			return nil, errors.New("embedded node address must be in the tailnet namespace")
		}
		p.selfIPs[ip] = struct{}{}
	}
	ids := make(map[string]bool, len(policies))
	configuredIntervals := 0
	for _, rule := range policies {
		if strings.TrimSpace(rule.ID) == "" || len(rule.ID) > 128 || ids[rule.ID] {
			return nil, errors.New("policy IDs must be nonempty, unique and at most 128 bytes")
		}
		ids[rule.ID] = true
		if rule.Network != "tcp" && rule.Network != "udp" {
			return nil, errors.New("range policy network must be tcp or udp")
		}
		if _, own := p.selfIPs[rule.Address]; !own || !tailnetIP(rule.Address) {
			return nil, errors.New("range policy must use a current embedded node address")
		}
		if !loopbackIP(rule.Loopback) {
			return nil, errors.New("range policy target must be exactly 127.0.0.1 or ::1")
		}
		if rule.ExpiresAt.IsZero() {
			return nil, errors.New("range policy must have an explicit expiry")
		}
		if len(rule.PeerIDs) == 0 || len(rule.PeerIDs) > MaxPolicyPeers {
			return nil, errors.New("range policy requires 1..32 pinned peer IDs")
		}
		rule.PeerIDs = append([]string(nil), rule.PeerIDs...)
		sort.Strings(rule.PeerIDs)
		for i, id := range rule.PeerIDs {
			if strings.TrimSpace(id) == "" || len(id) > 256 || (i > 0 && id == rule.PeerIDs[i-1]) {
				return nil, errors.New("pinned peer IDs must be nonempty and unique")
			}
		}
		var err error
		rule.Ports, err = NewSet(rule.Ports.intervals)
		if err != nil {
			return nil, err
		}
		rule.Exclude, err = NewSet(rule.Exclude.intervals)
		if err != nil {
			return nil, err
		}
		configuredIntervals += rule.Ports.IntervalCount() + rule.Exclude.IntervalCount()
		if configuredIntervals > MaxIntervals {
			return nil, errors.New("too many configured port intervals (maximum 256)")
		}
		effective, err := rule.Ports.Excluding(rule.Exclude)
		if err != nil {
			return nil, err
		}
		effective, err = effective.Excluding(reserved)
		if err != nil {
			return nil, err
		}
		if rule.TargetPort != 0 && (effective.Count() != 1 || reserved.Contains(rule.TargetPort)) {
			return nil, errors.New("mapped sharing requires one exposed port and a non-reserved target port")
		}
		if effective.Empty() {
			return nil, fmt.Errorf("policy %q has no effective service ports", rule.ID)
		}
		p.intervals += effective.IntervalCount()
		if p.intervals > MaxIntervals {
			return nil, errors.New("too many effective port intervals (maximum 256)")
		}
		for _, previous := range p.policies {
			if previous.policy.Network == rule.Network && previous.policy.Address == rule.Address && previous.effective.Overlaps(effective) {
				return nil, fmt.Errorf("policies %q and %q overlap on the same protocol and address", previous.policy.ID, rule.ID)
			}
		}
		p.policies = append(p.policies, compiledPolicy{policy: rule, effective: effective})
	}
	return p, nil
}

func (p *Plan) PolicyCount() int {
	if p == nil {
		return 0
	}
	return len(p.policies)
}
func (p *Plan) IntervalCount() int {
	if p == nil {
		return 0
	}
	return p.intervals
}
func (p *Plan) Describe() []Description {
	if p == nil {
		return nil
	}
	out := make([]Description, 0, len(p.policies))
	for _, r := range p.policies {
		out = append(out, Description{ID: r.policy.ID, Network: r.policy.Network, Address: r.policy.Address, Ports: r.policy.Ports.String(), Exclude: r.policy.Exclude.String(), Effective: r.effective.String(), EffectivePorts: r.effective.Count(), Intervals: r.effective.IntervalCount(), ExpiresAt: r.policy.ExpiresAt})
	}
	return out
}

// ExpandMaterialized preflights the aggregate count before expanding the
// selected policies. TCP virtual sharing must not use this method. Existing
// UDP/OS-local listeners must be subtracted from limit by the caller.
func (p *Plan) ExpandMaterialized(ids []string, limit int) (map[string][]uint16, error) {
	if p == nil || limit < 1 || limit > MaxMaterializedListeners {
		return nil, errors.New("materialized listener limit must be in 1..64")
	}
	if len(ids) > MaxPolicies {
		return nil, errors.New("too many materialized policy IDs")
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if wanted[id] {
			return nil, errors.New("duplicate materialized policy ID")
		}
		wanted[id] = true
	}
	var count uint32
	for _, r := range p.policies {
		if wanted[r.policy.ID] {
			count += r.effective.Count()
		}
	}
	if count > uint32(limit) {
		return nil, errors.New("policies exceed aggregate materialized listener limit")
	}
	out := make(map[string][]uint16, len(wanted))
	for _, r := range p.policies {
		if wanted[r.policy.ID] {
			ports, err := r.effective.Expand(limit)
			if err != nil {
				return nil, err
			}
			out[r.policy.ID] = ports
		}
	}
	if len(out) != len(wanted) {
		return nil, errors.New("unknown materialized policy ID")
	}
	return out, nil
}

func equalPolicies(a, b compiledPolicy) bool {
	x, y := a.policy, b.policy
	if x.ID != y.ID || x.Network != y.Network || x.Address != y.Address || x.Loopback != y.Loopback || x.TargetPort != y.TargetPort || !x.ExpiresAt.Equal(y.ExpiresAt) || !equalSets(x.Ports, y.Ports) || !equalSets(x.Exclude, y.Exclude) || len(x.PeerIDs) != len(y.PeerIDs) {
		return false
	}
	for i := range x.PeerIDs {
		if x.PeerIDs[i] != y.PeerIDs[i] {
			return false
		}
	}
	return true
}
