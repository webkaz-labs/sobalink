//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/lanlink"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"tailscale.com/feature/buildfeatures"
)

// This opt-in native CI test uses stock Tailcat and an embedded TLS-pinned
// loopback relay. It proves Core peer APIs and a TCP share over that path; it
// does not claim real-device enrollment, direct UDP or network migration.
func TestLANCorePeerApplicationsIntegration(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_LAN_INTEGRATION") != "1" {
		t.Skip("requires an explicitly enabled isolated native CI environment")
	}
	if buildfeatures.HasUDPTransport {
		t.Fatal("isolated Core integration requires ts_omit_udptransport")
	}
	if lanlink.ValidateBuild() != nil {
		t.Fatal("isolated Core integration requires all LAN restriction build tags")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var cores []*Core
	var resources []io.Closer
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := len(resources) - 1; i >= 0; i-- {
					_ = resources[i].Close()
				}
				for i := len(cores) - 1; i >= 0; i-- {
					_ = cores[i].Close()
				}
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("native Core teardown did not complete")
			}
		})
	}
	t.Cleanup(cleanup)
	open := func() *Core {
		dir := t.TempDir()
		t.Cleanup(cleanup) // Close engines before this directory's cleanup on failure.
		c, err := Open(ctx, Options{Directory: dir, Version: "native-test", SkipNetworkStart: true})
		if err != nil {
			t.Fatal("could not open isolated Core state")
		}
		cores = append(cores, c)
		return c
	}
	invoke := func(c *Core, name string, payload any) (any, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		call, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		return c.Command(call, webui.Command{RequestID: randomID(), Name: name, Payload: encoded})
	}
	must := func(c *Core, name string, payload any) any {
		value, err := invoke(c, name, payload)
		if err != nil {
			t.Fatalf("native Core command %s failed", name)
		}
		return value
	}
	host, guest := open(), open()
	hostKey := must(host, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	guestKey := must(guest, "lan.identity", map[string]any{}).(map[string]string)["publicKey"]
	var relayAddress netip.AddrPort
	for attempt := 0; attempt < 8; attempt++ {
		probe, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal("could not select an isolated relay port")
		}
		address := probe.Addr().(*net.TCPAddr).AddrPort()
		_ = probe.Close()
		if address.Port() >= 1024 && (address.Port() < DiscoveryPort || address.Port() > lanlink.PairingPort) {
			relayAddress = address
			break
		}
	}
	if !relayAddress.IsValid() {
		t.Fatal("could not select an unreserved relay port")
	}
	must(host, "network.configure", map[string]any{"mode": "lan", "hostname": "native-host", "lan": LANSelection{Kind: "host", Address: relayAddress.String()}})
	selection := *host.lanStoreCopy().copy().Selection
	selection.Kind = "relay"
	must(guest, "network.configure", map[string]any{"mode": "lan", "hostname": "native-guest", "lan": selection})
	issued := must(host, "lan.invite", map[string]any{"recipientPublicKey": guestKey, "name": "native-guest", "ttlSeconds": 120}).(map[string]any)
	must(guest, "lan.join", map[string]any{"invitation": issued["invitation"]})
	for _, pair := range []struct {
		c    *Core
		peer string
	}{{host, guestKey}, {guest, hostKey}} {
		state, err := pair.c.current(ctx)
		if err != nil || len(state.Snapshot.Peers) != 1 || state.Snapshot.Peers[0].ID != pair.peer || len(state.Snapshot.Peers[0].IPs) < 2 {
			t.Fatal("real pairing did not publish the canonical server identity and verified role address")
		}
		if _, trusted := pair.c.trust(pair.peer); trusted {
			t.Fatal("transport pairing implicitly granted application trust")
		}
	}
	if _, err := invoke(guest, "message.send", map[string]any{"peerId": hostKey, "text": "unapproved"}); err == nil {
		t.Fatal("unapproved text was accepted")
	}
	// Normal Core maintenance starts the peer listeners. Bounded read-only
	// hellos exercise that startup path and both direction-specific roles.
	for _, pair := range []struct {
		from *Core
		peer string
	}{{guest, hostKey}, {host, guestKey}} {
		window, stopWindow := context.WithTimeout(ctx, 30*time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		for {
			call, stop := context.WithTimeout(window, 5*time.Second)
			err := pair.from.peerJSON(call, pair.peer, "GET", "/v1/hello", nil, nil)
			stop()
			if err == nil {
				break
			}
			select {
			case <-window.Done():
				ticker.Stop()
				stopWindow()
				t.Fatal("normal Core startup did not make the real peer API reachable")
			case <-ticker.C:
			}
		}
		ticker.Stop()
		stopWindow()
	}
	call, stop := context.WithTimeout(ctx, 10*time.Second)
	err := guest.peerJSON(call, hostKey, "POST", "/v1/messages", map[string]string{"id": "before-trust", "text": "unapproved"}, nil)
	stop()
	if err == nil {
		t.Fatal("remote peer API accepted text before explicit application trust")
	}
	must(host, "peer.trust", map[string]any{"peerId": guestKey, "trusted": true})
	must(guest, "peer.trust", map[string]any{"peerId": hostKey, "trusted": true})
	message := must(guest, "message.send", map[string]any{"peerId": hostKey, "text": "Native Core / こんにちは"}).(Message)
	if message.Status != "sent" {
		t.Fatal("real peer did not acknowledge text receipt")
	}
	host.mu.RLock()
	if len(host.messages) != 1 || host.messages[0].ID != message.ID || host.messages[0].PeerID != guestKey || host.messages[0].Text != message.Text {
		host.mu.RUnlock()
		t.Fatal("real receiver did not persist text under canonical identity")
	}
	host.mu.RUnlock()
	peerCall := func(method, path string, input, output any) error {
		call, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		if data, ok := input.([]byte); ok {
			return guest.peerRequest(call, hostKey, method, path, bytes.NewReader(data), "application/octet-stream", output)
		}
		return guest.peerJSON(call, hostKey, method, path, input, output)
	}
	payload := []byte("A complete file through the real Core peer API.\n")
	manifest := fileManifest("native-batch", string(payload))
	var offered wireBatch
	if peerCall("POST", "/v1/offers", manifest, &offered) != nil || offered.State != transfer.Pending {
		t.Fatal("real file offer did not remain pending for explicit acceptance")
	}
	var ack transfer.FileAck
	fileRoute := "/v1/batches/" + manifest.ID + "/files/file"
	if peerCall("PUT", fileRoute, payload, &ack) == nil {
		t.Fatal("real receiver saved a file before batch acceptance")
	}
	destination := t.TempDir()
	t.Cleanup(cleanup)
	accepted := must(host, "transfer.accept", map[string]any{"transferId": manifest.ID, "destination": destination}).(transfer.Batch)
	existingPath := filepath.Join(accepted.Destination, "folder", "note.txt")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0700); err != nil {
		t.Fatal("could not prepare no-overwrite fixture")
	}
	existingData := []byte("existing content must remain intact")
	if err := os.WriteFile(existingPath, existingData, 0600); err != nil {
		t.Fatal("could not prepare no-overwrite fixture")
	}
	if peerCall("PUT", fileRoute, payload, &ack) == nil {
		t.Fatal("real transfer accepted a conflicting destination")
	}
	original, err := os.ReadFile(existingPath)
	if err != nil || !bytes.Equal(original, existingData) {
		t.Fatal("real transfer overwrote an existing destination file")
	}
	// Clear only the fixture created above, then explicitly retry this same
	// failed file from byte zero through the ordinary Core command.
	if err := os.Remove(existingPath); err != nil {
		t.Fatal("could not clear the isolated no-overwrite fixture")
	}
	must(host, "transfer.retry", map[string]any{"transferId": manifest.ID})
	if peerCall("PUT", fileRoute, payload, &ack) != nil {
		t.Fatal("accepted real file was not saved")
	}
	digest := sha256.Sum256(payload)
	if ack.BatchID != manifest.ID || ack.FileID != "file" || ack.Size != int64(len(payload)) || ack.SHA256 != hex.EncodeToString(digest[:]) || ack.StoredName != "folder/note.txt" {
		t.Fatal("receiver save acknowledgment or collision handling was incorrect")
	}
	savedPath := filepath.Join(accepted.Destination, ack.StoredName)
	saved, err := os.ReadFile(savedPath)
	if err != nil || !bytes.Equal(saved, payload) || sha256.Sum256(saved) != digest {
		t.Fatal("saved file did not match its acknowledged payload hash")
	}
	info, err := os.Stat(savedPath)
	if err != nil {
		t.Fatal("saved file metadata unavailable")
	}
	var duplicate transfer.FileAck
	if peerCall("PUT", fileRoute, payload, &duplicate) != nil || duplicate != ack {
		t.Fatal("repeated upload did not preserve the original save acknowledgment")
	}
	infoAgain, err := os.Stat(savedPath)
	if err != nil || !os.SameFile(info, infoAgain) {
		t.Fatal("idempotent upload rewrote the saved file")
	}
	var completed wireBatch
	if peerCall("GET", "/v1/batches/"+manifest.ID, nil, &completed) != nil || completed.State != transfer.Completed || completed.CompletedBytes != int64(len(payload)) {
		t.Fatal("real receiver did not confirm batch completion")
	}
	// A real loopback echo target exercises Core's compact TCP grant callback.
	var target net.Listener
	var targetPort uint16
	for attempt := 0; attempt < 8; attempt++ {
		target, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal("could not create isolated application target")
		}
		targetPort = target.Addr().(*net.TCPAddr).AddrPort().Port()
		state, err := host.current(ctx)
		reserved := err != nil || (targetPort >= DiscoveryPort && targetPort <= lanlink.PairingPort)
		for _, port := range state.ReservedPorts {
			reserved = reserved || port == targetPort
		}
		if !reserved {
			break
		}
		_ = target.Close()
		target = nil
	}
	if target == nil {
		t.Fatal("could not select an unreserved application target port")
	}
	resources = append(resources, target)
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		connection, err := target.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
		_, _ = io.Copy(connection, connection)
	}()
	must(host, "service.share", map[string]any{"name": "native-echo", "network": "tcp", "remotePort": targetPort, "peerIds": []string{guestKey}, "ttlSeconds": 60})
	call, stop = context.WithTimeout(ctx, 15*time.Second)
	stream, err := guest.dial(call, hostKey, "tcp", int(targetPort))
	stop()
	if err != nil {
		t.Fatal("real Core service grant did not open a stream")
	}
	resources = append(resources, stream)
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write([]byte("echo")); err != nil {
		t.Fatal("real shared application write failed")
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(stream, response); err != nil || string(response) != "echo" {
		t.Fatal("real shared application response failed")
	}
	must(host, "lan.revoke", map[string]any{"peerId": guestKey})
	if _, trusted := host.trust(guestKey); trusted {
		t.Fatal("revocation retained application trust")
	}
	host.mu.RLock()
	activeShares := len(host.active)
	host.mu.RUnlock()
	if activeShares != 0 {
		t.Fatal("revocation retained active Core service grants")
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := stream.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal("revocation left an authenticated application stream usable")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("revocation did not close the active application stream promptly")
	}
	select {
	case <-targetDone:
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not release the application target connection")
	}
	call, stop = context.WithTimeout(ctx, 5*time.Second)
	err = guest.peerJSON(call, hostKey, "POST", "/v1/messages", map[string]string{"id": "after-revoke", "text": "must fail"}, nil)
	stop()
	if err == nil {
		t.Fatal("revoked peer sent another real message")
	}
	state, err := host.current(ctx)
	if err != nil || len(state.Snapshot.Peers) != 0 || len(host.lanStoreCopy().copy().Trust.Peers) != 0 {
		t.Fatal("revocation did not remove the durable canonical pairing")
	}
	cleanup()
}
