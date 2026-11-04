//go:build native_integration

package main

import (
	"bytes"
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
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/identity"
	"github.com/webkaz-labs/sobalink/internal/policy"
)

// This exercises the native adapter, real protected control socket/named pipe,
// two real Core instances in an isolated helper subprocess, and their unchanged
// peer HTTP message/file protocol.
// Only peer inventory, identity and stream transport are injected fixtures.
// net.Pipe never contacts a tailnet, LAN, login service, or user device. This is
// not installed soba binary, UI-click, or real-enrollment acceptance.
// A socket/pipe failure is a test failure, never a fallback or silent skip.
func TestNativeProtectedIPCCoreExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	aDir, bDir := nativeStartCoreDaemon(t, ctx)
	a, b := newExchangeClient(aDir, true, nil), newExchangeClient(bDir, true, nil)
	load := func(t *testing.T, dir string) snapshot {
		t.Helper()
		state, err := ipcLoader(dir)(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := load(t, aDir)
	if before.ResourceCatalogProcessID == "" || len(before.Peers) != 1 || !before.Peers[0].Trusted {
		t.Fatal("actual Core snapshot lacks selected process/trusted fixture peer")
	}

	t.Run("message delivery and replay", func(t *testing.T) {
		text := "Fictional fixture message / 架空のテストメッセージ"
		sent, err := a.SendMessage(ctx, "native-message-a", "fixture-b", text)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := a.SendMessage(ctx, "native-message-a", "fixture-b", text)
		if err != nil || repeated != sent {
			t.Fatal("same request did not return the same message", err)
		}
		if _, err := a.SendMessage(ctx, "native-message-a", "fixture-b", "changed fixture text"); err == nil {
			t.Fatal("Core accepted a changed payload under an existing request ID")
		}
		received := load(t, bDir).Messages
		if len(received) != 1 || received[0].ID != sent.ID || received[0].Text != text || received[0].PeerID != "fixture-a" || received[0].Direction != "incoming" || received[0].Status != "received" {
			t.Fatal("receiver snapshot does not contain exactly one genuine received message")
		}
		reply, err := b.SendMessage(ctx, "native-message-b", "fixture-a", "Fixture reply / テスト返信")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range load(t, aDir).Messages {
			if row.ID == reply.ID && row.Direction == "incoming" && row.Status == "received" && row.Text == reply.Text {
				found = true
			}
		}
		if !found {
			t.Fatal("reverse message did not arrive")
		}
	})
	if t.Failed() {
		return
	}

	t.Run("review commit accept and saved bytes", func(t *testing.T) {
		payload := []byte("Fictional file contents / 架空のファイル内容\n")
		source := filepath.Join(t.TempDir(), "fixture.txt")
		if err := os.WriteFile(source, payload, 0600); err != nil {
			t.Fatal(err)
		}
		review, err := a.PrepareFiles(ctx, "native-file-prepare", before.ResourceCatalogProcessID, []string{"fixture-b"}, []string{source})
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		if len(review.Entries) != 1 || review.Entries[0].SHA256 != hex.EncodeToString(digest[:]) || review.TotalBytes != int64(len(payload)) {
			t.Fatal("review did not describe the actual staged file")
		}
		if len(load(t, bDir).Transfers) != 0 {
			t.Fatal("preparation sent a peer offer before confirmation")
		}
		if _, err := a.CommitFiles(ctx, "native-file-unconfirmed", review, false); !errors.Is(err, errExchangeReview) {
			t.Fatal("unconfirmed commit was not blocked", err)
		}
		receipt, err := a.CommitFiles(ctx, "native-file-commit", review, true)
		if err != nil {
			t.Fatal(err)
		}
		again, err := a.CommitFiles(ctx, "native-file-commit", review, true)
		if err != nil || !reflect.DeepEqual(receipt, again) {
			t.Fatal("commit replay created a different dispatch", err)
		}
		status, err := a.FileStatus(ctx, "native-file-status", review.ProcessID, review.PreparationID)
		if err != nil || status.Status != "committed" || status.DispatchID != receipt.DispatchID {
			t.Fatal("dispatch status mismatch", err)
		}
		id := receipt.Children[0].TransferID
		waitTransfer := func(dir, expected string) exchangeTransfer {
			t.Helper()
			var result exchangeTransfer
			lastSenderState := ""
			observations := 0
			nativeAwait(t, ctx, "transfer "+expected, func() bool {
				for _, sender := range load(t, aDir).Transfers {
					if sender.ID != id {
						continue
					}
					if sender.Status != lastSenderState && observations < 8 {
						t.Logf("fixture sender state=%s completed_bytes=%d", sender.Status, sender.CompletedBytes)
						lastSenderState = sender.Status
						observations++
					}
					if sender.Status == "failed" || sender.Status == "cancelled" || sender.Status == "declined" {
						t.Fatalf("sender terminated before expected receiver state: %s", sender.Status)
					}
				}
				for _, row := range load(t, dir).Transfers {
					if row.ID == id {
						result = row
						if row.Status == "failed" || row.Status == "cancelled" || row.Status == "declined" {
							t.Fatalf("unexpected transfer state %s", row.Status)
						}
						return row.Status == expected
					}
				}
				return false
			})
			return result
		}
		pending := waitTransfer(bDir, "awaiting-acceptance")
		if pending.Direction != "incoming" || pending.PeerID != "fixture-a" {
			t.Fatal("incoming offer identity mismatch")
		}
		destination := t.TempDir()
		if err := b.AcceptFiles(ctx, "native-file-no-review", id, destination, false); !errors.Is(err, errExchangeReview) {
			t.Fatal("acceptance bypassed review", err)
		}
		if err := b.AcceptFiles(ctx, "native-file-accept", id, destination, true); err != nil {
			t.Fatal(err)
		}
		incoming, outgoing := waitTransfer(bDir, "completed"), waitTransfer(aDir, "completed")
		if incoming.CompletedBytes != int64(len(payload)) || outgoing.CompletedBytes != int64(len(payload)) || len(incoming.Entries) != 1 {
			t.Fatal("completion byte counts do not match")
		}
		saved := incoming.Entries[0].StoredPath
		if saved == "" || filepath.IsAbs(saved) || !filepath.IsLocal(saved) {
			t.Fatal("receiver returned an unsafe or empty stored name")
		}
		// Core may create a private per-batch subdirectory. Inspect only the explicit
		// temporary destination, and require exactly one file with the expected name.
		var matches []string
		err = filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && entry.Name() == filepath.Base(saved) {
				matches = append(matches, path)
			}
			return nil
		})
		if err != nil || len(matches) != 1 {
			t.Fatal("expected exactly one stored fixture file", err)
		}
		contents, err := os.ReadFile(matches[0])
		if err != nil || !bytes.Equal(contents, payload) || sha256.Sum256(contents) != digest {
			t.Fatal("receiver bytes/hash mismatch", err)
		}
		if len(load(t, aDir).Transfers) != 1 || len(load(t, bDir).Transfers) != 1 {
			t.Fatal("replayed dispatch duplicated transfers")
		}
	})
}

