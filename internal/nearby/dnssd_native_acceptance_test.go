//go:build darwin && cgo

package nearby

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func emitDNSServiceCapability(t *testing.T, r dnssdCapabilityResult) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dnssdNativeDiagnosticBatch.observe(r.FailureKind)
	t.Log("NEARBY_DNSSD_CAPABILITY " + string(data))
	if r.Status == "unavailable" || r.Status == "not_run" {
		t.Skip("OS DNS-SD capability not verified: " + r.Reason)
	}
	if r.Status != "verified" {
		t.Fatal("OS DNS-SD capability failed: " + r.Reason)
	}
}
func TestDNSServiceLocalOnlyCapability(t *testing.T) {
	requireDNSServiceBatchPermission(t)
	result := dnssdCapability("os_local_only")
	if os.Getenv("SOBALINK_DNSSD_LOCAL_ONLY") != "1" {
		result.Status, result.Reason = "not_run", "opt_in_required"
	} else {
		result = runDNSServiceLocalOnlyCapability()
	}
	emitDNSServiceCapability(t, result)
}
func TestDNSServiceSelectedLoopbackCapability(t *testing.T) {
	requireDNSServiceBatchPermission(t)
	result := dnssdCapability("selected_loopback_os")
	if os.Getenv("SOBALINK_DNSSD_SELECTED_LOOPBACK") != "1" {
		result.Status, result.Reason = "not_run", "opt_in_required"
	} else {
		result = runDNSServiceSelectedLoopbackCapability()
	}
	emitDNSServiceCapability(t, result)
}

// This separate opt-in is authorized only for a bounded hosted synthetic test.
// Ambiguous/no suitable scope is unavailable, never an implicit fallback.
func TestDNSServiceSelectedInterfaceCapability(t *testing.T) {
	requireDNSServiceBatchPermission(t)
	result := dnssdCapability("selected_private_interface_os")
	if os.Getenv("SOBALINK_DNSSD_SELECTED_INTERFACE") != "1" {
		result.Status, result.Reason = "not_run", "opt_in_required"
	} else {
		scope, err := dnssdSinglePrivateScope()
		if err != nil {
			result.Reason = "private_scope_unavailable"
		} else {
			result = runDNSServiceScopeCapability(scope, discoveryPolicy{}, "selected_private_interface_os")
		}
	}
	emitDNSServiceCapability(t, result)
}
func dnssdSinglePrivateScope() (Scope, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return Scope{}, ErrUnsupported
	}
	var selected Scope
	matches := 0
	for _, ifi := range interfaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := ifi.Addrs()
		if err != nil {
			return Scope{}, ErrUnsupported
		}
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			scope := Scope{InterfaceIndex: ifi.Index, LocalAddress: prefix.Addr(), Prefix: prefix.Masked()}
			if scope.Validate() != nil {
				continue
			}
			matches++
			selected = scope
		}
	}
	if matches != 1 {
		return Scope{}, ErrUnsupported
	}
	return selected, nil
}
func dnssdSyntheticConfigs(scope Scope) ([2]Config, error) {
	var cfgs [2]Config
	for i := range cfgs {
		id, err := NewSessionID()
		if err != nil {
			return cfgs, err
		}
		cfgs[i] = Config{Scope: scope, Advertisement: Advertisement{SessionID: id, Alias: "Example", Protocol: Protocol, Capability: Capability, Bootstrap: netip.AddrPortFrom(scope.LocalAddress, uint16(15443+i))}}
	}
	return cfgs, nil
}

// No production Config can select LocalOnly. This test changes only the native
// API index in its private backend registration and never fabricates a Scope.
type dnssdCapabilityPeer struct {
	backend  dnssdBackend
	done     chan struct{}
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	closeErr error
	started  bool
}

