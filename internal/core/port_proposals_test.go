package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/ranges"
)

type proposalProbe struct{ close func() }

func (p proposalProbe) Close() error { p.close(); return nil }

func portProposalFixture(t *testing.T) (*Core, ServiceSpec, map[string]any) {
	t.Helper()
	p := newCorePair(t)
	spec := ServiceSpec{ID: "saved-port-fixture", Backend: "tailnet", Name: "port-fixture", Direction: "forward", Network: "tcp", Ports: "8000-8002", ExcludePorts: "8001", LocalPort: 20000, LoopbackHost: "127.0.0.1", Lifetime: "until-stopped", PeerID: "peer-b", Purpose: "custom"}
	profile := p.a.profileCopy()
	profile.Services = []ServiceSpec{spec}
	if err := p.a.saveProfile(profile); err != nil {
		t.Fatal(err)
	}
	p.a.mu.Lock()
	p.a.profile = profile
	p.a.mu.Unlock()
	return p.a, spec, map[string]any{"id": spec.ID, "expectedRevision": serviceRevision(spec), "fromPort": 49152}
}

func TestPortProposalsPreserveMappingAndCloseEveryProbe(t *testing.T) {
	for _, family := range []struct{ protocol, host, network string }{{"tcp", "127.0.0.1", "tcp4"}, {"udp", "::1", "udp6"}} {
		t.Run(family.network, func(t *testing.T) {
			c, spec, input := portProposalFixture(t)
			spec.Network, spec.LoopbackHost = family.protocol, family.host
			c.mu.Lock()
			c.profile.Services[0] = spec
			c.mu.Unlock()
			input["expectedRevision"], input["fromPort"] = serviceRevision(spec), 54542
			before, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
			live, maximum, calls := 0, 0, 0
			var checked []int
			c.portProposalListen = func(_ context.Context, network, address string) (io.Closer, error) {
				calls++
				host, portText, err := net.SplitHostPort(address)
				if err != nil {
					t.Fatal(err)
				}
				port, _ := strconv.Atoi(portText)
				if network != family.network || host != family.host {
					t.Fatal("protocol or loopback family changed", network, address)
				}
				if port == 20001 {
					return nil, fixtureListenerConflict()
				}
				if port >= 54543 && port <= 54545 {
					t.Fatal("reserved port checked", port)
				}
				checked = append(checked, port)
				live++
				maximum = max(maximum, live)
				return proposalProbe{func() { live-- }}, nil
			}
			result := mustCommand(t, c, "service.ports", input).(ServicePortProposals)
			if result.EffectivePorts != "8000,8002" || result.ConflictPort != 20001 || result.Revision != serviceRevision(spec) || !reflect.DeepEqual(spec, result.Configuration) || result.Reservation || result.CheckedAt.IsZero() {
				t.Fatal("review lost original scope", result)
			}
			want := []PortProposal{{54546, 54547}, {54548, 54549}, {54550, 54551}}
			if !reflect.DeepEqual(result.Proposals, want) || result.StopReason != "requested_count" || result.BindChecks != int64(calls) || maximum != 2 || live != 0 {
				t.Fatal("partial, unbounded or leaked probes", result, maximum, live, checked)
			}
			after, _ := os.ReadFile(filepath.Join(c.dir, "sobalink.json"))
			if string(before) != string(after) || len(c.active) != 0 || len(c.serviceStates) != 0 || c.profileCopy().Services[0].LocalPort != 20000 {
				t.Fatal("proposal mutated saved settings or runtime")
			}
		})
	}
}

