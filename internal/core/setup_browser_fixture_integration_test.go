//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test executable only: product scope enumeration still rejects loopback.
// The nearby adapter also rejects it, exercising the explicit copied-contact
// fallback rather than implying native multicast or physical-LAN acceptance.
func installBrowserSetupScope(c *Core) {
	scope := SetupScope{InterfaceIndex: 1, Interface: "synthetic-loopback", LocalAddress: "127.0.0.1", Prefix: "127.0.0.0/8", linkBinding: "isolated-test"}
	scope.ScopeID = setupScopeID(scope)
	c.setupScopeRead = func() ([]SetupScope, error) { return []SetupScope{scope}, nil }
}

type setupBrowserEvidence struct {
	Backend       bool `json:"backend"`
	Pairs         int  `json:"pairs"`
	Trusts        int  `json:"trusts"`
	StrictScope   bool `json:"strictScope"`
	Joined        bool `json:"joined"`
	LocalHello    bool `json:"localHello"`
	RemoteReceipt bool `json:"remoteReceipt"`
}

// Counts and proofs come from genuine protected Core/backend state. No browser
// claim is accepted as evidence of pairing, trust, hello, or physical closure.
func browserSetupEvidence(c *Core) setupBrowserEvidence {
	v := setupBrowserEvidence{Backend: c.nodeCopy() != nil, Trusts: len(c.profileCopy().Peers)}
	if store := c.lanStoreCopy(); store != nil {
		saved := store.copy()
		v.Pairs, v.StrictScope = len(saved.Trust.Peers), saved.DestinationPolicy.Strict()
	}
	if s := c.setupCopy(); s != nil {
		s.mu.Lock()
		v.LocalHello, v.RemoteReceipt = s.localConnected, s.remoteConnected
		s.mu.Unlock()
		select {
		case <-s.done:
			v.Joined = true
		default:
		}
	}
	return v
}

func browserSetupHello(ctx context.Context, c *Core) bool {
	s := c.setupCopy()
	if s == nil {
		return false
	}
	s.mu.Lock()
	peer := ""
	if s.peer != nil {
		peer = s.peer.Key
	}
	s.mu.Unlock()
	if peer == "" {
		return false
	}
	call, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var hello struct {
		Protocol int    `json:"protocol"`
		Product  string `json:"product"`
	}
	return c.peerJSON(call, peer, "GET", "/v1/hello", nil, &hello) == nil && hello.Protocol == 1 && hello.Product == "sobalink"
}

// This failure-only receipt is intentionally independent of the success proof.
// No unvalidated bytes, runtime error, DOM, selector, or contact is exported.
type nearbyBrowserFailure struct {
	Schema    int    `json:"schema"`
	Phase     string `json:"phase"`
	ErrorKind string `json:"errorKind"`
}

func validNearbyBrowserFailure(v nearbyBrowserFailure) bool {
	if v.Schema != 1 {
		return false
	}
	switch v.Phase {
	case "launch", "descriptor", "authenticate-a", "authenticate-b", "open-a", "start-a", "cancel-a", "checkpoint-cancelled", "restart-a", "open-b", "start-b", "manual-a", "manual-b", "plan-a", "plan-b", "fingerprint-a", "fingerprint-b", "checkpoint-review", "approve-a", "recover-a", "checkpoint-one-approved", "approve-b", "connected-a", "connected-b", "checkpoint-connected", "cleanup", "core-start", "core-cancelled", "core-review", "core-one-approved", "core-connected", "core-cleanup", "manual-edit-a", "manual-edit-b", "manual-submit-a", "manual-submit-b", "fingerprint-read-a", "fingerprint-read-b", "fingerprint-initial-a", "fingerprint-initial-b", "fingerprint-mismatch-edit-a", "fingerprint-mismatch-edit-b", "fingerprint-mismatch-blocked-a", "fingerprint-mismatch-blocked-b", "fingerprint-match-edit-a", "fingerprint-match-edit-b", "fingerprint-verify-a", "fingerprint-verify-b", "fingerprint-consent-a", "fingerprint-consent-b", "fingerprint-unverify-a", "fingerprint-unverify-b", "fingerprint-ready-a", "fingerprint-ready-b", "approve-send-a", "approve-response-a", "approve-uncertain-a", "recover-request-a", "recover-receipt-a", "approve-send-b", "approve-receipt-b":
	default:
		return false
	}
	switch v.ErrorKind {
	case "timeout", "assertion", "selector_ambiguous", "browser_launch", "browser_closed", "runtime", "command_boundary", "checkpoint", "cleanup", "interrupted", "unexpected", "core_evidence", "evidence_unavailable", "locator_missing", "element_disabled", "element_hidden", "element_not_editable", "element_detached", "pointer_intercepted", "element_unstable":
		return true
	default:
		return false
	}
}

func saveNearbyBrowserFailure(t *testing.T, v nearbyBrowserFailure) {
	t.Helper()
	path := os.Getenv("SOBA_SETUP_BROWSER_FAILURE")
	if path == "" || !t.Failed() || !validNearbyBrowserFailure(v) {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return
	} // Preserve the first failure, including before cleanup.
	if err != nil {
		t.Log("fixed browser failure evidence unavailable")
		return
	}
	raw, _ := json.Marshal(v)
	_, writeErr := f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Log("fixed browser failure evidence save failed")
	}
}

func exportNearbyBrowserFailure(t *testing.T, private string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	path := filepath.Join(private, "nearby-browser-failure.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024 {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value nearbyBrowserFailure
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || !validNearbyBrowserFailure(value) {
		return
	}
	saveNearbyBrowserFailure(t, value)
}

func TestNearbyBrowserFailureSchema(t *testing.T) {
	valid := nearbyBrowserFailure{Schema: 1, Phase: "fingerprint-b", ErrorKind: "assertion"}
	if !validNearbyBrowserFailure(valid) {
		t.Fatal("fixed failure rejected")
	}
	for _, kind := range []string{"locator_missing", "element_disabled", "element_hidden", "element_not_editable", "element_detached", "pointer_intercepted", "element_unstable"} {
		if !validNearbyBrowserFailure(nearbyBrowserFailure{Schema: 1, Phase: "fingerprint-match-edit-a", ErrorKind: kind}) {
			t.Fatal("fixed actionability failure rejected")
		}
	}
	for _, invalid := range []nearbyBrowserFailure{
		{Schema: 0, Phase: valid.Phase, ErrorKind: valid.ErrorKind},
		{Schema: 1, Phase: "synthetic-private-endpoint", ErrorKind: valid.ErrorKind},
		{Schema: 1, Phase: valid.Phase, ErrorKind: "synthetic-private-body"},
	} {
		if validNearbyBrowserFailure(invalid) {
			t.Fatal("unbounded failure classification accepted")
		}
	}
}
