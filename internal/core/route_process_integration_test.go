//go:build lanlink_integration

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"tailscale.com/feature/buildfeatures"
)

// This helper exists only in the test executable. Commands and synthetic private
// pairing frames travel over inherited pipes, never argv or CI output. Each
// child owns a genuine Core, protected directory and transport engine.
func TestRouteCoreProcessHelper(t *testing.T) {
	if os.Getenv("SOBALINK_ROUTE_PROCESS_HELPER") != "1" {
		t.Skip("parent-owned native fixture")
	}
	dir := os.Getenv("SOBALINK_ROUTE_PROCESS_DIR")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Open(ctx, Options{Directory: dir, Version: "process-fixture", SkipNetworkStart: os.Getenv("SOBALINK_ROUTE_PROCESS_OFFLINE") == "1"})
	if err != nil {
		t.Fatal("open isolated subprocess Core")
	}
	defer c.Close()
	var closers []io.Closer
	defer func() {
		for _, v := range closers {
			v.Close()
		}
	}()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			Name    string
			Payload json.RawMessage
		}
		if decoder.Decode(&request) != nil {
			return
		}
		var value any
		var callErr error
		switch request.Name {
		case "fixture.exit":
			encoder.Encode(processReply{Value: json.RawMessage(`true`)})
			return
		case "fixture.selection":
			value = c.lanStoreCopy().copy().Selection
		case "fixture.identity":
			s := c.lanStoreCopy().copy()
			type role struct {
				Peer, Incoming string
				Address        any
				Client         any
			}
			roles := []role{}
			for _, r := range s.Remotes {
				roles = append(roles, role{r.Peer.Key, r.IncomingClientKey, r.Address, r.ClientPrivate})
			}
			raw, _ := json.Marshal(struct {
				Identity lanlink.Identity
				Roles    []role
			}{s.Identity, roles})
			sum := sha256.Sum256(raw)
			value = hex.EncodeToString(sum[:])
		case "fixture.alternative":
			var address string
			callErr = json.Unmarshal(request.Payload, &address)
			if callErr == nil {
				var ap netip.AddrPort
				ap, callErr = netip.ParseAddrPort(address)
				if callErr == nil && !ap.Addr().IsLoopback() {
					callErr = errors.New("fixture requires loopback")
				}
				if callErr == nil {
					var id lanlink.RelayIdentity
					id, callErr = lanlink.GenerateRelayIdentity(ap.Addr())
					if callErr == nil {
						raw, _ := json.Marshal(id)
						callErr = config.AtomicWritePrivate(filepath.Join(dir, "fixture-relay.json"), raw)
					}
					if callErr == nil {
						var relay lanlink.TrustedRelay
						relay, callErr = id.Endpoint(ap)
						value = map[string]any{"address": address, "certificateSHA256": relay.CertificateSHA256, "scope": "external"}
					}
				}
			}
		case "fixture.start-alternative":
			var address string
			callErr = json.Unmarshal(request.Payload, &address)
			if callErr == nil {
				var id lanlink.RelayIdentity
				callErr = config.ReadJSON(filepath.Join(dir, "fixture-relay.json"), &id)
				if callErr == nil {
					node, done, e := c.routeControl()
					callErr = e
					if e == nil {
						ap, e := netip.ParseAddrPort(address)
						callErr = e
						if e == nil {
							var relay *lanlink.LocalRelay
							relay, callErr = lanlink.StartLocalRelay(ctx, ap, id, node.AllowRelayKey, node.AuthorizeRelayBootstrap)
							if callErr == nil {
								closers = append(closers, relay)
							}
						}
						done()
					}
				}
			}
		case "fixture.cut-primary":
			backend, ok := c.nodeCopy().(*lanBackend)
			if !ok {
				callErr = errors.New("missing LAN backend")
			} else {
				backend.mu.Lock()
				if backend.relay != nil {
					callErr = backend.relay.Close()
					backend.relay = nil
				}
				backend.mu.Unlock()
			}
		case "fixture.echo":
			var listener net.Listener
			listener, callErr = routeFixtureListener()
			if callErr == nil {
				closers = append(closers, listener)
				value = listener.Addr().(*net.TCPAddr).Port
				go func() {
					for {
						stream, e := listener.Accept()
						if e != nil {
							return
						}
						go func() {
							defer stream.Close()
							stream.SetDeadline(time.Now().Add(10 * time.Second))
							io.Copy(stream, stream)
						}()
					}
				}()
			}
		default:
			call, stop := context.WithTimeout(ctx, 30*time.Second)
			value, callErr = c.Command(call, webui.Command{RequestID: randomID(), Name: request.Name, Payload: request.Payload})
			stop()
		}
		raw, _ := json.Marshal(value)
		reply := processReply{Value: raw}
		if callErr != nil {
			reply.Error = request.Name + ": " + networkErrorCode(callErr)
			if reply.Error == request.Name+": " {
				reply.Error += "failed"
			}
		}
		if encoder.Encode(reply) != nil {
			return
		}
	}
}

