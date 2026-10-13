package nearby

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// API observations are separate from the raw native capability marker. Even a
// selected-interface callback does not attest an original network datagram.
type dnssdCapabilityResult struct {
	Status                   string                `json:"status"`
	Reason                   string                `json:"reason"`
	Scope                    string                `json:"scope"`
	ReciprocalRecords        bool                  `json:"reciprocalRecords"`
	ReciprocalCandidates     bool                  `json:"reciprocalCandidates"`
	WithdrawalObserved       bool                  `json:"withdrawalObserved"`
	OwnersJoined             bool                  `json:"ownersJoined"`
	FailureStage             string                `json:"failureStage"`
	FailureKind              string                `json:"failureKind"`
	RawPacketVerified        bool                  `json:"rawPacketVerified"`
	NetworkMulticastVerified bool                  `json:"networkMulticastVerified"`
	ReceiveCounters          *dnssdReceiveCounters `json:"receiveCounters"`
}

func dnssdCapability(scope string) dnssdCapabilityResult {
	return dnssdCapabilityResult{Status: "unavailable", Reason: "deadline", Scope: scope, FailureStage: "none", FailureKind: "none"}
}
func dnssdCapabilityError(result *dnssdCapabilityResult, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return
	}
	result.Status = "unavailable"
	result.ReciprocalRecords = false
	result.ReciprocalCandidates = false
	result.WithdrawalObserved = false
	result.RawPacketVerified = false
	result.NetworkMulticastVerified = false
	var native dnssdFailure
	if errors.As(err, &native) {
		result.Reason = "native_api_unavailable"
		result.FailureStage = native.stage
		result.FailureKind = native.kind
		return
	}
	if errors.Is(err, ErrScopeChanged) {
		result.Reason = "selected_binding_changed"
		return
	}
	result.Status, result.Reason = "failed", "provider_invariant"
}
func TestDNSServiceCapabilityLateErrorDowngradesProof(t *testing.T) {
	for _, err := range []error{dnssdFailure{"callback", "name_conflict"}, ErrScopeChanged, ErrInvalidPacket} {
		result := dnssdCapability("selected_private_interface_raw_receive")
		result.Status = "verified"
		result.Reason = "native_raw_receive_observed"
		result.RawPacketVerified = true
		result.OwnersJoined = true
		dnssdCapabilityError(&result, err)
		if result.Status == "verified" || result.RawPacketVerified || result.ReciprocalRecords || result.ReciprocalCandidates || result.WithdrawalObserved || result.NetworkMulticastVerified || !result.OwnersJoined {
			t.Fatalf("late failure retained final proof: %+v", result)
		}
	}
}

// The hosted diagnostic driver must not start another scope after an explicit
// native policy denial. This test-only latch never changes production policy.
// These five top-level cases are intentionally not parallel. Observation is
// recorded after each run function returns and its cleanup defers complete.
type dnssdDiagnosticBatch struct{ denied atomic.Bool }

func (b *dnssdDiagnosticBatch) allowed() bool { return !b.denied.Load() }
func (b *dnssdDiagnosticBatch) observe(kind string) {
	if kind == "permission_denied" {
		b.denied.Store(true)
	}
}

var dnssdNativeDiagnosticBatch dnssdDiagnosticBatch

func requireDNSServiceBatchPermission(t *testing.T) {
	t.Helper()
	if !dnssdNativeDiagnosticBatch.allowed() {
		t.Skip("diagnostic stopped after prior native policy denial")
	}
}

func TestDNSServiceBatchDenialFence(t *testing.T) {
	for _, kind := range []string{"none", "host_unreachable", "permission_denied"} {
		t.Run(kind, func(t *testing.T) {
			var batch dnssdDiagnosticBatch
			started, joined := 0, false
			run := func() string {
				started++
				defer func() { joined = true }()
				return kind
			}
			batch.observe(run())
			if !joined {
				t.Fatal("result observed before owned cleanup returned")
			}
			if batch.allowed() {
				started++
			}
			want := 2
			if kind == "permission_denied" {
				want = 1
			}
			if started != want {
				t.Fatal("batch started a scope outside the denial fence")
			}
			batch.observe("none")
			if kind == "permission_denied" && batch.allowed() {
				t.Fatal("denial was not latched")
			}
		})
	}
}
