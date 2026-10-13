//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/testfixture"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type transferProcessSnapshot struct {
	ProcessID string `json:"resourceCatalogProcessId"`
	Transfers []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		PeerID         string `json:"peerId"`
		Direction      string `json:"direction"`
		Status         string `json:"status"`
		Retryable      bool   `json:"retryable"`
		CompletedBytes int64  `json:"completedBytes"`
		Entries        []struct {
			ID         string `json:"id"`
			Path       string `json:"path"`
			StoredPath string `json:"storedPath"`
		} `json:"entries"`
	} `json:"transfers"`
}

// Two independent Core child processes, real staged bytes and native peer HTTP.
// The second staged file is temporarily unavailable within synthetic test state;
// no peer response or progress is substituted. Browser acceptance is separate.
func TestTransferGroupTwoProcessSelectedRetry(t *testing.T) {
	runTransferGroupTwoProcess(t, false)
}

func TestTransferGroupTwoProcessBrowser(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_TRANSFER_BROWSER_NATIVE") != "1" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unperformed: explicit process/browser acceptance required")
	}
	runTransferGroupTwoProcess(t, true)
}

func runTransferGroupTwoProcess(t *testing.T, browser bool) {
	if os.Getenv("SOBALINK_RUN_DIRECT_INTEGRATION") != "1" {
		t.Skip("explicit native loopback execution required")
	}
	if !directLANNetstackReady {
		t.Skip("userspace netstack activation remains gated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dirs := [2]string{t.TempDir(), t.TempDir()}
	var peers [2]*routeCoreProcess
	var keys [2]string
	for i := range peers {
		peers[i] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[i], true, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
		r, err := testfixture.ReserveLoopbackTCPUDP(netip.MustParseAddr("127.0.0.1"), 54543, 54544, 54545)
		if err != nil {
			t.Fatal(err)
		}
		listen := r.Endpoint().String()
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		peers[i].invoke(t, "network.configure", map[string]any{"mode": "direct-lan", "hostname": "synthetic-transfer-" + strconv.Itoa(i), "directLAN": DirectLANSelection{Listen: listen, Prefixes: []string{"127.0.0.0/8"}}})
		keys[i] = catalogProcessDecode[map[string]string](t, peers[i].invoke(t, "direct-lan.identity", struct{}{}))["publicKey"]
	}
	invite := catalogProcessDecode[map[string]any](t, peers[0].invoke(t, "direct-lan.invite", map[string]any{"recipientPublicKey": keys[1], "name": "synthetic-transfer-peer", "ttlSeconds": 120}))
	peers[1].invoke(t, "direct-lan.join", map[string]any{"invitation": invite["invitation"]})
	for i, p := range peers {
		p.invoke(t, "peer.trust", map[string]any{"peerId": keys[1-i], "trusted": true})
	}
	for i, p := range peers {
		until := time.Now().Add(30 * time.Second)
		for {
			probe := catalogProcessDecode[map[string]any](t, p.invoke(t, "fixture.probe", map[string]any{"Peer": keys[1-i], "Port": PeerPort}))
			if probe["connected"] == true && probe["networkReady"] == true {
				break
			}
			if time.Now().After(until) {
				t.Fatal("actual transfer peer transport did not become ready")
			}
			select {
			case <-ctx.Done():
				t.Fatal("transfer peer readiness timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	snapshot := func(p *routeCoreProcess) transferProcessSnapshot {
		return catalogProcessDecode[transferProcessSnapshot](t, p.invoke(t, "fixture.snapshot", struct{}{}))
	}
	wait := func(p *routeCoreProcess, id, status string) transferProcessSnapshot {
		deadline := time.Now().Add(25 * time.Second)
		for {
			s := snapshot(p)
			for _, row := range s.Transfers {
				if row.ID == id && row.Status == status {
					return s
				}
				if row.ID == id && row.Status == "failed" && status != "failed" {
					t.Fatal("actual child transfer failed before expected state", status)
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("actual child transfer state did not arrive", status)
			}
			select {
			case <-ctx.Done():
				t.Fatal("actual child transfer wait timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	source := t.TempDir()
	paths := []string{filepath.Join(source, "first.txt"), filepath.Join(source, "second.txt")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("synthetic-transfer-data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := catalogProcessDecode[map[string]string](t, peers[0].invoke(t, "fixture.send-paths", map[string]any{"peerId": keys[1], "paths": paths}))["id"]
	pending := wait(peers[0], id, "awaiting-acceptance")
	stagedDirectories, err := os.ReadDir(filepath.Join(dirs[0], "outgoing"))
	if err != nil || len(stagedDirectories) != 1 || !stagedDirectories[0].IsDir() {
		t.Fatal("exact isolated staging directory unavailable")
	}
	var missing string
	for _, row := range pending.Transfers {
		if row.ID == id {
			for _, entry := range row.Entries {
				if entry.Path == "second.txt" {
					missing = filepath.Join(dirs[0], "outgoing", stagedDirectories[0].Name(), entry.ID)
				}
			}
		}
	}
	if missing == "" {
		t.Fatal("actual staged second file absent")
	}
	backup, err := os.ReadFile(missing)
	if err != nil {
		t.Fatal("actual staged file unreadable")
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal("isolated staged file retirement failed")
	}
	wait(peers[1], id, "awaiting-acceptance")
	accepted := catalogProcessDecode[transfer.Batch](t, peers[1].invoke(t, "transfer.accept", map[string]any{"transferId": id, "destination": t.TempDir()}))
	failed := wait(peers[0], id, "failed")
	expected := transferRetryExpectation{ProcessID: failed.ProcessID, PeerID: keys[1], Direction: "outgoing"}
	first := filepath.Join(accepted.Destination, "first.txt")
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal("first actual saved file absent")
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil || string(firstBytes) != "synthetic-transfer-data" {
		t.Fatal("first actual bytes mismatch")
	}
	old := expected
	old.ProcessID = "0000000000000000000000000000000000000000000000000000000000000000"
	if peers[0].invokeResult(t, "transfer.retry", map[string]any{"transferId": id, "expected": old}).Error == "" {
		t.Fatal("old process retry selection accepted")
	}
	untouchedID, untouchedName := "", ""
	selectedName := ""
	for _, row := range failed.Transfers {
		if row.ID == id {
			selectedName = row.Name
		}
	}
	if browser {
		untouchedID = catalogProcessDecode[map[string]string](t, peers[0].invoke(t, "fixture.send-paths", map[string]any{"peerId": keys[1], "paths": paths}))["id"]
		untouchedPending := wait(peers[0], untouchedID, "awaiting-acceptance")
		entryID := ""
		for _, row := range untouchedPending.Transfers {
			if row.ID == untouchedID {
				untouchedName = row.Name
				for _, entry := range row.Entries {
					if entry.Path == "second.txt" {
						entryID = entry.ID
					}
				}
			}
		}
		directories, err := os.ReadDir(filepath.Join(dirs[0], "outgoing"))
		if err != nil || len(directories) != 2 {
			t.Fatal("untouched isolated staging directory unavailable")
		}
		untouchedMissing := ""
		for _, directory := range directories {
			candidate := filepath.Join(dirs[0], "outgoing", directory.Name())
			if candidate != filepath.Dir(missing) {
				untouchedMissing = filepath.Join(candidate, entryID)
			}
		}
		if entryID == "" || untouchedMissing == "" {
			t.Fatal("untouched actual staged entry unavailable")
		}
		if err := os.Remove(untouchedMissing); err != nil {
			t.Fatal("untouched staged file retirement failed")
		}
		wait(peers[1], untouchedID, "awaiting-acceptance")
		peers[1].invoke(t, "transfer.accept", map[string]any{"transferId": untouchedID, "destination": t.TempDir()})
		wait(peers[0], untouchedID, "failed")
	}
	if err := os.WriteFile(missing, backup, 0600); err != nil {
		t.Fatal("isolated staged file restoration failed")
	}
	if browser {
		runTransferGroupProcessBrowser(t, ctx, peers[0], keys[1], expected.ProcessID, id, selectedName, untouchedID, untouchedName)
		wait(peers[0], untouchedID, "failed")
	} else {
		peers[0].invoke(t, "transfer.retry", map[string]any{"transferId": id, "expected": expected})
	}
	completed := wait(peers[0], id, "completed")
	wait(peers[1], id, "completed")
	firstAgain, err := os.Stat(first)
	if err != nil || !os.SameFile(firstInfo, firstAgain) || firstInfo.ModTime() != firstAgain.ModTime() {
		t.Fatal("retry replaced previously successful file")
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != string(firstBytes) {
		t.Fatal("retry changed successful bytes")
	}
	second, err := os.ReadFile(filepath.Join(accepted.Destination, "second.txt"))
	if err != nil || string(second) != string(backup) {
		t.Fatal("retry did not save missing file actual bytes")
	}
	for _, row := range completed.Transfers {
		if row.ID == id && (row.Retryable || row.CompletedBytes != int64(len(firstBytes)+len(second))) {
			t.Fatal("completed selected row remained retryable or lost bytes")
		}
	}
	if peers[0].invokeResult(t, "transfer.retry", map[string]any{"transferId": id, "expected": expected}).Error == "" {
		t.Fatal("completed selected transfer resent")
	}
	cancelID := catalogProcessDecode[map[string]string](t, peers[0].invoke(t, "fixture.send-paths", map[string]any{"peerId": keys[1], "paths": paths}))["id"]
	wait(peers[0], cancelID, "awaiting-acceptance")
	peers[0].invoke(t, "transfer.cancel", map[string]string{"transferId": cancelID})
	wait(peers[0], cancelID, "cancelled")
	if peers[0].invokeResult(t, "transfer.retry", map[string]any{"transferId": cancelID, "expected": expected}).Error == "" {
		t.Fatal("cancelled selected transfer resent")
	}
	if untouchedID != "" {
		peers[0].invoke(t, "transfer.cancel", map[string]string{"transferId": untouchedID})
		wait(peers[0], untouchedID, "cancelled")
	}
	// Reopen the actual sender child process and retain the old selection. The
	// new agent must reject it, without resurrecting an outgoing spool or offer.
	peers[0].stop(t)
	peers[0] = startRouteCoreProcessWithEnvironment(t, ctx, dirs[0], true, []string{"SOBALINK_ROUTE_PROCESS_CATALOG=1"})
	reopened := snapshot(peers[0])
	if reopened.ProcessID == "" || reopened.ProcessID == expected.ProcessID {
		t.Fatal("actual sender restart reused transfer selection generation")
	}
	if peers[0].invokeResult(t, "transfer.retry", map[string]any{"transferId": id, "expected": expected}).Error == "" {
		t.Fatal("actual old-process selection was resent")
	}
	if len(snapshot(peers[0]).Transfers) != 0 {
		t.Fatal("old-process selection resurrected outgoing transfer")
	}
	for _, p := range peers {
		p.stop(t)
	}
	if browser {
		if path := os.Getenv("SOBA_TRANSFER_BROWSER_SUMMARY"); path != "" {
			// Fixed fields only: private selectors, identities and paths never leave the fixture.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal("sanitized transfer acceptance summary unavailable")
			}
			_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatal("sanitized transfer acceptance summary save failed")
			}
		}
	}
}

func runTransferGroupProcessBrowser(t *testing.T, ctx context.Context, sender *routeCoreProcess, peerID, processID, selectedID, selectedName, untouchedID, untouchedName string) {
	t.Helper()
	viewer := catalogProcessDecode[map[string]any](t, sender.invoke(t, "fixture.web", struct{}{}))
	snapshot := catalogProcessDecode[struct {
		Peers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"peers"`
	}](t, sender.invoke(t, "fixture.snapshot", struct{}{}))
	peerName := ""
	for _, peer := range snapshot.Peers {
		if peer.ID == peerID {
			peerName = peer.Name
		}
	}
	if peerName == "" {
		t.Fatal("actual transfer peer name absent")
	}
	descriptor := map[string]any{"schema": 1, "viewer": viewer, "peerId": peerID, "peerName": peerName, "processId": processID, "selected": map[string]string{"id": selectedID, "name": selectedName, "direction": "outgoing"}, "untouched": map[string]string{"id": untouchedID, "name": untouchedName, "direction": "outgoing"}}
	runTransferProcessBrowser(t, ctx, descriptor, false)
}

// Both browser cases retain the same private login, output and cleanup contract.
func runTransferProcessBrowser(t *testing.T, ctx context.Context, descriptor map[string]any, dispatch bool, whileRunning ...func(string)) string {
	t.Helper()
	private := t.TempDir()
	if descriptor["nearby"] == true {
		defer exportNearbyBrowserFailure(t, private)
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal("private transfer browser descriptor encoding failed")
	}
	path := filepath.Join(private, "session.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("private transfer browser descriptor save failed")
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal("browser source directory unavailable")
	}
	webDir := filepath.Clean(filepath.Join(working, "..", "..", "web"))
	node := os.Getenv("SOBA_TRANSFER_BROWSER_NODE")
	if node == "" {
		node, err = exec.LookPath("node")
		if err != nil {
			t.Fatal("prepared Node runtime unavailable")
		}
	}
	call, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	configuration := "playwright.transfer-native.config.mjs"
	if descriptor["nearby"] == true {
		configuration = "playwright.nearby-native.config.mjs"
	}
	command := exec.CommandContext(call, node, filepath.Join(webDir, "node_modules", "@playwright", "test", "cli.js"), "test", "--config", filepath.Join(webDir, configuration), "--workers", "1", "--retries", "0")
	command.Dir = webDir
	command.Env = append(os.Environ(), "SOBA_TRANSFER_BROWSER_SESSION="+path)
	if descriptor["outbox"] == true {
		command.Env = append(command.Env, "SOBA_TRANSFER_BROWSER_OUTBOX=1")
	} else {
		command.Env = append(command.Env, "SOBA_TRANSFER_BROWSER_OUTBOX=0")
	}
	if dispatch {
		command.Env = append(command.Env, "SOBA_TRANSFER_BROWSER_DISPATCH=1")
	} else {
		command.Env = append(command.Env, "SOBA_TRANSFER_BROWSER_DISPATCH=0")
	}
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 10 * time.Second
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if len(whileRunning) == 0 {
		if err := command.Run(); err != nil {
			t.Fatal("real transfer process/browser acceptance failed; private selectors withheld")
		}
	} else {
		if len(whileRunning) != 1 {
			t.Fatal("only one bounded native browser bridge is supported")
		}
		if err := command.Start(); err != nil {
			t.Fatal("native browser process did not start")
		}
		joined := false
		defer func() {
			if !joined {
				cancel()
				_ = command.Wait()
			}
		}()
		whileRunning[0](private)
		err := command.Wait()
		joined = true
		if err != nil {
			t.Fatal("real native process/browser acceptance failed; private selectors withheld")
		}
	}
	result, err := os.ReadFile(filepath.Join(private, "transfer-browser-result.json"))
	if err != nil {
		t.Fatal("transfer browser receipt unavailable")
	}
	receipt := catalogProcessDecode[struct {
		Schema   int  `json:"schema"`
		Accepted bool `json:"accepted"`
		Observed int  `json:"observed"`
		Passed   int  `json:"passed"`
		Errors   int  `json:"errors"`
	}](t, result)
	if receipt.Schema != 1 || !receipt.Accepted || receipt.Observed != 1 || receipt.Passed != 1 || receipt.Errors != 0 {
		t.Fatal("transfer browser receipt did not establish acceptance")
	}
	return private
}
