//go:build lanlink_integration

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Failure-only, bounded observations. Identities, plans, keys, endpoint strings
// and error messages are compared in memory, never included in this record.
type nearbyBrowserCoreEvidence struct {
	CheckpointArrived bool                         `json:"checkpointArrived"`
	Stage             string                       `json:"stage"`
	Peers             [2]nearbyBrowserPeerEvidence `json:"peers"`
}
type nearbyBrowserPeerEvidence struct {
	Observed   bool                `json:"observed"`
	State      string              `json:"state"`
	Reason     string              `json:"reason"`
	Checks     nearbyBrowserChecks `json:"checks"`
	FreshHello string              `json:"freshHello"`
}
type nearbyBrowserChecks struct {
	Connected        bool `json:"connected"`
	Paired           bool `json:"paired"`
	Untrusted        bool `json:"untrusted"`
	LocalApproved    bool `json:"localApproved"`
	RemoteApproved   bool `json:"remoteApproved"`
	SessionMatches   bool `json:"sessionMatches"`
	PlanMatches      bool `json:"planMatches"`
	DiscoveryStopped bool `json:"discoveryStopped"`
	Backend          bool `json:"backend"`
	ExactlyOnePair   bool `json:"exactlyOnePair"`
	TrustUnchanged   bool `json:"trustUnchanged"`
	StrictScope      bool `json:"strictScope"`
	LocalHello       bool `json:"localHello"`
	RemoteReceipt    bool `json:"remoteReceipt"`
}

func (v nearbyBrowserChecks) ready() bool {
	return v.Connected && v.Paired && v.Untrusted && v.LocalApproved && v.RemoteApproved && v.SessionMatches && v.PlanMatches && v.DiscoveryStopped && v.Backend && v.ExactlyOnePair && v.TrustUnchanged && v.StrictScope && v.LocalHello && v.RemoteReceipt
}
func nearbyBrowserState(value string) string {
	switch value {
	case "idle", "discovering", "selecting", "authenticating", "awaiting_approval", "preparing", "connecting", "connected", "recovery_required", "needs_restart", "cancelling", "cancelled", "expired", "stop_required", "failed":
		return value
	default:
		return "unknown"
	}
}
func nearbyBrowserReason(value string) string {
	switch value {
	case "":
		return "none"
	case "setup_connection_failed", "setup_commit_uncertain", "setup_cleanup_unconfirmed", "setup_session_expired", "setup_authority_changed", "setup_state_changed", "lan_environment_proxy", "lan_environment_override", "lan_start_failed":
		return value
	default:
		return "other"
	}
}
func observeNearbyBrowserPeer(v, reviewed SetupSnapshot, e setupBrowserEvidence) nearbyBrowserPeerEvidence {
	return nearbyBrowserPeerEvidence{Observed: true, State: nearbyBrowserState(v.State), Reason: nearbyBrowserReason(v.ReasonCode), FreshHello: "not_run", Checks: nearbyBrowserChecks{
		Connected: v.Connected, Paired: v.Paired, Untrusted: !v.Trusted, LocalApproved: v.LocalApproved, RemoteApproved: v.RemoteApproved,
		SessionMatches:   v.SessionID != "" && v.SessionID == reviewed.SessionID,
		PlanMatches:      v.Plan != nil && reviewed.Plan != nil && v.Plan.Fingerprint == reviewed.Plan.Fingerprint,
		DiscoveryStopped: v.Discovery.State == "stopped", Backend: e.Backend, ExactlyOnePair: e.Pairs == 1, TrustUnchanged: e.Trusts == 0, StrictScope: e.StrictScope, LocalHello: e.LocalHello, RemoteReceipt: e.RemoteReceipt,
	}}
}
func validNearbyBrowserCoreEvidence(v nearbyBrowserCoreEvidence) bool {
	switch v.Stage {
	case "waiting_checkpoint", "waiting_discovery", "invariants", "hello_a", "hello_b", "acknowledged":
	default:
		return false
	}
	for _, p := range v.Peers {
		if nearbyBrowserState(p.State) != p.State || p.Reason != "none" && p.Reason != "other" && nearbyBrowserReason(p.Reason) != p.Reason {
			return false
		}
		if p.FreshHello != "not_run" && p.FreshHello != "passed" && p.FreshHello != "failed" {
			return false
		}
	}
	return true
}

