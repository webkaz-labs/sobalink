package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

func init() {
	for _, key := range []string{"portProposalAttempts", "portProposalResults", "portProposalBinds", "portProposalSeconds"} {
		supportedCapacityResources[key] = true
	}
}

// PortProposal uses the existing connect mapping: consecutive local ports map
// to ascending effective remote ports, after applying all saved exclusions.
type PortProposal struct {
	LocalPort int `json:"localPort"`
	LocalEnd  int `json:"localEnd"`
}

type ServicePortProposals struct {
	Configuration  ServiceSpec       `json:"configuration"`
	Revision       string            `json:"revision"`
	Code           string            `json:"code"`
	ConflictPort   int               `json:"conflictPort"`
	EffectivePorts string            `json:"effectivePorts"`
	CheckedAt      time.Time         `json:"checkedAt"`
	FromPort       int               `json:"fromPort"`
	Attempts       int               `json:"attempts"`
	RequestedCount int               `json:"requestedCount"`
	AttemptBudget  int               `json:"attemptBudget"`
	BindChecks     int64             `json:"bindChecks"`
	StopReason     string            `json:"stopReason"`
	Proposals      []PortProposal    `json:"proposals"`
	Reservation    bool              `json:"reservation"`
	NextSteps      map[string]string `json:"nextSteps"`
}

