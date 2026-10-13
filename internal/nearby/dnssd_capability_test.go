package nearby

import (
	"context"
	"errors"
	"testing"
)

// API observations are separate from the raw native capability marker. Even a
// selected-interface callback does not attest an original network datagram.
type dnssdCapabilityResult struct {
	Status                   string `json:"status"`
	Reason                   string `json:"reason"`
	Scope                    string `json:"scope"`
	ReciprocalRecords        bool   `json:"reciprocalRecords"`
	ReciprocalCandidates     bool   `json:"reciprocalCandidates"`
	WithdrawalObserved       bool   `json:"withdrawalObserved"`
	OwnersJoined             bool   `json:"ownersJoined"`
	FailureStage             string `json:"failureStage"`
	FailureKind              string `json:"failureKind"`
	RawPacketVerified        bool   `json:"rawPacketVerified"`
	NetworkMulticastVerified bool   `json:"networkMulticastVerified"`
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