func (p *dnssdCapabilityPeer) close() {
	p.once.Do(func() {
		go func() { e := p.backend.Close(); p.mu.Lock(); p.closeErr = e; p.mu.Unlock(); close(p.closed) }()
	})
}
func runDNSServiceLocalOnlyCapability() (result dnssdCapabilityResult) {
	result = dnssdCapability("os_local_only")
	scope, err := isolatedLoopbackScope()
	if err != nil {
		result.Reason = "loopback_scope_unavailable"
		return
	}
	cfgs, err := dnssdSyntheticConfigs(scope)
	if err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	var peers []*dnssdCapabilityPeer
	defer func() {
		cancel()
		for _, p := range peers {
			p.close()
			if !p.started {
				close(p.done)
			}
		}
		timer := time.NewTimer(closeJoinTimeout)
		defer timer.Stop()
		for _, p := range peers {
			for _, done := range []<-chan struct{}{p.closed, p.done} {
				select {
				case <-done:
				case <-timer.C:
					result.Status, result.Reason = "failed", "owners_not_joined"
					return
				}
			}
			p.mu.Lock()
			e := p.closeErr
			p.mu.Unlock()
			if e != nil {
				result.Status, result.Reason = "failed", "cleanup_uncertain"
				return
			}
		}
		result.OwnersJoined = len(peers) == 2
		if result.Status == "verified" && !result.OwnersJoined {
			result.Status, result.Reason = "failed", "owners_missing"
		}
	}()
	var regs [2]dnssdRegistration
	for i, cfg := range cfgs {
		reg, e := dnssdRegistrationFor(cfg, isolatedLoopbackPolicy())
		if e != nil {
			result.Status, result.Reason = "failed", "fixture_unavailable"
			return
		}
		reg.InterfaceIndex = ^uint32(0)
		regs[i] = reg
		backend, e := newNativeDNSServiceBackend(reg)
		if backend != nil {
			p := &dnssdCapabilityPeer{backend: backend, done: make(chan struct{}), closed: make(chan struct{})}
			peers = append(peers, p)
		}
		if e != nil {
			dnssdCapabilityError(&result, e)
			return
		}
	}
	type report struct {
		index int
		kind  string
		err   error
	}
	reports := make(chan report, 6)
	for i, p := range peers {
		p.started = true
		go func(i int, p *dnssdCapabilityPeer) {
			defer close(p.done)
			if e := p.backend.Start(); e != nil {
				reports <- report{i, "error", e}
				return
			}
			remote := regs[1-i]
			seen := map[recordKind]bool{}
			queried := false
			announced := false
			for {
				event, e := p.backend.Next()
				if e != nil {
					if ctx.Err() == nil && !errors.Is(e, context.Canceled) {
						reports <- report{i, "error", e}
					}
					return
				}
				if event.InterfaceIndex != ^uint32(0) {
					reports <- report{i, "error", ErrInvalidPacket}
					return
				}
				record, e := dnssdDecodeRecord(event)
				if e != nil {
					reports <- report{i, "error", e}
					return
				}
				expected := false
				for _, raw := range remote.Records {
					want, _ := dnssdDecodeRecord(dnssdEventFor(raw))
					copy := record
					copy.TTL = want.TTL
					copy.Removed = false
					if reflect.DeepEqual(copy, want) {
						expected = true
						break
					}
				}
				if !expected {
					continue
				}
				if record.Removed {
					if record.Kind == ptrRecord {
						reports <- report{i, "withdrawal", nil}
						return
					}
					continue
				}
				seen[record.Kind] = true
				if record.Kind == ptrRecord && !queried {
					queried = true
					for _, raw := range remote.Records[1:] {
						if e = p.backend.Query(raw.Name, raw.Type); e != nil {
							reports <- report{i, "error", e}
							return
						}
					}
				}
				if len(seen) == 4 && !announced {
					announced = true
					reports <- report{i, "records", nil}
				}
			}
		}(i, p)
	}
	var seen [2]bool
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-reports:
			if r.err != nil {
				dnssdCapabilityError(&result, r.err)
				return
			}
			if r.kind == "records" {
				seen[r.index] = true
				if seen[0] && seen[1] {
					result.ReciprocalRecords = true
					peers[0].close()
				}
			}
			if r.kind == "withdrawal" && r.index == 1 && result.ReciprocalRecords {
				result.WithdrawalObserved = true
				result.Status, result.Reason = "verified", "native_local_api_observed"
				return
			}
		}
	}
}
func runDNSServiceSelectedLoopbackCapability() (result dnssdCapabilityResult) {
	result = dnssdCapability("selected_loopback_os")
	scope, err := isolatedLoopbackScope()
	if err != nil {
		result.Reason = "loopback_scope_unavailable"
		return
	}
	return runDNSServiceScopeCapability(scope, isolatedLoopbackPolicy(), "selected_loopback_os")
}
func runDNSServiceScopeCapability(scope Scope, policy discoveryPolicy, label string) (result dnssdCapabilityResult) {
	result = dnssdCapability(label)
	cfgs, err := dnssdSyntheticConfigs(scope)
	if err != nil {
		result.Status, result.Reason = "failed", "fixture_unavailable"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	var sessions []*Session
	defer func() {
		cancel()
		closed := make(chan error, len(sessions))
		for _, s := range sessions {
			go func(s *Session) { closed <- s.Close() }(s)
		}
		timer := time.NewTimer(closeJoinTimeout)
		defer timer.Stop()
		for range sessions {
			select {
			case e := <-closed:
				if e != nil {
					result.Status, result.Reason = "failed", "cleanup_uncertain"
					return
				}
			case <-timer.C:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
		}
		for _, s := range sessions {
			select {
			case <-s.Done():
			default:
				result.Status, result.Reason = "failed", "owners_not_joined"
				return
			}
			if len(s.Candidates()) != 0 {
				result.Status, result.Reason = "failed", "candidate_survived_close"
				return
			}
		}
		result.OwnersJoined = len(sessions) == 2
		if result.Status == "verified" && !result.OwnersJoined {
			result.Status, result.Reason = "failed", "owners_missing"
		}
	}()
	for _, cfg := range cfgs {
		s, e := startProvider(ctx, cfg, newDarwinRecordProvider, policy)
		if s != nil {
			sessions = append(sessions, s)
		}
		if e != nil {
			dnssdCapabilityError(&result, e)
			return
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		both := true
		for i, s := range sessions {
			found := false
			for _, c := range s.Candidates() {
				if c.Advertisement == cfgs[1-i].Advertisement && c.Scope == scope && !c.Source.IsValid() && c.Provenance == OSDNSServiceProvenance && c.ExpiresAt.Equal(s.deadline) && time.Now().Before(c.ExpiresAt) {
					looked, lookupErr := s.Lookup(c.ID)
					found = lookupErr == nil && looked == c
				}
			}
			both = both && found
			if e := s.Err(); e != nil {
				dnssdCapabilityError(&result, e)
				return
			}
		}
		if both {
			result.ReciprocalCandidates = true
			result.Status, result.Reason = "verified", "native_scoped_api_observed"
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