func TestPortProposalsDoNotInventConflict(t *testing.T) {
	for _, tc := range append(fixtureListenerErrors(), struct {
		err  error
		code string
	}{nil, "listener_no_conflict"}, struct {
		err  error
		code string
	}{errors.New("private unknown fixture failure"), "listener_unavailable"}) {
		t.Run(tc.code, func(t *testing.T) {
			c, _, input := portProposalFixture(t)
			calls, live := 0, 0
			c.portProposalListen = func(context.Context, string, string) (io.Closer, error) {
				calls++
				if tc.err != nil {
					return nil, &net.OpError{Op: "listen", Net: "tcp4", Err: tc.err}
				}
				live++
				return proposalProbe{func() { live-- }}, nil
			}
			_, err := command(c, randomID(), "service.ports", input)
			if networkErrorCode(err) != tc.code || calls > 2 || live != 0 {
				t.Fatal("failure misclassified or alternate searched", err, calls, live)
			}
		})
	}
}

func TestPortProposalsValidateBeforeBinding(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*Core, *ServiceSpec, map[string]any)
	}{
		{"stale", "service_revision_conflict", func(c *Core, s *ServiceSpec, in map[string]any) { in["expectedRevision"] = "old" }},
		{"share", "listener_proposal_unsupported", func(c *Core, s *ServiceSpec, in map[string]any) { s.Direction = "share" }},
		{"backend", "service_backend_mismatch", func(c *Core, s *ServiceSpec, in map[string]any) { s.Backend = "lan" }},
		{"family", "listener_mapping_invalid", func(c *Core, s *ServiceSpec, in map[string]any) { s.LoopbackHost = "0.0.0.0" }},
		{"overflow", "listener_mapping_invalid", func(c *Core, s *ServiceSpec, in map[string]any) { s.LocalPort = 65535 }},
		{"capacity", "listener_capacity", func(c *Core, s *ServiceSpec, in map[string]any) {
			c.capacity.Resources["materializedListeners"] = capacity.Limited(1)
		}},
		{"active", "service_active", func(c *Core, s *ServiceSpec, in map[string]any) { c.active[s.ID] = &activeService{spec: *s} }},
		{"search-capacity", "listener_probe_capacity", func(c *Core, s *ServiceSpec, in map[string]any) { in["count"] = 4 }},
		{"bad-start", "listener_mapping_invalid", func(c *Core, s *ServiceSpec, in map[string]any) { in["fromPort"] = 80 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, spec, input := portProposalFixture(t)
			c.mu.Lock()
			tc.change(c, &spec, input)
			c.profile.Services[0] = spec
			c.mu.Unlock()
			if tc.name != "stale" {
				input["expectedRevision"] = serviceRevision(spec)
			}
			c.portProposalListen = func(context.Context, string, string) (io.Closer, error) {
				t.Fatal("invalid proposal bound a socket")
				return nil, nil
			}
			_, err := command(c, randomID(), "service.ports", input)
			if networkErrorCode(err) != tc.code {
				t.Fatal("unexpected error", err, networkErrorCode(err))
			}
			if tc.name == "active" {
				c.mu.Lock()
				delete(c.active, spec.ID)
				c.mu.Unlock()
			}
		})
	}
}

func TestPortProposalsBoundSearchAndRespectRaisedBudgets(t *testing.T) {
	for _, scenario := range []string{"attempts", "binds", "raised", "upper-edge"} {
		t.Run(scenario, func(t *testing.T) {
			c, _, input := portProposalFixture(t)
			c.mu.Lock()
			switch scenario {
			case "attempts":
				input["attempts"] = 2
			case "binds":
				c.capacity.Resources["portProposalBinds"] = capacity.Limited(4)
			case "raised":
				c.capacity.Resources["portProposalResults"] = capacity.Limited(5)
				c.capacity.Resources["portProposalAttempts"] = capacity.Limited(40)
				input["count"], input["attempts"] = 5, 40
			case "upper-edge":
				input["fromPort"] = 65535
			}
			c.mu.Unlock()
			live, calls := 0, 0
			c.portProposalListen = func(_ context.Context, _ string, address string) (io.Closer, error) {
				calls++
				_, text, _ := net.SplitHostPort(address)
				port, _ := strconv.Atoi(text)
				if port == 20000 || scenario == "attempts" {
					return nil, fixtureListenerConflict()
				}
				live++
				return proposalProbe{func() { live-- }}, nil
			}
			got := mustCommand(t, c, "service.ports", input).(ServicePortProposals)
			switch scenario {
			case "attempts":
				if got.Attempts != 2 || got.StopReason != "attempt_budget" || len(got.Proposals) != 0 || got.Code != "listener_proposals_exhausted" {
					t.Fatal(got)
				}
			case "binds":
				if calls != 3 || len(got.Proposals) != 1 || got.StopReason != "bind_budget" {
					t.Fatal(got, calls)
				}
			case "raised":
				if len(got.Proposals) != 5 || got.AttemptBudget != 40 {
					t.Fatal("defaults became immutable", got)
				}
			case "upper-edge":
				if got.Attempts != 0 || len(got.Proposals) != 0 || got.StopReason != "port_range" {
					t.Fatal(got)
				}
			}
			if live != 0 {
				t.Fatal("probe leaked")
			}
		})
	}
}

