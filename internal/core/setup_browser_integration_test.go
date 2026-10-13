//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two genuine Core processes and two authenticated browser contexts. The only
// substituted input is local scope enumeration in the test executable. Native
// multicast and real-device acceptance remain separate, unperformed gates.
func TestNearbySetupTwoProcessBrowser(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_SETUP_BROWSER_NATIVE") != "1" {
		t.Skip("unperformed: explicit nearby process/browser acceptance required")
	}
	failurePhase := "core-start"
	var coreEvidence *nearbyBrowserCoreEvidence
	var cleanupEvidence *nearbyBrowserCleanupEvidence
	defer func() {
		saveNearbyBrowserFailure(t, nearbyBrowserFailure{Schema: 1, Phase: failurePhase, ErrorKind: "core_evidence", Core: coreEvidence, Cleanup: cleanupEvidence})
	}()
	f := newNativeLANCoreFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	peers := []*routeCoreProcess{}
	devices := []map[string]any{}
	ports := map[uint16]bool{}
	port := func() uint16 {
		for {
			p := f.relayAddress().Port()
			if !ports[p] {
				ports[p] = true
				return p
			}
		}
	}
	for i, alias := range []string{"Synthetic peer A", "Synthetic peer B"} {
		// Only these isolated loopback children need no proxy. Preserve the
		// production environment guard and the parent's process environment.
		environment := []string{"SOBALINK_ROUTE_PROCESS_SETUP=1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "TS_PROXY=", "http_proxy=", "https_proxy=", "all_proxy=", "ts_proxy="}
		p := startRouteCoreProcessWithEnvironment(t, ctx, t.TempDir(), true, environment)
		peers = append(peers, p)
		viewer := catalogProcessDecode[map[string]any](t, p.invoke(t, "fixture.web", struct{}{}))
		devices = append(devices, map[string]any{"viewer": viewer, "alias": alias, "bootstrapPort": port(), "relayPort": port(), "locale": []string{"en", "ja"}[i]})
	}
	view := func(p *routeCoreProcess) SetupSnapshot {
		return catalogProcessDecode[SetupSnapshot](t, p.invoke(t, "setup.status", SetupSessionInput{}))
	}
	evidence := func(p *routeCoreProcess) setupBrowserEvidence {
		return catalogProcessDecode[setupBrowserEvidence](t, p.invoke(t, "fixture.setup-evidence", nil))
	}
	unpaired := func() {
		for _, p := range peers {
			e := evidence(p)
			if e.Backend || e.Pairs != 0 || e.Trusts != 0 || e.LocalHello || e.RemoteReceipt {
				t.Fatal("browser review or one-sided consent activated pairing or receiving trust")
			}
		}
	}
	waitUntil := func(until time.Time, condition func() bool, message string) {
		t.Helper()
		for !condition() {
			if time.Now().After(until) || ctx.Err() != nil {
				t.Fatal(message)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	wait := func(condition func() bool, message string) {
		t.Helper()
		waitUntil(time.Now().Add(45*time.Second), condition, message)
	}
	var reviewed [2]SetupSnapshot
	collect := func() {
		for i, p := range peers {
			previousHello := coreEvidence.Peers[i].FreshHello
			coreEvidence.Peers[i] = observeNearbyBrowserPeer(view(p), reviewed[i], evidence(p))
			coreEvidence.Peers[i].FreshHello = previousHello
		}
	}
	checkpoint := func(private, stage string) bool {
		path := filepath.Join(private, "nearby-"+stage+".json")
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return false
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128 {
			t.Fatal("private browser checkpoint invalid")
		}
		raw, err := os.ReadFile(path)
		var marker struct {
			Schema int    `json:"schema"`
			Stage  string `json:"stage"`
		}
		if err != nil || strictLANJSON(raw, &marker) != nil || marker.Schema != 1 || marker.Stage != stage {
			t.Fatal("private browser checkpoint did not bind exact stage")
		}
		return true
	}
	runTransferProcessBrowser(t, ctx, map[string]any{"schema": 1, "nearby": true, "devices": devices}, false, func(private string) {
		defer func() {
			// Capture an earlier browser failure, or this Core evidence failure,
			// before runner cancellation can produce a secondary interruption.
			exportNearbyBrowserFailure(t, private, coreEvidence)
			saveNearbyBrowserFailure(t, nearbyBrowserFailure{Schema: 1, Phase: failurePhase, ErrorKind: "core_evidence", Core: coreEvidence})
		}()
		for _, stage := range []string{"cancelled", "review", "one-approved", "connected"} {
			failurePhase = "core-" + stage
			until := time.Now().Add(45 * time.Second)
			if stage == "connected" {
				coreEvidence = &nearbyBrowserCoreEvidence{Stage: "waiting_checkpoint"}
				for i := range coreEvidence.Peers {
					coreEvidence.Peers[i] = nearbyBrowserPeerEvidence{State: "unknown", Reason: "other", FreshHello: "not_run"}
				}
				lastSample := time.Time{}
				// Sample actual state while awaiting the browser marker, so a missing
				// UI checkpoint cannot be confused with a failed backend connection.
				waitUntil(until, func() bool {
					coreEvidence.CheckpointArrived = checkpoint(private, stage)
					if coreEvidence.CheckpointArrived || time.Since(lastSample) >= 250*time.Millisecond {
						collect()
						lastSample = time.Now()
					}
					return coreEvidence.CheckpointArrived
				}, "browser connected checkpoint did not arrive")
			} else {
				waitUntil(until, func() bool { return checkpoint(private, stage) }, "browser setup checkpoint did not arrive")
			}
			switch stage {
			case "cancelled":
				var v SetupSnapshot
				waitUntil(until, func() bool { v = view(peers[0]); return v.State == "cancelled" && evidence(peers[0]).Joined }, "cancelled setup workers did not join")
				if v.Discovery.State != "stopped" {
					t.Fatal("browser navigation completed before actual setup workers joined")
				}
				listener, err := net.Listen("tcp4", v.Contact.Bootstrap)
				if err != nil {
					t.Fatal("cancelled setup retained its temporary listener")
				}
				_ = listener.Close()
				unpaired()
			case "review":
				for i, p := range peers {
					reviewed[i] = view(p)
					if reviewed[i].Plan == nil || reviewed[i].LocalApproved || reviewed[i].RemoteApproved || reviewed[i].Discovery.State != "unavailable" || reviewed[i].State != "awaiting_approval" {
						t.Fatal("browser review lacked exact unapproved manual-contact plan")
					}
				}
				if reviewed[0].Plan.Fingerprint != reviewed[1].Plan.Fingerprint || len(reviewed[0].Plan.Fingerprint) != 64 {
					t.Fatal("actual bilateral plan fingerprints differ")
				}
				unpaired()
			case "one-approved":
				waitUntil(until, func() bool { return view(peers[1]).RemoteApproved }, "actual peer did not observe first browser approval")
				if !view(peers[0]).LocalApproved || view(peers[1]).LocalApproved {
					t.Fatal("one browser approval became bilateral consent")
				}
				unpaired()
			case "connected":
				// checkConnected publishes Connected before run closes discovery.
				// Wait only for that documented owner transition, within the same
				// existing stage deadline, before requiring every original invariant.
				coreEvidence.Stage = "waiting_discovery"
				waitUntil(until, func() bool {
					collect()
					return coreEvidence.Peers[0].Checks.DiscoveryStopped && coreEvidence.Peers[1].Checks.DiscoveryStopped
				}, "connected discovery owners did not stop")
				for i, p := range peers {
					coreEvidence.Stage = "invariants"
					if !coreEvidence.Peers[i].Checks.ready() {
						t.Fatal("browser connected result lacks independent pairing, hello, receipt or trust evidence")
					}
					coreEvidence.Stage = []string{"hello_a", "hello_b"}[i]
					ok := catalogProcessDecode[bool](t, p.invoke(t, "fixture.setup-hello", nil))
					coreEvidence.Peers[i].FreshHello = "failed"
					if !ok {
						t.Fatal("connected peer API hello unavailable through actual transport")
					}
					coreEvidence.Peers[i].FreshHello = "passed"
				}
				coreEvidence.Stage = "acknowledged"
			}
			if err := os.WriteFile(filepath.Join(private, "nearby-"+stage+".ok"), []byte("verified\n"), 0600); err != nil {
				t.Fatal("private browser evidence checkpoint unavailable")
			}
		}
	})
	// Retain the final connected checkpoint vector; subsequent cleanup has
	// its own outer phase and must not relabel the older snapshot.
	failurePhase = "core-cleanup"
	// The runner returned only after command.Wait and the exact one-case,
	// zero-error browser receipt passed. This is evidence, not wrapper success.
	cleanupEvidence = newNearbyBrowserCleanupEvidence()
	defer func() { cleanupEvidence.ContextEnded = ctx.Err() != nil }()
	for i, p := range peers {
		cleanupEvidence.Peer = []string{"a", "b"}[i]
		observed := &cleanupEvidence.Peers[i]
		cleanupEvidence.Stage = "cancel_snapshot"
		v := view(p)
		cleanupEvidence.Stage = "cancel_request"
		reply := p.invoke(t, "setup.cancel", SetupSessionInput{SessionID: v.SessionID})
		observed.CancelReturned = true
		cleanupEvidence.Stage = "cancel_reply"
		var cancelled SetupSnapshot
		observed.CancelDecoded = json.Unmarshal(reply, &cancelled) == nil
		if observed.CancelDecoded {
			observed.CancelState, observed.CancelReason = nearbyBrowserState(cancelled.State), nearbyBrowserReason(cancelled.ReasonCode)
		}
		cleanupEvidence.Stage = "join"
		wait(func() bool { observed.Joined = evidence(p).Joined; return observed.Joined }, "promoted setup cleanup did not join")
		cleanupEvidence.Stage = "post_cancel_snapshot"
		current := view(p)
		observed.PostObserved, observed.State, observed.Reason, observed.Connected = true, nearbyBrowserState(current.State), nearbyBrowserReason(current.ReasonCode), current.Connected
		// Preserve the original assertion order and short-circuit behavior.
		if !current.Connected {
			t.Fatal("temporary cleanup closed the approved persistent connection")
		}
		cleanupEvidence.Stage = "post_cancel_backend"
		observed.Backend = evidence(p).Backend
		if !observed.Backend {
			t.Fatal("temporary cleanup closed the approved persistent connection")
		}
		cleanupEvidence.Stage = "post_cancel_hello"
		ok := catalogProcessDecode[bool](t, p.invoke(t, "fixture.setup-hello", nil))
		observed.FreshHello = "failed"
		if !ok {
			t.Fatal("temporary cleanup closed the approved persistent connection")
		}
		observed.FreshHello = "passed"
		cleanupEvidence.Stage = "listener_release"
		conn, err := net.DialTimeout("tcp", v.Contact.Bootstrap, 100*time.Millisecond)
		observed.Listener = "dial_rejected"
		if err == nil {
			observed.Listener = "still_open"
			_ = conn.Close()
			t.Fatal("joined setup retained its bootstrap listener")
		}
	}
	for i, p := range peers {
		cleanupEvidence.Peer, cleanupEvidence.Stage = []string{"a", "b"}[i], "child_exit"
		p.stopObserved(t, func(stage string) { cleanupEvidence.Peers[i].ChildExit = stage })
	}
	cleanupEvidence.Peer = "none"
	if path := os.Getenv("SOBA_SETUP_BROWSER_SUMMARY"); path != "" {
		cleanupEvidence.Stage = "summary_open"
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("sanitized nearby browser summary unavailable")
		}
		cleanupEvidence.SummaryOpened = true
		cleanupEvidence.Stage = "summary_write"
		_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"cores\":2,\"browserContexts\":2,\"locales\":[\"en\",\"ja\"],\"widths\":[375,320],\"path\":\"manual-contact-loopback\",\"nativeMulticastVerified\":false,\"bilateralFingerprint\":true,\"persistentConsent\":true,\"approvalResponseLost\":true,\"originalSessionRecovered\":true,\"oneApprovalUnpaired\":true,\"helloConfirmed\":true,\"receivingTrustUnchanged\":true,\"cancelJoined\":true,\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
		cleanupEvidence.SummaryWritten = writeErr == nil
		cleanupEvidence.Stage = "summary_close"
		closeErr := file.Close()
		cleanupEvidence.SummaryClosed = closeErr == nil
		if writeErr != nil || closeErr != nil {
			t.Fatal("sanitized nearby browser summary save failed")
		}
	}
	cleanupEvidence.Stage = "complete"
}