type processReply struct {
	Value json.RawMessage
	Error string
}
type routeCoreProcess struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	done    chan error
	stopped bool
}

func startRouteCoreProcess(t *testing.T, ctx context.Context, dir string, offline bool) *routeCoreProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("locate native test executable")
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRouteCoreProcessHelper$", "-test.timeout=4m")
	mode := "0"
	if offline {
		mode = "1"
	}
	cmd.Env = append(os.Environ(), "SOBALINK_ROUTE_PROCESS_HELPER=1", "SOBALINK_ROUTE_PROCESS_DIR="+dir, "SOBALINK_ROUTE_PROCESS_OFFLINE="+mode)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("open fixture input")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("open fixture output")
	}
	// Do not retain diagnostics that might contain synthetic private frames.
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		t.Fatal("start independent Core process")
	}
	p := &routeCoreProcess{cmd: cmd, input: in, output: json.NewDecoder(out), done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		in.Close()
		if !p.stopped {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				t.Error("fixture process cleanup timed out")
			}
			p.stopped = true
		}
	})
	return p
}
func (p *routeCoreProcess) invoke(t *testing.T, name string, payload any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(payload)
	if json.NewEncoder(p.input).Encode(struct {
		Name    string
		Payload json.RawMessage
	}{name, raw}) != nil {
		t.Fatalf("subprocess command write %s", name)
	}
	ch := make(chan processReply, 1)
	go func() {
		var reply processReply
		if p.output.Decode(&reply) != nil {
			reply.Error = "subprocess response unavailable"
		}
		ch <- reply
	}()
	select {
	case reply := <-ch:
		if reply.Error != "" {
			t.Fatalf("subprocess command %s: %s", name, reply.Error)
		}
		return reply.Value
	case <-time.After(40 * time.Second):
		p.cmd.Process.Kill()
		t.Fatalf("subprocess command timed out: %s", name)
	}
	return nil
}
func (p *routeCoreProcess) stop(t *testing.T) {
	t.Helper()
	p.invoke(t, "fixture.exit", nil)
	p.input.Close()
	select {
	case err := <-p.done:
		p.stopped = true
		if err != nil {
			t.Fatal("Core child did not shut down cleanly")
		}
	case <-time.After(10 * time.Second):
		p.cmd.Process.Kill()
		t.Fatal("Core child shutdown timed out")
	}
}

