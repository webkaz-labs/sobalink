//go:build lanlink_integration && directlan_integration

package core

import (
	"context"
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
	defer func() {
		saveNearbyBrowserFailure(t, nearbyBrowserFailure{Schema: 1, Phase: failurePhase, ErrorKind: "core_evidence"})
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
	wait := func(condition func() bool, message string) {
		t.Helper()
		until := time.Now().Add(45 * time.Second)
		for !condition() {
			if time.Now().After(until) || ctx.Err() != nil {
				t.Fatal(message)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	var reviewed [2]SetupSnapshot
	runTransferProcessBrowser(t, ctx, map[string]any{"schema": 1, "nearby": true, "devices": devices}, false, func(private string) {
		defer func() {
			// Capture an earlier browser failure, or this Core evidence failure,
			// before runner cancellation can produce a secondary interruption.
			exportNearbyBrowserFailure(t, private)
			saveNearbyBrowserFailure(t, nearbyBrowserFailure{Schema: 1, Phase: failurePhase, ErrorKind: "core_evidence"})
		}()
		for _, stage := range []string{"cancelled", "review", "one-approved", "connected"} {
			failurePhase = "core-" + stage
			wait(func() bool { _, err := os.Stat(filepath.Join(private, "nearby-"+stage+".json")); return err == nil }, "browser setup checkpoint did not arrive")
			switch stage {
			case "cancelled":
				v := view(peers[0])
				if v.State != "cancelled" || !evidence(peers[0]).Joined || v.Discovery.State != "stopped" {
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
				wait(func() bool { return view(peers[1]).RemoteApproved }, "actual peer did not observe first browser approval")
				if !view(peers[0]).LocalApproved || view(peers[1]).LocalApproved {
					t.Fatal("one browser approval became bilateral consent")
				}
				unpaired()
			case "connected":
				for i, p := range peers {
					v, e := view(p), evidence(p)
					if !v.Connected || !v.Paired || v.Trusted || !v.LocalApproved || !v.RemoteApproved || v.SessionID != reviewed[i].SessionID || v.Plan.Fingerprint != reviewed[i].Plan.Fingerprint || v.Discovery.State != "stopped" || !e.Backend || e.Pairs != 1 || e.Trusts != 0 || !e.StrictScope || !e.LocalHello || !e.RemoteReceipt {
						t.Fatal("browser connected result lacks independent pairing, hello, receipt or trust evidence")
					}
					if !catalogProcessDecode[bool](t, p.invoke(t, "fixture.setup-hello", nil)) {
						t.Fatal("connected peer API hello unavailable through actual transport")
					}
				}
			}
			if err := os.WriteFile(filepath.Join(private, "nearby-"+stage+".ok"), []byte("verified\n"), 0600); err != nil {
				t.Fatal("private browser evidence checkpoint unavailable")
			}
		}
	})
	failurePhase = "core-cleanup"
	for _, p := range peers {
		v := view(p)
		p.invoke(t, "setup.cancel", SetupSessionInput{SessionID: v.SessionID})
		wait(func() bool { return evidence(p).Joined }, "promoted setup cleanup did not join")
		if current := view(p); !current.Connected || !evidence(p).Backend || !catalogProcessDecode[bool](t, p.invoke(t, "fixture.setup-hello", nil)) {
			t.Fatal("temporary cleanup closed the approved persistent connection")
		}
		conn, err := net.DialTimeout("tcp", v.Contact.Bootstrap, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			t.Fatal("joined setup retained its bootstrap listener")
		}
	}
	for _, p := range peers {
		p.stop(t)
	}
	if path := os.Getenv("SOBA_SETUP_BROWSER_SUMMARY"); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("sanitized nearby browser summary unavailable")
		}
		_, writeErr := file.WriteString("{\"schema\":1,\"accepted\":true,\"observed\":1,\"passed\":1,\"errors\":0,\"cores\":2,\"browserContexts\":2,\"locales\":[\"en\",\"ja\"],\"widths\":[375,320],\"path\":\"manual-contact-loopback\",\"nativeMulticastVerified\":false,\"bilateralFingerprint\":true,\"persistentConsent\":true,\"approvalResponseLost\":true,\"originalSessionRecovered\":true,\"oneApprovalUnpaired\":true,\"helloConfirmed\":true,\"receivingTrustUnchanged\":true,\"cancelJoined\":true,\"browserCommandExited\":true,\"coreChildrenJoined\":true}\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("sanitized nearby browser summary save failed")
		}
	}
}
