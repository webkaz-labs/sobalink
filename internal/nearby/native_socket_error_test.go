package nearby

import (
	"errors"
	"testing"
)

func TestNativeSocketStageIsTypedAndRedacted(t *testing.T) {
	err := socketFailure("socket_bind")
	if !errors.Is(err, ErrSocketUnavailable) || !IsUnavailable(err) || err.Error() != ErrSocketUnavailable.Error() || socketFailureStage(err) != "socket_bind" {
		t.Fatal("socket failure lost unavailable semantics or exposed platform data")
	}
	joined := errors.Join(err, ErrCloseUncertain)
	if IsUnavailable(joined) || socketFailureStage(joined) != "socket_bind" {
		t.Fatal("stage obscured cleanup uncertainty")
	}
	for _, other := range []error{nil, ErrUnsupported, ErrScopeChanged, errors.New("synthetic private text")} {
		if socketFailureStage(other) != "none" {
			t.Fatal("untyped error text became diagnostic stage")
		}
	}
}

func TestNativeSendClassifiesCountsWithoutLeakingErrors(t *testing.T) {
	for _, tc := range []struct {
		n, want, control, wantControl int
		err                           error
		kind                          string
	}{
		{12, 12, 24, 24, nil, "none"},
		{11, 12, 24, 24, nil, "short_payload"},
		{12, 12, 0, 24, nil, "short_control"},
		{0, 12, 0, 24, errors.New("synthetic private platform text"), "other"},
	} {
		err := classifyNativeSend(tc.n, tc.want, tc.control, tc.wantControl, tc.err)
		if nativeSendFailureKind(err) != tc.kind {
			t.Fatal("wrong fixed send kind")
		}
		if err != nil && (err.Error() != ErrSocketUnavailable.Error() || socketFailureStage(err) != "socket_write") {
			t.Fatal("send failure exposed raw platform error")
		}
	}
}
func TestNativeSendProbeCannotUpgradeProductionProof(t *testing.T) {
	result := nativeCapabilityResult{Status: "unavailable", Reason: "native_receive_unavailable", OwnersJoined: true, SocketFailureStage: "socket_write", SendFailureKind: "invalid_argument"}
	recordNativeSendProbe(&result, &nativeSendProbeResult{Status: "verified", Reason: "scoped_no_ancillary_delivery", MetadataVerified: true, OwnersJoined: true})
	if result.Status != "unavailable" || result.BidirectionalCandidates || result.WindowsNoAncillaryProbe.ProductionDiscoveryVerified {
		t.Fatal("supplemental probe upgraded production proof")
	}
	for _, reason := range []string{"foreign_or_missing_packet_metadata", "cleanup_close_uncertain", "cleanup_not_joined"} {
		recordNativeSendProbe(&result, &nativeSendProbeResult{Status: "failed", Reason: reason})
		if result.Status != "failed" || result.Reason != reason {
			t.Fatal("unsafe probe remained a green unavailable diagnostic")
		}
	}
}