// Two genuine Core processes, unchanged product transport, protected restart,
// and one actual service listener. Loopback topology proves process isolation
// and application recovery, not physical WAN/NAT/suspend or TCP continuity.
func TestCoreTwoProcessRouteRecoveryIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" && os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native CI only")
	}
	if lanlink.ValidateBuild() != nil {
		t.Fatal("unsafe transport feature build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	hostDir, guestDir := t.TempDir(), t.TempDir()
	// Reserve two distinct ports; B stays unavailable through saved cold start.
	a, err := routeFixtureListener()
	if err != nil {
		t.Fatal("reserve A")
	}
	b, err := routeFixtureListener()
	if err != nil {
		a.Close()
		t.Fatal("reserve B")
	}
	defer b.Close()
	aAddr, bAddr := a.Addr().String(), b.Addr().String()
	a.Close()
	host, guest := startRouteCoreProcess(t, ctx, hostDir, true), startRouteCoreProcess(t, ctx, guestDir, true)
	var hostIdentity, guestIdentity map[string]string
	json.Unmarshal(host.invoke(t, "lan.identity", map[string]any{}), &hostIdentity)
	json.Unmarshal(guest.invoke(t, "lan.identity", map[string]any{}), &guestIdentity)
	hk, gk := hostIdentity["publicKey"], guestIdentity["publicKey"]
	if hk == "" || gk == "" || hk == gk {
		t.Fatal("distinct process identities required")
	}
	host.invoke(t, "network.configure", map[string]any{"mode": "lan", "hostname": "process-host", "lan": LANSelection{Kind: "host", Address: aAddr}})
	var selection LANSelection
	json.Unmarshal(host.invoke(t, "fixture.selection", nil), &selection)
	selection.Kind = "relay"
	guest.invoke(t, "network.configure", map[string]any{"mode": "lan", "hostname": "process-guest", "lan": selection})
	var invitation map[string]any
	json.Unmarshal(host.invoke(t, "lan.invite", map[string]any{"recipientPublicKey": gk, "name": "process-guest", "ttlSeconds": 120}), &invitation)
	guest.invoke(t, "lan.join", map[string]any{"invitation": invitation["invitation"]})
	var alt map[string]any
	json.Unmarshal(host.invoke(t, "fixture.alternative", bAddr), &alt)
	beforeHost, beforeGuest := string(host.invoke(t, "fixture.identity", nil)), string(guest.invoke(t, "fixture.identity", nil))
	host.stop(t)
	guest.stop(t)
	host, guest = startRouteCoreProcess(t, ctx, hostDir, true), startRouteCoreProcess(t, ctx, guestDir, true)
	host.invoke(t, "lan.routes.add", alt)
	alternativeID, primaryID := "", ""
	guest.invoke(t, "lan.routes.add", alt)
	for _, pair := range []struct {
		from, to          *routeCoreProcess
		recipient, issuer string
	}{{host, guest, gk, hk}, {guest, host, hk, gk}} {
		var offer, review map[string]any
		json.Unmarshal(pair.from.invoke(t, "lan.routes.export", map[string]any{"peerId": pair.recipient, "lifetime": "until-revoked"}), &offer)
		json.Unmarshal(pair.to.invoke(t, "lan.routes.inspect", map[string]any{"peerId": pair.issuer, "update": offer["update"]}), &review)
		ids := []string{}
		for _, candidate := range review["candidates"].([]any) {
			view := candidate.(map[string]any)
			id := view["candidateId"].(string)
			ids = append(ids, id)
			if view["scope"] == "external" {
				alternativeID = id
			} else {
				primaryID = id
			}
		}
		pair.to.invoke(t, "lan.routes.apply", map[string]any{"peerId": pair.issuer, "update": offer["update"], "digest": review["digest"], "candidateIds": ids, "expires": review["expires"], "lifetime": "until-revoked"})
	}
	grantEvidence := func(p *routeCoreProcess, peer string) string {
		var snapshot map[string]any
		if json.Unmarshal(p.invoke(t, "lan.routes.list", map[string]any{"peerId": peer}), &snapshot) != nil {
			t.Fatal("decode route evidence")
		}
		stable := map[string]any{}
		for _, key := range []string{"issuedSequence", "receivedSequence", "candidates", "approvals", "permittedIds", "lifetime", "expires", "nextExpiry"} {
			stable[key] = snapshot[key]
		}
		raw, err := json.Marshal(stable)
		if err != nil {
			t.Fatal("encode route evidence")
		}
		return string(raw)
	}
	beforeHostGrants, beforeGuestGrants := grantEvidence(host, gk), grantEvidence(guest, hk)
	host.stop(t)
	guest.stop(t)
	host, guest = startRouteCoreProcess(t, ctx, hostDir, false), startRouteCoreProcess(t, ctx, guestDir, false)
	if string(host.invoke(t, "fixture.identity", nil)) != beforeHost || string(guest.invoke(t, "fixture.identity", nil)) != beforeGuest {
		t.Fatal("process cold start changed pair identity or role keys")
	}
	if grantEvidence(host, gk) != beforeHostGrants || grantEvidence(guest, hk) != beforeGuestGrants {
		t.Fatal("cold start changed exact offer or local approval evidence")
	}
	for _, pair := range []struct {
		p    *routeCoreProcess
		peer string
	}{{host, gk}, {guest, hk}} {
		var snapshot map[string]any
		json.Unmarshal(pair.p.invoke(t, "lan.routes.list", map[string]any{"peerId": pair.peer}), &snapshot)
		if len(snapshot["permittedIds"].([]any)) != 2 {
			t.Fatal("cold start lost route authorization")
		}
		pair.p.invoke(t, "peer.trust", map[string]any{"peerId": pair.peer, "trusted": true})
	}
	var targetPort int
	json.Unmarshal(host.invoke(t, "fixture.echo", nil), &targetPort)
	host.invoke(t, "service.share", map[string]any{"name": "process-echo", "network": "tcp", "remotePort": targetPort, "peerIds": []string{gk}, "lifetime": "until-revoked"})
	// Explicit reserved local endpoint is stable across candidate changes.
	local, err := routeFixtureListener()
	if err != nil {
		t.Fatal("reserve service entry")
	}
	localPort := local.Addr().(*net.TCPAddr).Port
	local.Close()
	var forward map[string]any
	json.Unmarshal(guest.invoke(t, "service.connect", map[string]any{"name": "process-forward", "network": "tcp", "remotePort": targetPort, "localPort": localPort, "peerId": hk, "lifetime": "until-stopped"}), &forward)
	endpoint, _ := forward["endpoint"].(string)
	if endpoint == "" {
		t.Fatal("Core omitted actual local service endpoint")
	}
	exchange := func() bool {
		stream, e := net.DialTimeout("tcp", endpoint, time.Second)
		if e != nil {
			return false
		}
		defer stream.Close()
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e = stream.Write([]byte("process-echo")); e != nil {
			return false
		}
		reply := make([]byte, 12)
		_, e = io.ReadFull(stream, reply)
		return e == nil && string(reply) == "process-echo"
	}
	await := func(label string) {
		t.Helper()
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			if exchange() {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("same localhost endpoint did not recover: %s", label)
	}
	await("offline LAN cold start with alternative unavailable")
	b.Close()
	host.invoke(t, "fixture.start-alternative", bAddr)
	host.invoke(t, "fixture.cut-primary", nil)
	await("primary relay removed")
	var routes map[string]any
	json.Unmarshal(guest.invoke(t, "lan.routes.list", map[string]any{"peerId": hk}), &routes)
	observation, ok := routes["observation"].(map[string]any)
	if !ok || observation["state"] != "ready" || (observation["path"] != "direct" && observation["path"] != "relay") {
		t.Fatal("application recovery lacked fresh authoritative route observation")
	}
	if !buildfeatures.HasUDPTransport && observation["candidateId"] != alternativeID {
		t.Fatal("relay-only recovery did not reach approved alternative")
	}
	// Direct UDP can legitimately remain healthy after relay A disappears.
	// Separately prove a deliberate grant reduction selects B in that build.
	if buildfeatures.HasUDPTransport {
		guest.invoke(t, "lan.routes.revoke", map[string]any{"peerId": hk, "candidateIds": []string{primaryID}})
		await("explicit primary grant removal with direct UDP enabled")
		json.Unmarshal(guest.invoke(t, "lan.routes.list", map[string]any{"peerId": hk}), &routes)
		observation, _ = routes["observation"].(map[string]any)
		if observation["candidateId"] != alternativeID {
			t.Fatal("normal build did not select remaining approved candidate")
		}
	}
	var servicePage struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(guest.invoke(t, "service.list", map[string]any{}), &servicePage) != nil {
		t.Fatal("decode recovered service")
	}
	sameService := false
	for _, service := range servicePage.Items {
		if service["id"] != forward["id"] {
			continue
		}
		sameService = true
		for _, key := range []string{"endpoint", "lifetime", "expiresAt", "ttlSeconds", "peerId", "ports", "localPort"} {
			a, _ := json.Marshal(service[key])
			b, _ := json.Marshal(forward[key])
			if string(a) != string(b) {
				t.Fatalf("recovery changed service authority or endpoint: %s", key)
			}
		}
	}
	if !sameService {
		t.Fatal("recovery replaced the original local service")
	}
	// The echo above uses the exact initial endpoint, without another start call.
	guest.invoke(t, "lan.routes.revoke", map[string]any{"peerId": hk})
	if exchange() {
		t.Fatal("revocation retained usable outgoing application route")
	}
	t.Logf("two independent Core processes preserved identity, grants and local service endpoint; normal UDP enabled=%t", buildfeatures.HasUDPTransport)
	guest.stop(t)
	host.stop(t)
}

// Avoid the product's fixed peer-control ports even on runners whose ephemeral
// range overlaps them. Every OS listener in this fixture is numeric loopback.
func routeFixtureListener() (net.Listener, error) {
	for range 16 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if port >= 1024 && (port < int(DiscoveryPort) || port > int(lanlink.PairingPort)) {
			return listener, nil
		}
		listener.Close()
	}
	return nil, errors.New("no unreserved loopback fixture port")
}