// The child entry point is test-only. No environment flag or fixture backend is
// added to the production daemon. EOF on its inherited stdin requests shutdown.
func TestNativeCoreDaemonProcessHelper(t *testing.T) {
	if os.Getenv("SOBALINK_NATIVE_CORE_FIXTURE") != "1" {
		t.Skip("parent-owned daemon fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub := &nativePeerHub{listeners: make(map[netip.AddrPort]*nativePeerListener)}
	aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	aDir := nativeCoreEndpoint(t, ctx, &nativePeerNode{hub: hub, ip: aIP, peerIP: bIP, peerID: "fixture-b"})
	bDir := nativeCoreEndpoint(t, ctx, &nativePeerNode{hub: hub, ip: bIP, peerIP: aIP, peerID: "fixture-a"})
	// Wait for genuine Core maintenance, never fabricate a ready snapshot or
	// directly invoke private lifecycle helpers.
	nativeAwait(t, ctx, "both peer listeners", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.listeners) == 4
	})
	if err := json.NewEncoder(os.Stdout).Encode([2]string{aDir, bDir}); err != nil {
		t.Fatal(err)
	}
	eof := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, os.Stdin); eof <- err }()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("daemon fixture did not receive parent shutdown")
	}
}

func nativeStartCoreDaemon(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestNativeCoreDaemonProcessHelper$", "-test.timeout=70s")
	child.Env = append(os.Environ(), "SOBALINK_NATIVE_CORE_FIXTURE=1")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	var diagnostics, outputLog bytes.Buffer
	child.Stderr = &diagnostics
	if err := child.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		// Drain remaining test-runner output, then join the child so its protected
		// endpoints, Core owners, lifecycle locks and temporary files are all closed.
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(&outputLog, output); close(drained) }()
		joined := make(chan error, 1)
		go func() { joined <- child.Wait() }()
		select {
		case err := <-joined:
			<-drained
			if err != nil {
				t.Errorf("Core daemon helper failed: %v; %s; %s", err, diagnostics.String(), outputLog.String())
			}
		case <-time.After(8 * time.Second):
			_ = child.Process.Kill()
			<-joined
			t.Error("Core daemon helper failed to stop after stdin EOF")
		}
		<-drained
	})
	type startup struct {
		dirs [2]string
		err  error
	}
	ready := make(chan startup, 1)
	go func() {
		var result startup
		result.err = json.NewDecoder(io.TeeReader(output, &outputLog)).Decode(&result.dirs)
		ready <- result
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatalf("Core daemon helper startup: %v; %s", result.err, outputLog.String())
		}
		if result.dirs[0] == "" || result.dirs[1] == "" || result.dirs[0] == result.dirs[1] {
			t.Fatal("daemon helper returned invalid endpoints")
		}
		return result.dirs[0], result.dirs[1]
	case <-ctx.Done():
		// Unblock the startup reader before cleanup starts draining the same pipe.
		_ = child.Process.Kill()
		<-ready
		t.Fatal("Core daemon helper startup timed out")
		return "", ""
	}
}