func TestPortProposalsCancellationClosesPartialWindow(t *testing.T) {
	c, _, input := portProposalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls, live := 0, 0
	c.portProposalListen = func(context.Context, string, string) (io.Closer, error) {
		calls++
		if calls == 1 {
			return nil, fixtureListenerConflict()
		}
		live++
		if calls == 3 {
			cancel()
		}
		return proposalProbe{func() { live-- }}, nil
	}
	raw, _ := json.Marshal(input)
	_, err := c.servicePortProposals(ctx, raw)
	if !errors.Is(err, context.Canceled) || live != 0 || calls != 3 {
		t.Fatal("cancellation leaked or returned a proposal", err, live, calls)
	}
}

func TestPortProposalsNativeTCPBindConflictAndRecheck(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, spec, input := portProposalFixture(t)
	spec.LocalPort = listener.Addr().(*net.TCPAddr).Port
	spec.Ports = "8000"
	spec.ExcludePorts = ""
	c.mu.Lock()
	c.profile.Services[0] = spec
	c.mu.Unlock()
	input["expectedRevision"] = serviceRevision(spec)
	got := mustCommand(t, c, "service.ports", input).(ServicePortProposals)
	if len(got.Proposals) == 0 {
		t.Fatal("no native proposal", got)
	}
	candidate := got.Proposals[0].LocalPort
	occupied, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate)))
	if err != nil {
		t.Fatal("proposal did not close its socket", err)
	}
	defer occupied.Close()
	var args map[string]any
	if err := json.Unmarshal(savedStartPayload(spec, "", 0), &args); err != nil {
		t.Fatal(err)
	}
	args["localPort"] = candidate
	args["replaceId"] = spec.ID
	args["expectedRevision"] = got.Revision
	_, err = command(c, randomID(), "service.connect", args)
	if networkErrorCode(err) != "listener_conflict" || len(c.active) != 0 || c.serviceFailures[spec.ID].Code != "listener_conflict" {
		t.Fatal("actual bind did not recheck conflict", err)
	}
	if c.profileCopy().Services[0].LocalPort != candidate {
		t.Fatal("explicit restart unexpectedly chose another port")
	}
}

func TestPortProposalsSamePortExclusionsAndBackendReservations(t *testing.T) {
	c, spec, input := portProposalFixture(t)
	spec.LocalPort = 0
	c.mu.Lock()
	c.profile.Services[0] = spec
	c.mu.Unlock()
	input["expectedRevision"] = serviceRevision(spec)
	node := c.nodeCopy().(*pipeNode)
	node.mu.Lock()
	node.state.ReservedPorts = []uint16{49152, 49155}
	node.mu.Unlock()
	var checked []int
	c.portProposalListen = func(_ context.Context, _ string, address string) (io.Closer, error) {
		_, raw, _ := net.SplitHostPort(address)
		port, _ := strconv.Atoi(raw)
		checked = append(checked, port)
		if port == 8002 {
			return nil, fixtureListenerConflict()
		}
		if port == 8001 || port == 49152 || port == 49155 {
			t.Fatal("excluded or reserved port probed", port)
		}
		return proposalProbe{func() {}}, nil
	}
	result := mustCommand(t, c, "service.ports", input).(ServicePortProposals)
	if !reflect.DeepEqual(checked[:2], []int{8000, 8002}) || result.Configuration.LocalPort != 0 || result.Proposals[0] != (PortProposal{49153, 49154}) || result.Proposals[1] != (PortProposal{49156, 49157}) {
		t.Fatal("same-port intent, exclusions or reserved mapping changed", checked, result)
	}
}

