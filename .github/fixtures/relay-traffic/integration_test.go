//go:build lanlink_integration

package lanlink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/feature/buildfeatures"
)

const trafficSourceCommit = "8e6cbb00d60757f701d7d453adb92590cc5d2544"
const trafficBuildTags = "lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport"
const trafficReportPrefix = "SOBALINK_RELAY_TRAFFIC_REPORT "

type relayTrafficReport struct {
	Schema              int            `json:"schema"`
	Evidence            string         `json:"evidence"`
	SourceCommit        string         `json:"source_commit"`
	FixtureSHA256       string         `json:"fixture_sha256"`
	BuildTags           string         `json:"build_tags"`
	Target              string         `json:"target"`
	GoVersion           string         `json:"go_version"`
	Outcome             string         `json:"outcome"`
	Phases              []trafficPhase `json:"phases"`
	CleanupComplete     bool           `json:"cleanup_complete"`
	ResidualConnections int64          `json:"residual_connections"`
	PacketBytes         *uint64        `json:"packet_bytes"`
	Scope               string         `json:"scope"`
	Workload            string         `json:"workload"`
	Excludes            []string       `json:"excludes"`
}

type trafficProxy struct {
	listener    net.Listener
	target      netip.AddrPort
	counters    trafficCounters
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
	once        sync.Once
	closed      bool
}

func startTrafficProxy(ctx context.Context, listener net.Listener, target netip.AddrPort) (*trafficProxy, error) {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.AddrPort().Addr().IsLoopback() || address.Port < 1024 || !target.Addr().IsLoopback() || target.Port() < 1024 {
		return nil, fmt.Errorf("traffic proxy requires exact loopback endpoints")
	}
	p := &trafficProxy{listener: listener, target: target, connections: make(map[net.Conn]struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				client.Close()
				return
			}
			p.connections[client] = struct{}{}
			p.counters.opened.Add(1)
			p.counters.active.Add(1)
			p.wg.Add(1)
			p.mu.Unlock()
			go p.forward(ctx, client)
		}
	}()
	return p, nil
}

func (p *trafficProxy) forward(ctx context.Context, client net.Conn) {
	defer p.wg.Done()
	defer p.counters.active.Add(-1)
	defer client.Close()
	defer func() {
		p.mu.Lock()
		delete(p.connections, client)
		p.mu.Unlock()
	}()
	dial, cancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := (&net.Dialer{}).DialContext(dial, "tcp4", p.target.String())
	cancel()
	if err != nil {
		p.counters.dialFailures.Add(1)
		return
	}
	defer upstream.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.connections[upstream] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.connections, upstream)
		p.mu.Unlock()
	}()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		io.CopyBuffer(trafficCountWriter{upstream, &p.counters.toRelay}, client, make([]byte, 32<<10))
		client.Close()
		upstream.Close()
	}()
	io.CopyBuffer(trafficCountWriter{client, &p.counters.fromRelay}, upstream, make([]byte, 32<<10))
	client.Close()
	upstream.Close()
	<-upDone
}

func (p *trafficProxy) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.listener.Close()
		for connection := range p.connections {
			connection.Close()
		}
		p.mu.Unlock()
	})
	p.wg.Wait()
	return nil
}

func trafficSleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// This is a test-only overlay on the exact released source. Unlike the release
// binary, this test compiles out OS UDP. Only the loopback byte proxy is added;
// stock Tailcat, certificate pins, pairing, and relay admission remain intact.
func TestRelayTrafficObservation(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_TRAFFIC_MEASUREMENT") != "1" {
		t.Skip("requires explicit isolated hosted measurement")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("GitHub-hosted Linux amd64 measurement required")
	}
	if buildfeatures.HasUDPTransport || ValidateBuild() != nil {
		t.Fatal("isolated relay measurement build tags required")
	}
	fixtureHash := os.Getenv("SOBALINK_TRAFFIC_FIXTURE_SHA256")
	if !validKey(fixtureHash) || os.Getenv("SOBALINK_TRAFFIC_SOURCE_COMMIT") != trafficSourceCommit {
		t.Fatal("exact measurement source identity required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	report := relayTrafficReport{Schema: 1, Evidence: "source_built_relay_only", SourceCommit: trafficSourceCommit,
		FixtureSHA256: fixtureHash, BuildTags: trafficBuildTags, Target: "linux-amd64", GoVersion: runtime.Version(),
		Outcome: "in_progress", Phases: []trafficPhase{}, ResidualConnections: -1,
		Scope:    "successful_encrypted_stream_writes_at_one_proxy_boundary_per_relay_leg",
		Workload: "tailcat_tcp_echo",
		Excludes: []string{"outer_tcp_ip_ethernet_headers", "tcp_ack_only_packets", "kernel_retransmissions", "icmp_diagnostics", "direct_udp", "separate_local_admission_http", "real_wan_nat", "released_binary_memory", "sobalink_file_message_protocol", "peer_service_discovery", "web_ui_polling"}}
	emit := func() {
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Error("could not serialize aggregate measurement")
			return
		}
		fmt.Fprintf(os.Stdout, "%s%s\n", trafficReportPrefix, encoded)
	}
	var closers []io.Closer
	var proxy *trafficProxy
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := len(closers) - 1; i >= 0; i-- {
					closers[i].Close()
				}
			}()
			select {
			case <-done:
				if proxy != nil {
					report.ResidualConnections = proxy.counters.active.Load()
					report.CleanupComplete = report.ResidualConnections == 0
				}
			case <-time.After(10 * time.Second):
				t.Error("measurement teardown did not complete")
			}
		})
	}
	defer func() {
		cleanup()
		if t.Failed() || !report.CleanupComplete {
			report.Outcome = "failed"
		} else {
			report.Outcome = "passed"
		}
		emit()
	}()
	outer, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not create loopback counter listener")
	}
	closers = append(closers, outer)
	outerAddress := outer.Addr().(*net.TCPAddr).AddrPort()
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not select isolated inner relay port")
	}
	innerAddress := probe.Addr().(*net.TCPAddr).AddrPort()
	probe.Close()
	identity, err := GenerateRelayIdentity(outerAddress.Addr())
	if err != nil {
		t.Fatal("could not create synthetic relay identity")
	}
	relayConfig, err := identity.Endpoint(outerAddress)
	if err != nil {
		t.Fatal("could not pin synthetic relay certificate")
	}
	var saves, admissions atomic.Uint64
	persist := func(Snapshot, []RemotePeer) error { saves.Add(1); return nil }
	host, err := NewNode(NodeConfig{Identity: GenerateIdentity(), Relay: relayConfig, Trust: NewBook(), EmbeddedRelay: true, Persist: persist})
	if err != nil {
		t.Fatal("could not create isolated host")
	}
	closers = append(closers, host)
	allow := func(key string) bool {
		allowed := host.AllowRelayKey(key)
		if allowed {
			admissions.Add(1)
		}
		return allowed
	}
	relay, err := StartLocalRelay(ctx, innerAddress, identity, allow, host.AuthorizeRelayBootstrap)
	if err != nil {
		t.Fatal("could not start isolated pinned relay")
	}
	closers = append(closers, relay)
	proxy, err = startTrafficProxy(ctx, outer, innerAddress)
	if err != nil {
		t.Fatal("could not start isolated stream counter")
	}
	closers = append(closers, proxy)
	finishPhase := func(name string, before trafficSnapshot, started time.Time, payload time.Duration, sent, received, beforeAdmissions uint64) trafficPhase {
		phase, err := summarizeTraffic(name, before, proxy.counters.snapshot(), time.Since(started), payload, sent, received, admissions.Load()-beforeAdmissions)
		if err != nil || phase.DialFailures != 0 || phase.RelayExcessBytes < 0 {
			t.Fatal("invalid relay phase counters")
		}
		report.Phases = append(report.Phases, phase)
		emit()
		return phase
	}
	startupStart, startupCounters, startupAdmissions := time.Now(), proxy.counters.snapshot(), admissions.Load()
	if _, err = host.ServePairing(ctx); err != nil {
		t.Fatal("could not start isolated host pairing")
	}
	client, err := NewNode(NodeConfig{Identity: GenerateIdentity(), Relay: relayConfig, Trust: NewBook(), Persist: persist})
	if err != nil {
		t.Fatal("could not create isolated client")
	}
	closers = append(closers, client)
	if _, err = client.ServePairing(ctx); err != nil {
		t.Fatal("could not start isolated client pairing")
	}
	invitation, err := host.IssueInvitation(ctx, Peer{Key: client.PublicKey(), Name: "fixture-client"}, "fixture-host", time.Minute)
	if err != nil || client.PairInvitation(ctx, invitation) != nil || saves.Load() != 2 {
		t.Fatal("synthetic pinned pairing did not complete")
	}
	listener, err := host.ListenPeer(ctx, "tcp", 32101)
	if err != nil {
		t.Fatal("could not open isolated overlay echo service")
	}
	closers = append(closers, listener)
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- connection
		defer connection.Close()
		io.CopyBuffer(connection, connection, make([]byte, 32<<10))
	}()
	conn, err := client.DialPeer(ctx, host.PublicKey(), "tcp", 32101)
	if err != nil {
		t.Fatal("could not dial isolated overlay echo service")
	}
	closers = append(closers, conn)
	select {
	case incoming := <-accepted:
		if incoming == nil {
			t.Fatal("isolated echo service did not accept")
		}
		closers = append(closers, incoming)
		if key, ok := host.PeerKey(incoming.RemoteAddr()); !ok || key != client.PublicKey() {
			t.Fatal("isolated peer identity was not preserved")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("isolated echo accept timed out")
	}
	finishPhase("startup_pairing", startupCounters, startupStart, 0, 0, 0, startupAdmissions)
	for _, phase := range []struct {
		name     string
		duration time.Duration
	}{{"idle", 60 * time.Second}} {
		started, before, admitted := time.Now(), proxy.counters.snapshot(), admissions.Load()
		if !trafficSleep(ctx, phase.duration) {
			t.Fatal("idle observation timed out")
		}
		finishPhase(phase.name, before, started, 0, 0, 0, admitted)
	}
	echo := func(size int64, duration time.Duration) time.Duration {
		conn.SetDeadline(time.Now().Add(duration))
		started := time.Now()
		written := make(chan error, 1)
		go func() {
			n, err := io.CopyN(conn, &trafficPattern{}, size)
			if err == nil && n != size {
				err = io.ErrShortWrite
			}
			written <- err
		}()
		actual := sha256.New()
		n, err := io.CopyN(actual, conn, size)
		if err != nil || n != size {
			t.Fatal("relay payload echo was incomplete")
		}
		select {
		case err := <-written:
			if err != nil {
				t.Fatal("relay payload write was incomplete")
			}
		case <-ctx.Done():
			t.Fatal("relay payload writer timed out")
		}
		elapsed := time.Since(started)
		expected := sha256.New()
		io.CopyN(expected, &trafficPattern{}, size)
		if !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) {
			t.Fatal("relay payload digest mismatch")
		}
		conn.SetDeadline(time.Time{})
		return elapsed
	}
	for _, size := range []int64{1 << 20, 16 << 20, 64 << 20} {
		started, before, admitted := time.Now(), proxy.counters.snapshot(), admissions.Load()
		elapsed := echo(size, 60*time.Second)
		if !trafficSleep(ctx, 200*time.Millisecond) {
			t.Fatal("counter settling timed out")
		}
		finishPhase(fmt.Sprintf("bulk_%d", size), before, started, elapsed, uint64(size), uint64(size), admitted)
	}
	{
		started, before, admitted := time.Now(), proxy.counters.snapshot(), admissions.Load()
		var elapsed time.Duration
		for range 1000 {
			if time.Since(started) > 60*time.Second {
				t.Fatal("small-message phase timed out")
			}
			elapsed += echo(256, 5*time.Second)
		}
		if !trafficSleep(ctx, 200*time.Millisecond) {
			t.Fatal("counter settling timed out")
		}
		finishPhase("small_tcp_frames", before, started, elapsed, 1000*256, 1000*256, admitted)
	}
	{
		started, before, admitted := time.Now(), proxy.counters.snapshot(), admissions.Load()
		var elapsed time.Duration
		var frames uint64
		for time.Since(started) < 130*time.Second {
			elapsed += echo(64, 10*time.Second)
			frames++
			if !trafficSleep(ctx, time.Second) {
				t.Fatal("relay lease observation timed out")
			}
		}
		// A final payload proves this same application TCP stream survived past
		// the lease observation interval. No application redial/retry is used.
		elapsed += echo(64, 10*time.Second)
		frames++
		if !trafficSleep(ctx, 200*time.Millisecond) {
			t.Fatal("counter settling timed out")
		}
		phase := finishPhase("lease_reconnect", before, started, elapsed, frames*64, frames*64, admitted)
		if phase.ConnectionsOpened == 0 || phase.Admissions == 0 {
			t.Fatal("no actual relay reconnection and readmission observed")
		}
	}
	{
		started, before, admitted := time.Now(), proxy.counters.snapshot(), admissions.Load()
		if !trafficSleep(ctx, 30*time.Second) {
			t.Fatal("cooldown observation timed out")
		}
		finishPhase("cooldown_idle", before, started, 0, 0, 0, admitted)
	}
}