func nativeAwait(t *testing.T, ctx context.Context, label string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

func nativeCoreEndpoint(t *testing.T, ctx context.Context, node *nativePeerNode) string {
	t.Helper()
	dir, app := nativeOpenCore(t, ctx, node)
	server, err := control.ServeWithLimits(ctx, dir, app.IPC, app.LocalControlLimits)
	if err != nil {
		t.Fatal("real protected IPC endpoint unavailable:", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(filepath.Join(dir, "control.sock"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			t.Fatal("control endpoint is not a private native socket")
		}
	}
	return dir
}

func nativeOpenCore(t *testing.T, ctx context.Context, node *nativePeerNode) (string, *core.Core) {
	t.Helper()
	// Short private path keeps Unix socket addresses below the native path limit.
	dir, err := os.MkdirTemp("", "soba-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := config.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	// Start from a genuinely fresh profile. Prewriting only sobalink.json makes
	// Core treat absent receive-accounting state as legacy recovery, correctly
	// refusing file offers. Let Core create its complete owned state instead.
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	})
	app, err := core.Open(ctx, core.Options{Directory: dir, Version: "native-fixture", LifecycleLock: lock, NodeFactory: func(string, string) (core.NetworkBackend, error) { return node, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, command := range []struct {
		ID, Name string
		Payload  any
	}{
		{"fixture-network", "network.configure", map[string]any{"mode": "tailnet"}},
		{"fixture-trust", "peer.trust", map[string]any{"peerId": node.peerID, "trusted": true}},
	} {
		raw, err := json.Marshal(map[string]any{"requestId": command.ID, "name": command.Name, "payload": command.Payload})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.IPC(ctx, string(raw)); err != nil {
			t.Fatal("fresh fixture setup:", err)
		}
	}
	value, err := app.IPC(ctx, "status")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var readiness struct {
		ReceiveRecovery struct{ State, Code string } `json:"receiveRecovery"`
	}
	if err := json.Unmarshal(raw, &readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.ReceiveRecovery.State != "ready" || readiness.ReceiveRecovery.Code != "" {
		t.Fatalf("fresh fixture receive accounting is blocked: state=%s code=%s", readiness.ReceiveRecovery.State, readiness.ReceiveRecovery.Code)
	}
	return dir, app
}

// The fixture transports bytes only. It does not implement any peer HTTP route,
// message/file response, acceptance, persistence, status, or completion result.
type nativePeerHub struct {
	mu        sync.Mutex
	listeners map[netip.AddrPort]*nativePeerListener
}
type nativePeerNode struct {
	hub        *nativePeerHub
	ip, peerIP netip.Addr
	peerID     string
}

var _ core.NetworkBackend = (*nativePeerNode)(nil)

func (n *nativePeerNode) Start() error                { return nil }
func (n *nativePeerNode) Login(context.Context) error { return errors.New("fixture must never log in") }
func (n *nativePeerNode) Logout(context.Context) error {
	return errors.New("fixture must never log out")
}
func (n *nativePeerNode) State(context.Context) (identity.State, error) {
	return identity.State{Backend: "Running", IPs: []netip.Addr{n.ip}, Snapshot: policy.Snapshot{Running: true, Peers: []policy.Peer{{ID: n.peerID, DNSName: n.peerID + ".invalid", Online: true, IPs: []netip.Addr{n.peerIP}}}}}, nil
}
func (n *nativePeerNode) WhoIs(_ context.Context, source netip.AddrPort) (string, error) {
	if source.Addr() != n.peerIP {
		return "", errors.New("unknown fixture identity")
	}
	return n.peerID, nil
}
func (n *nativePeerNode) Listen(network, address string) (net.Listener, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || network != "tcp" || ap.Addr() != n.ip || (ap.Port() != core.PeerPort && ap.Port() != core.DiscoveryPort) {
		return nil, errors.New("unexpected fixture listener scope")
	}
	n.hub.mu.Lock()
	defer n.hub.mu.Unlock()
	if n.hub.listeners[ap] != nil {
		return nil, errors.New("duplicate fixture listener")
	}
	l := &nativePeerListener{address: ap, queue: make(chan net.Conn), done: make(chan struct{})}
	n.hub.listeners[ap] = l
	return l, nil
}
func (n *nativePeerNode) ListenPacket(string, string) (net.PacketConn, error) {
	return nil, errors.New("fixture UDP unsupported")
}
func (n *nativePeerNode) DialIP(ctx context.Context, network string, target netip.AddrPort) (net.Conn, error) {
	if network != "tcp" || target.Addr() != n.peerIP {
		return nil, errors.New("unexpected fixture destination")
	}
	n.hub.mu.Lock()
	l := n.hub.listeners[target]
	n.hub.mu.Unlock()
	if l == nil {
		return nil, errors.New("fixture listener not ready")
	}
	left, right := net.Pipe()
	source := netip.AddrPortFrom(n.ip, 40000)
	client := &nativePeerConn{Conn: left, local: source, remote: target}
	server := &nativePeerConn{Conn: right, local: target, remote: source}
	select {
	case l.queue <- server:
		return client, nil
	case <-l.done:
		_ = left.Close()
		_ = right.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = left.Close()
		_ = right.Close()
		return nil, ctx.Err()
	}
}
func (n *nativePeerNode) Close() error {
	n.hub.mu.Lock()
	defer n.hub.mu.Unlock()
	for address, listener := range n.hub.listeners {
		if address.Addr() == n.ip {
			_ = listener.Close()
		}
	}
	return nil
}

type nativePeerListener struct {
	address netip.AddrPort
	queue   chan net.Conn
	done    chan struct{}
	once    sync.Once
}

func (l *nativePeerListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.queue:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *nativePeerListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *nativePeerListener) Addr() net.Addr { return net.TCPAddrFromAddrPort(l.address) }

type nativePeerConn struct {
	net.Conn
	local, remote netip.AddrPort
}

func (c *nativePeerConn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(c.local) }
func (c *nativePeerConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

// Explicit weaker diagnostic test; never substituted for protected IPC acceptance.
// It proves fixture initialization and genuine peer file protocol without a socket.
func TestNativeInProcessCoreProtocolDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hub := &nativePeerHub{listeners: make(map[netip.AddrPort]*nativePeerListener)}
	aIP, bIP := netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("100.64.0.2")
	aDir, aCore := nativeOpenCore(t, ctx, &nativePeerNode{hub: hub, ip: aIP, peerIP: bIP, peerID: "fixture-b"})
	bDir, bCore := nativeOpenCore(t, ctx, &nativePeerNode{hub: hub, ip: bIP, peerIP: aIP, peerID: "fixture-a"})
	caller := func(ctx context.Context, dir, command string, out any) error {
		app := aCore
		if dir == bDir {
			app = bCore
		} else if dir != aDir {
			return errors.New("unknown fixture profile")
		}
		value, err := app.IPC(ctx, command)
		if err != nil {
			return err
		}
		if out == nil {
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	a, b := newExchangeClient(aDir, true, caller), newExchangeClient(bDir, true, caller)
	nativeAwait(t, ctx, "peer listeners", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.listeners) == 4 })
	load := func(dir string) snapshot {
		t.Helper()
		var state snapshot
		if err := caller(ctx, dir, "status", &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	source := filepath.Join(t.TempDir(), "fixture.txt")
	payload := []byte("fictional protocol fixture")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	review, err := a.PrepareFiles(ctx, "diag-prepare", load(aDir).ResourceCatalogProcessID, []string{"fixture-b"}, []string{source})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := a.CommitFiles(ctx, "diag-commit", review, true)
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Children[0].TransferID
	wait := func(dir, expected string) exchangeTransfer {
		t.Helper()
		var result exchangeTransfer
		nativeAwait(t, ctx, "protocol "+expected, func() bool {
			for _, row := range load(aDir).Transfers {
				if row.ID == id && (row.Status == "failed" || row.Status == "cancelled") {
					t.Fatalf("sender failed: %s", row.Status)
				}
			}
			for _, row := range load(dir).Transfers {
				if row.ID == id {
					result = row
					return row.Status == expected
				}
			}
			return false
		})
		return result
	}
	wait(bDir, "awaiting-acceptance")
	destination := t.TempDir()
	if err := b.AcceptFiles(ctx, "diag-accept", id, destination, true); err != nil {
		t.Fatal(err)
	}
	received := wait(bDir, "completed")
	wait(aDir, "completed")
	if received.CompletedBytes != int64(len(payload)) || len(received.Entries) != 1 {
		t.Fatal("protocol completion mismatch")
	}
	count := 0
	err = filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "fixture.txt" {
			count++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, payload) {
				return errors.New("protocol fixture saved wrong bytes")
			}
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatal("protocol saved-file verification failed", err)
	}
}