func TestPortProposalsStaleApplyLeavesChangedDefinitionUntouched(t *testing.T) {
	c, spec, input := portProposalFixture(t)
	c.portProposalListen = func(_ context.Context, _ string, address string) (io.Closer, error) {
		_, raw, _ := net.SplitHostPort(address)
		if raw == "20000" {
			return nil, fixtureListenerConflict()
		}
		return proposalProbe{func() {}}, nil
	}
	proposal := mustCommand(t, c, "service.ports", input).(ServicePortProposals)
	changed := spec
	changed.Lifetime, changed.TTLSeconds = "finite", 7200
	c.mu.Lock()
	c.profile.Services[0] = changed
	c.mu.Unlock()
	var args map[string]any
	if err := json.Unmarshal(savedStartPayload(spec, "", 0), &args); err != nil {
		t.Fatal(err)
	}
	args["localPort"] = proposal.Proposals[0].LocalPort
	_, err := command(c, randomID(), "service.connect", args)
	if networkErrorCode(err) != "service_revision_conflict" || !reflect.DeepEqual(c.profileCopy().Services[0], changed) || len(c.active) != 0 {
		t.Fatal("stale proposal applied or changed newer lifetime", err)
	}
}

func TestPortProposalsTimeBudgetIsIndependentOfPermission(t *testing.T) {
	c, spec, input := portProposalFixture(t)
	c.mu.Lock()
	c.capacity.Resources["portProposalSeconds"] = capacity.Limited(1)
	c.mu.Unlock()
	calls, live := 0, 0
	c.portProposalListen = func(ctx context.Context, _ string, _ string) (io.Closer, error) {
		calls++
		if calls == 1 {
			return nil, fixtureListenerConflict()
		}
		if calls == 2 {
			live++
			return proposalProbe{func() { live-- }}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := command(c, randomID(), "service.ports", input)
	if !errors.Is(err, context.DeadlineExceeded) || networkErrorCode(err) != "listener_probe_timeout" || live != 0 || !reflect.DeepEqual(c.profileCopy().Services[0], spec) || len(c.active) != 0 {
		t.Fatal("check deadline leaked a socket or changed permission", err, live)
	}
}

func TestPortProposalsCancellationBeforeReservedWindows(t *testing.T) {
	c, spec, input := portProposalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.mu.Lock()
	c.capacity.Resources["portProposalAttempts"] = capacity.Limited(64512)
	c.mu.Unlock()
	node := c.nodeCopy().(*pipeNode)
	node.mu.Lock()
	for port := 49152; port <= 65535; port++ {
		node.state.ReservedPorts = append(node.state.ReservedPorts, uint16(port))
	}
	node.mu.Unlock()
	calls := 0
	c.portProposalListen = func(context.Context, string, string) (io.Closer, error) {
		calls++
		if calls == 1 {
			return proposalProbe{cancel}, nil
		}
		return nil, fixtureListenerConflict()
	}
	raw, _ := json.Marshal(input)
	_, err := c.servicePortProposals(ctx, raw)
	if !errors.Is(err, context.Canceled) || calls != 2 || c.profileCopy().Services[0].LocalPort != spec.LocalPort {
		t.Fatal("reserved-window path ignored cancellation", err, calls)
	}
}

func TestPortProposalsWideReservedWindowsDoNotAllocatePerAttempt(t *testing.T) {
	c, spec, input := portProposalFixture(t)
	spec.Ports, spec.ExcludePorts, spec.LocalPort = "1024-32767", "", 1024
	c.mu.Lock()
	c.profile.Services[0] = spec
	c.capacity.Resources["materializedListeners"] = capacity.Limited(65535)
	c.capacity.Resources["portProposalBinds"] = capacity.Limited(65535)
	c.capacity.Resources["portProposalAttempts"] = capacity.Limited(1000)
	c.mu.Unlock()
	input["expectedRevision"], input["fromPort"] = serviceRevision(spec), 32768
	node := c.nodeCopy().(*pipeNode)
	node.mu.Lock()
	node.state.ReservedPorts = []uint16{65000}
	node.mu.Unlock()
	c.portProposalListen = func(context.Context, string, string) (io.Closer, error) { return nil, fixtureListenerConflict() }
	allocations := func(attempts int) float64 {
		input["attempts"] = attempts
		raw, _ := json.Marshal(input)
		return testing.AllocsPerRun(2, func() {
			value, err := c.servicePortProposals(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			result := value.(ServicePortProposals)
			if result.Attempts != attempts || result.BindChecks != 1 || len(result.Proposals) != 0 || result.StopReason != "attempt_budget" {
				t.Fatal("reserved search escaped work bounds", result)
			}
		})
	}
	one, many := allocations(1), allocations(1000)
	if many > one+16 {
		t.Fatalf("skipped windows allocate by attempt: one=%v many=%v", one, many)
	}
}

func TestPortProposalsLinearExclusionMatchesMapping(t *testing.T) {
	for _, input := range []struct{ ports, exclude string }{{"1-65535", "1,1024-4000,6000,65535"}, {"1,3,5,7", "1-4,6-10"}, {"1000-1010,1020-1030,1040-1050", "1005-1045"}, {"8000-8002", "8001"}} {
		ports, _ := ranges.ParseWithLimit(input.ports, 65535)
		excluded, _ := ranges.ParseWithLimit(input.exclude, 65535)
		want, err := ports.Excluding(excluded)
		if err != nil {
			t.Fatal(err)
		}
		got, err := excludeProposalPorts(context.Background(), ports, excluded)
		if err != nil || got.String() != want.String() {
			t.Fatal("exclusion semantics changed", input, got.String(), want.String(), err)
		}
	}
	var left, right []ranges.Interval
	for port := 1; port < 65535; port += 2 {
		left = append(left, ranges.Interval{First: uint16(port), Last: uint16(port)})
		right = append(right, ranges.Interval{First: uint16(port + 1), Last: uint16(port + 1)})
	}
	ports, _ := ranges.NewSetWithLimit(left, 65535)
	excluded, _ := ranges.NewSetWithLimit(right, 65535)
	got, err := excludeProposalPorts(context.Background(), ports, excluded)
	if err != nil || got.Count() != ports.Count() || got.IntervalCount() != ports.IntervalCount() {
		t.Fatal("large disjoint mapping changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := excludeProposalPorts(ctx, ports, excluded); !errors.Is(err, context.Canceled) {
		t.Fatal("exclusion ignored cancellation", err)
	}
}

func TestPortProposalsAreFreshAndNeverRetainedInRequestCache(t *testing.T) {
	c, _, input := portProposalFixture(t)
	busy, calls := true, 0
	c.portProposalListen = func(_ context.Context, _ string, address string) (io.Closer, error) {
		calls++
		_, port, _ := net.SplitHostPort(address)
		if busy && port == "20000" {
			return nil, fixtureListenerConflict()
		}
		return proposalProbe{func() {}}, nil
	}
	requestID := randomID()
	before := len(c.requests)
	if _, err := command(c, requestID, "service.ports", input); err != nil {
		t.Fatal(err)
	}
	firstCalls := calls
	busy = false
	if _, err := command(c, requestID, "service.ports", input); networkErrorCode(err) != "listener_no_conflict" {
		t.Fatal("replayed stale port observation", err)
	}
	if calls <= firstCalls || len(c.requests) != before || c.requests[requestID].value != nil {
		t.Fatal("proposal results retained in request cache", calls, firstCalls, len(c.requests), before)
	}
}
