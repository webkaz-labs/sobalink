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