// This is a separate post-browser observation. It never overwrites the earlier
// connected checkpoint or turns a partial cleanup into an accepted test.
type nearbyBrowserCleanupEvidence struct {
	Stage                  string                      `json:"stage"`
	Peer                   string                      `json:"peer"`
	BrowserCommandExited   bool                        `json:"browserCommandExited"`
	BrowserReceiptVerified bool                        `json:"browserReceiptVerified"`
	ContextEnded           bool                        `json:"contextEnded"`
	Peers                  [2]nearbyBrowserCleanupPeer `json:"peers"`
	SummaryOpened          bool                        `json:"summaryOpened"`
	SummaryWritten         bool                        `json:"summaryWritten"`
	SummaryClosed          bool                        `json:"summaryClosed"`
}
type nearbyBrowserCleanupPeer struct {
	CancelReturned bool   `json:"cancelReturned"`
	CancelDecoded  bool   `json:"cancelDecoded"`
	CancelState    string `json:"cancelState"`
	CancelReason   string `json:"cancelReason"`
	Joined         bool   `json:"joined"`
	PostObserved   bool   `json:"postObserved"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	Connected      bool   `json:"connected"`
	Backend        bool   `json:"backend"`
	FreshHello     string `json:"freshHello"`
	Listener       string `json:"listener"`
	ChildExit      string `json:"childExit"`
}

func newNearbyBrowserCleanupEvidence() *nearbyBrowserCleanupEvidence {
	v := &nearbyBrowserCleanupEvidence{Stage: "browser_returned", Peer: "none", BrowserCommandExited: true, BrowserReceiptVerified: true}
	for i := range v.Peers {
		v.Peers[i] = nearbyBrowserCleanupPeer{CancelState: "unknown", CancelReason: "other", State: "unknown", Reason: "other", FreshHello: "not_run", Listener: "not_run", ChildExit: "not_run"}
	}
	return v
}
func validNearbyBrowserCleanupEvidence(v nearbyBrowserCleanupEvidence) bool {
	switch v.Stage {
	case "browser_returned", "cancel_snapshot", "cancel_request", "cancel_reply", "join", "post_cancel_snapshot", "post_cancel_backend", "post_cancel_hello", "listener_release", "child_exit", "summary_open", "summary_write", "summary_close", "complete":
	default:
		return false
	}
	if v.Peer != "a" && v.Peer != "b" && v.Peer != "none" {
		return false
	}
	for _, p := range v.Peers {
		if nearbyBrowserState(p.CancelState) != p.CancelState || nearbyBrowserState(p.State) != p.State {
			return false
		}
		for _, reason := range []string{p.CancelReason, p.Reason} {
			if reason != "none" && reason != "other" && nearbyBrowserReason(reason) != reason {
				return false
			}
		}
		if p.FreshHello != "not_run" && p.FreshHello != "passed" && p.FreshHello != "failed" {
			return false
		}
		if p.Listener != "not_run" && p.Listener != "dial_rejected" && p.Listener != "still_open" {
			return false
		}
		switch p.ChildExit {
		case "not_run", "requesting", "replied", "joined", "exit_failed", "timeout":
		default:
			return false
		}
	}
	return true
}

// Real production /v1/hello handler plus the production strict peerJSON decoder,
// over existing net.Pipe transport only. This is a collector regression, never
// a substitute for native transport or actual-browser acceptance.
func TestNearbyBrowserHelloStrictProductionResponse(t *testing.T) {
	p := newCorePair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var old struct {
		Protocol int    `json:"protocol"`
		Product  string `json:"product"`
	}
	if err := p.a.peerJSON(ctx, "peer-b", "GET", "/v1/hello", nil, &old); err == nil || !strings.Contains(err.Error(), `unknown field "capabilities"`) {
		t.Fatal("old incomplete collector did not reject the actual handler response")
	}
	if !browserSetupPeerHello(ctx, p.a, "peer-b") {
		t.Fatal("complete strict collector rejected actual peer hello")
	}
	if len(p.a.profileCopy().Peers) != 0 || len(p.b.profileCopy().Peers) != 0 {
		t.Fatal("hello collector granted receiving trust")
	}
}
func TestNearbyBrowserFinalEvidencePreservesEveryInvariant(t *testing.T) {
	checks := nearbyBrowserChecks{}
	fields := reflect.ValueOf(&checks).Elem()
	for i := 0; i < fields.NumField(); i++ {
		fields.Field(i).SetBool(true)
	}
	if !checks.ready() {
		t.Fatal("complete evidence rejected")
	}
	for i := 0; i < fields.NumField(); i++ {
		fields.Field(i).SetBool(false)
		if checks.ready() {
			t.Fatal("missing required invariant accepted")
		}
		fields.Field(i).SetBool(true)
	}
	// Connected is published before discovery joins; it is not final readiness.
	checks.DiscoveryStopped = false
	if checks.ready() {
		t.Fatal("connected alone was accepted before discovery closure")
	}
	checks.DiscoveryStopped = true
	if !checks.ready() {
		t.Fatal("joined discovery did not satisfy unchanged final invariants")
	}
	// The predicate vector is derived from actual DTO/state comparisons, not UI
	// booleans. Compare both the complete case and all missing snapshot inputs.
	reviewed := SetupSnapshot{SessionID: "synthetic-session", Plan: &SetupPlanView{Fingerprint: "synthetic-plan"}}
	current := reviewed
	current.State, current.Connected, current.Paired = "connected", true, true
	current.LocalApproved, current.RemoteApproved = true, true
	current.Discovery.State = "stopped"
	evidence := setupBrowserEvidence{Backend: true, Pairs: 1, StrictScope: true, LocalHello: true, RemoteReceipt: true}
	observed := observeNearbyBrowserPeer(current, reviewed, evidence)
	if !observed.Observed || observed.Checks != checks || observed.State != "connected" || observed.Reason != "none" {
		t.Fatal("collector did not derive exact final invariants")
	}
	for _, missing := range []SetupSnapshot{{}, {SessionID: "synthetic-other"}, {Plan: reviewed.Plan}} {
		if observeNearbyBrowserPeer(missing, reviewed, evidence).Checks.ready() {
			t.Fatal("missing state or plan was accepted")
		}
	}
	current.State, current.ReasonCode = "synthetic-private-state", "synthetic-private-error"
	observed = observeNearbyBrowserPeer(current, reviewed, evidence)
	if observed.State != "unknown" || observed.Reason != "other" {
		t.Fatal("unknown backend values were not reduced to fixed categories")
	}

	v := nearbyBrowserCoreEvidence{Stage: "invariants", CheckpointArrived: true}
	for i := range v.Peers {
		v.Peers[i] = nearbyBrowserPeerEvidence{Observed: true, State: "connected", Reason: "none", Checks: checks, FreshHello: "not_run"}
	}
	failure := nearbyBrowserFailure{Schema: 1, Phase: "core-connected", ErrorKind: "core_evidence", Core: &v}
	if !validNearbyBrowserFailure(failure) {
		t.Fatal("closed core evidence rejected")
	}
	raw, err := json.Marshal(failure)
	if err != nil || len(raw) > nearbyBrowserFailureMaxBytes {
		t.Fatal("core evidence exceeded fixed bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var roundTrip nearbyBrowserFailure
	if decoder.Decode(&roundTrip) != nil || decoder.Decode(new(any)) != io.EOF || !reflect.DeepEqual(failure, roundTrip) {
		t.Fatal("closed evidence roundtrip failed")
	}
	for _, value := range []string{"synthetic-private-path", "synthetic-private-token"} {
		v.Peers[0].Reason = value
		if validNearbyBrowserFailure(failure) {
			t.Fatal("unbounded core reason accepted")
		}
		v.Peers[0].Reason = "none"
		v.Peers[0].State = value
		if validNearbyBrowserFailure(failure) {
			t.Fatal("unbounded core state accepted")
		}
		v.Peers[0].State = "connected"
		v.Peers[0].FreshHello = value
		if validNearbyBrowserFailure(failure) {
			t.Fatal("unbounded hello outcome accepted")
		}
		v.Peers[0].FreshHello = "not_run"
	}
}

func TestNearbyBrowserCleanupEvidenceSchema(t *testing.T) {
	v := newNearbyBrowserCleanupEvidence()
	failure := nearbyBrowserFailure{Schema: 1, Phase: "core-cleanup", ErrorKind: "core_evidence", Cleanup: v}
	if !validNearbyBrowserFailure(failure) {
		t.Fatal("initial closed cleanup vector rejected")
	}
	for _, value := range []string{"synthetic-private-path", "synthetic-private-token"} {
		v.Stage = value
		if validNearbyBrowserFailure(failure) {
			t.Fatal("unbounded cleanup stage accepted")
		}
		v.Stage = "join"
		v.Peer = value
		if validNearbyBrowserFailure(failure) {
			t.Fatal("unbounded cleanup peer accepted")
		}
		v.Peer = "a"
		peer := reflect.ValueOf(&v.Peers[0]).Elem()
		for i := 0; i < peer.NumField(); i++ {
			if peer.Field(i).Kind() == reflect.String {
				before := peer.Field(i).String()
				peer.Field(i).SetString(value)
				if validNearbyBrowserFailure(failure) {
					t.Fatal("unbounded cleanup observation accepted")
				}
				peer.Field(i).SetString(before)
			}
		}
	}
	// The largest normal failure includes both the successful pre-cancel
	// checkpoint and the subsequent cleanup observations, below the same cap.
	checks := nearbyBrowserChecks{}
	fields := reflect.ValueOf(&checks).Elem()
	for i := 0; i < fields.NumField(); i++ {
		fields.Field(i).SetBool(true)
	}
	core := &nearbyBrowserCoreEvidence{Stage: "acknowledged", CheckpointArrived: true}
	for i := range core.Peers {
		core.Peers[i] = nearbyBrowserPeerEvidence{Observed: true, State: "connected", Reason: "none", Checks: checks, FreshHello: "passed"}
	}
	failure.Core = core
	raw, err := json.Marshal(failure)
	if err != nil || len(raw)+1 > nearbyBrowserFailureMaxBytes {
		t.Fatal("combined cleanup evidence exceeded fixed bound")
	}
	var decoded nearbyBrowserFailure
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&decoded) != nil || d.Decode(new(any)) != io.EOF || !reflect.DeepEqual(failure, decoded) {
		t.Fatal("combined cleanup evidence changed in roundtrip")
	}
}

type nearbyCleanupInput struct{ bytes.Buffer }

func (v *nearbyCleanupInput) Close() error { return nil }
func TestNearbyBrowserCleanupProcessObserver(t *testing.T) {
	input := new(nearbyCleanupInput)
	p := &routeCoreProcess{input: input, output: json.NewDecoder(strings.NewReader(`{"Value":true,"Error":""}`)), done: make(chan error, 1)}
	p.done <- nil
	var seen []string
	p.stopObserved(t, func(stage string) { seen = append(seen, stage) })
	if !p.stopped || !reflect.DeepEqual(seen, []string{"requesting", "replied", "joined"}) {
		t.Fatal("process observer did not preserve shutdown order")
	}
	var command struct {
		Name      string
		Payload   any
		RequestID string
	}
	if json.Unmarshal(input.Bytes(), &command) != nil || command.Name != "fixture.exit" || command.Payload != nil || command.RequestID != "" {
		t.Fatal("observer changed shutdown command")
	}
}