// This explicit local command opens only transient loopback bind checks. It
// never creates a forwarder, changes a saved definition or starts a permission.
// c.op serializes it with service admission and configuration mutations.
func (c *Core) servicePortProposals(ctx context.Context, raw json.RawMessage) (value any, resultErr error) {
	seconds := min(c.limit("resources", "portProposalSeconds"), capacity.MaxDurationSeconds)
	check, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	stop := context.AfterFunc(c.ctx, cancel)
	defer cancel()
	defer stop()
	defer func() {
		if check.Err() != nil {
			value, resultErr = nil, check.Err()
		}
		if errors.Is(resultErr, context.DeadlineExceeded) {
			resultErr = &portProposalContextError{"listener_probe_timeout", "the port check deadline expired; no configuration changed; review the finite check budget and retry explicitly", resultErr}
		}
		if errors.Is(resultErr, context.Canceled) {
			resultErr = &portProposalContextError{"listener_probe_canceled", "the port check was canceled; no configuration changed and all transient sockets were closed", resultErr}
		}
	}()
	ctx = check
	var in struct {
		ID               string `json:"id"`
		ExpectedRevision string `json:"expectedRevision"`
		FromPort         int    `json:"fromPort"`
		Count            int    `json:"count"`
		Attempts         int    `json:"attempts"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if in.FromPort == 0 {
		in.FromPort = 49152
	}
	if in.FromPort < 1024 || in.FromPort > 65535 {
		return nil, &localCommandError{"listener_mapping_invalid", "choose a proposal starting port within 1024..65535"}
	}
	// The physical port domain also bounds the search, even when the user raises
	// a finite policy budget. These are work/result budgets, not permissions.
	countLimit := int(min(c.limit("resources", "portProposalResults"), 64512))
	attemptLimit := int(min(c.limit("resources", "portProposalAttempts"), 64512))
	if in.Count < 0 || in.Attempts < 0 {
		return nil, &localCommandError{"listener_probe_invalid", "proposal count and attempt count must be nonnegative"}
	}
	if in.Count == 0 {
		in.Count = countLimit
	}
	if in.Attempts == 0 {
		in.Attempts = attemptLimit
	}
	if in.Count < 1 || in.Count > countLimit || in.Attempts < 1 || in.Attempts > attemptLimit {
		return nil, &localCommandError{"listener_probe_capacity", "requested checks exceed the finite portProposalResults or portProposalAttempts budget; review capacity settings"}
	}
	var spec ServiceSpec
	c.mu.RLock()
	backend := c.profile.Settings.Network
	for _, saved := range c.profile.Services {
		if ctx.Err() != nil {
			c.mu.RUnlock()
			return nil, ctx.Err()
		}
		if saved.ID == in.ID {
			spec = saved
			spec.PeerIDs = append([]string(nil), saved.PeerIDs...)
			break
		}
	}
	c.mu.RUnlock()
	if spec.ID == "" {
		return nil, &localCommandError{"service_not_found", "saved service no longer exists; refresh the local service list"}
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != serviceRevision(spec) {
		return nil, &localCommandError{"service_revision_conflict", "saved service changed; reload and review its complete configuration"}
	}
	c.mu.RLock()
	active := c.active[spec.ID] != nil
	c.mu.RUnlock()
	if active {
		return nil, &localCommandError{"service_active", "stop this service explicitly before checking alternate ports"}
	}
	if spec.Direction != "forward" {
		return nil, &localCommandError{"listener_proposal_unsupported", "alternate local entry ports apply only to stopped outbound connections; application targets and shares stay fixed"}
	}
	if spec.Backend == "" || spec.Backend != backend {
		return nil, &localCommandError{"service_backend_mismatch", "review this saved connection in its selected network before checking ports"}
	}
	if spec.Network != "tcp" && spec.Network != "udp" {
		return nil, &localCommandError{"listener_mapping_invalid", "review the saved TCP or UDP protocol"}
	}
	host, err := serviceLoopback(spec.LoopbackHost)
	if err != nil {
		return nil, &localCommandError{"listener_mapping_invalid", "review the saved exact IPv4 or IPv6 loopback address"}
	}
	effective, err := ranges.ParseWithLimit(spec.Ports, 65535)
	if err != nil {
		return nil, &localCommandError{"listener_mapping_invalid", "review the saved ports and exclusions"}
	}
	if spec.ExcludePorts != "" {
		exclude, err := ranges.ParseWithLimit(spec.ExcludePorts, 65535)
		if err != nil {
			return nil, &localCommandError{"listener_mapping_invalid", "review the saved ports and exclusions"}
		}
		effective, err = excludeProposalPorts(ctx, effective, exclude)
		if err != nil {
			return nil, &localCommandError{"listener_mapping_invalid", "review the saved ports and exclusions"}
		}
	}
	if effective.Empty() {
		return nil, &localCommandError{"listener_mapping_invalid", "no saved ports remain after exclusions"}
	}
	remote, err := effective.ExpandWithLimit(c.limit("resources", "materializedListeners") - int64(c.materializedCount()))
	if err != nil {
		return nil, listenerError("listener_capacity")
	}
	original := make([]int, len(remote))
	for i, port := range remote {
		original[i] = int(port)
		if spec.LocalPort != 0 {
			original[i] = spec.LocalPort + i
		}
		if original[i] < 1024 || original[i] > 65535 {
			return nil, &localCommandError{"listener_mapping_invalid", "local listener mapping must remain within 1024..65535"}
		}
	}
	st, err := c.current(ctx)
	if err != nil || !st.Snapshot.Running {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &localCommandError{"network_unavailable", "reconnect the selected network before checking its reserved ports"}
	}
	reserved := map[int]bool{54543: true, 54544: true, 54545: true}
	for _, port := range st.ReservedPorts {
		reserved[int(port)] = true
	}
	c.mu.RLock()
	for _, endpoint := range c.proxyReservedPorts() {
		reserved[int(endpoint.Port())] = true
	}
	if c.web != nil {
		reserved[int(c.web.Port())] = true
	}
	c.mu.RUnlock()
	reservedPorts := make([]int, 0, len(reserved))
	for port := range reserved {
		reservedPorts = append(reservedPorts, port)
	}
	slices.Sort(reservedPorts)
	bindBudget := c.limit("resources", "portProposalBinds")
	remainingBinds := bindBudget
	conflict, err := c.checkLocalPorts(check, spec.Network, host, len(original), func(i int) int { return original[i] }, &remainingBinds)
	if err == nil {
		return nil, &localCommandError{"listener_no_conflict", "the saved local ports are available now; no alternate was selected or started"}
	}
	if networkErrorCode(err) != "listener_conflict" {
		return nil, err
	}
	result := ServicePortProposals{Configuration: spec, Revision: serviceRevision(spec), Code: "listener_conflict", ConflictPort: conflict, EffectivePorts: effective.String(), FromPort: in.FromPort, RequestedCount: in.Count, AttemptBudget: in.Attempts, Proposals: []PortProposal{}, NextSteps: diagnosticNextSteps("listener_conflict")}
	for start := in.FromPort; start+len(remote)-1 <= 65535 && result.Attempts < in.Attempts && len(result.Proposals) < in.Count; start++ {
		if err := check.Err(); err != nil {
			return nil, err
		}
		if remainingBinds < int64(len(remote)) {
			result.StopReason = "bind_budget"
			break
		}
		result.Attempts++
		// Inspect the reserved interval before materializing a window. Work for
		// skipped candidates is logarithmic in reserved ports, not mapped width.
		end := start + len(remote) - 1
		if portProposalWindowReserved(reservedPorts, start, end) {
			continue
		}
		// Generate each consecutive port only when its bind is charged. An early
		// conflict must not pay mapped-width work for the rest of the window.
		if _, err := c.checkLocalPorts(check, spec.Network, host, len(remote), func(i int) int { return start + i }, &remainingBinds); err != nil {
			if networkErrorCode(err) == "listener_probe_capacity" {
				result.StopReason = "bind_budget"
				break
			}
			if networkErrorCode(err) == "listener_conflict" {
				continue
			}
			return nil, err
		}
		result.Proposals = append(result.Proposals, PortProposal{LocalPort: start, LocalEnd: end})
		start += len(remote) - 1
	}
	if err := check.Err(); err != nil {
		return nil, err
	}
	result.CheckedAt = time.Now().UTC()
	result.BindChecks = bindBudget - remainingBinds
	if result.StopReason == "" {
		switch {
		case len(result.Proposals) == in.Count:
			result.StopReason = "requested_count"
		case result.Attempts == in.Attempts:
			result.StopReason = "attempt_budget"
		default:
			result.StopReason = "port_range"
		}
	}
	if len(result.Proposals) == 0 {
		result.Code = "listener_proposals_exhausted"
		result.NextSteps = map[string]string{"en": "No checked alternative was found within the bounded search. Choose another high starting port and check again, or stop the conflicting listener explicitly.", "ja": "回数を制限した確認では候補が見つかりませんでした。別の高位の開始ポートを指定して再確認するか、競合している入口を明示的に停止してください。"}
	}
	return result, nil
}

// Hold the whole window until checked, then close every socket before returning.
// A partial window cannot be advertised as checked. No socket accepts or forwards.
func (c *Core) checkLocalPorts(ctx context.Context, protocol, host string, count int, portAt func(int) int, remaining *int64) (int, error) {
	var probes []io.Closer
	defer func() {
		for _, probe := range probes {
			_ = probe.Close()
		}
	}()
	network := protocol + "4"
	if host == "::1" {
		network = protocol + "6"
	}
	listen := c.portProposalListen
	if listen == nil {
		listen = func(ctx context.Context, network, address string) (io.Closer, error) {
			config := &net.ListenConfig{}
			if protocol == "tcp" {
				return config.Listen(ctx, network, address)
			}
			return config.ListenPacket(ctx, network, address)
		}
	}
	for i := 0; i < count; i++ {
		port := portAt(i)
		if err := ctx.Err(); err != nil {
			return port, err
		}
		if *remaining <= 0 {
			return port, &localCommandError{"listener_probe_capacity", "the finite portProposalBinds budget is exhausted; review checking capacity"}
		}
		*remaining = *remaining - 1
		probe, err := listen(ctx, network, config.Address(host, port))
		if probe != nil {
			probes = append(probes, probe)
		}
		if ctx.Err() != nil {
			return port, ctx.Err()
		}
		if err != nil {
			return port, classifyListenerError(err)
		}
		if probe == nil {
			return port, listenerError("listener_unavailable")
		}
	}
	return 0, nil
}

func listenerError(code string) error {
	messages := map[string]string{
		"listener_conflict":            "the local port is already in use; stop the conflicting listener or explicitly check alternate ports with service ports",
		"listener_capacity":            "listener capacity is exhausted; narrow the selection, stop unused work or review the finite listener budget",
		"listener_permission_denied":   "the operating system denied this loopback bind; review local permissions without changing security settings automatically",
		"listener_address_unavailable": "the selected loopback address family is unavailable; review the exact IPv4 or IPv6 choice",
		"listener_unavailable":         "the loopback bind failed for an unclassified reason; review the local listener configuration before retrying",
	}
	return &localCommandError{code, messages[code]}
}

func classifyListenerError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return listenerError(listenerSystemErrorCode(err))
}

func serviceFailureCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return "service_start_failed"
}

// Keep the proposed endpoint formatting exactly aligned with numeric loopback binds.
func (p PortProposal) String() string {
	if p.LocalPort == p.LocalEnd {
		return strconv.Itoa(p.LocalPort)
	}
	return strconv.Itoa(p.LocalPort) + "-" + strconv.Itoa(p.LocalEnd)
}

func portProposalWindowReserved(ports []int, start, end int) bool {
	position, _ := slices.BinarySearch(ports, start)
	return position < len(ports) && ports[position] <= end
}

// Exclusion uses a monotone cursor over normalized intervals. Unlike repeatedly
// rescanning all exclusions, preflight is linear and checks cancellation even
// for a saved scope with many disjoint intervals. The physical port domain
// bounds parsing and the final normalization to at most 65535 intervals.
func excludeProposalPorts(ctx context.Context, ports, excluded ranges.Set) (ranges.Set, error) {
	exclusions := excluded.Intervals()
	var out []ranges.Interval
	cursor := 0
	for _, interval := range ports.Intervals() {
		if err := ctx.Err(); err != nil {
			return ranges.Set{}, err
		}
		next, last := uint32(interval.First), uint32(interval.Last)
		for cursor < len(exclusions) && uint32(exclusions[cursor].Last) < next {
			cursor++
		}
		for cursor < len(exclusions) && uint32(exclusions[cursor].First) <= last {
			if err := ctx.Err(); err != nil {
				return ranges.Set{}, err
			}
			exclusion := exclusions[cursor]
			if uint32(exclusion.First) > next {
				out = append(out, ranges.Interval{First: uint16(next), Last: exclusion.First - 1})
			}
			next = uint32(exclusion.Last) + 1
			if next > last {
				break
			}
			cursor++
		}
		if next <= last {
			out = append(out, ranges.Interval{First: uint16(next), Last: uint16(last)})
		}
	}
	if err := ctx.Err(); err != nil {
		return ranges.Set{}, err
	}
	return ranges.NewSetWithLimit(out, 65535)
}

type portProposalContextError struct {
	code, message string
	cause         error
}

func (e *portProposalContextError) Error() string     { return e.message }
func (e *portProposalContextError) ErrorCode() string { return e.code }
func (e *portProposalContextError) Unwrap() error     { return e.cause }
