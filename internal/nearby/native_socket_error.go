package nearby

import (
	"errors"
	"net"
)

// Fixed stages distinguish adapter failures without exposing addresses,
// interface identifiers, paths or underlying platform error text.
type nativeSocketStage string

type nativeSocketError struct {
	stage    nativeSocketStage
	sendKind string
	bindKind string
	bindRole string
}

func (e nativeSocketError) Error() string         { return ErrSocketUnavailable.Error() }
func (e nativeSocketError) Unwrap() error         { return ErrSocketUnavailable }
func socketFailure(stage nativeSocketStage) error { return nativeSocketError{stage: stage} }
func socketFailureStage(err error) string {
	var failure nativeSocketError
	if errors.As(err, &failure) {
		return string(failure.stage)
	}
	return "none"
}

func nativeBindFailure(role string, err error) error {
	return nativeSocketError{stage: "socket_bind", bindRole: role, bindKind: nativeBindErrorKind(err)}
}
func nativeBindFailureKind(err error) string {
	var failure nativeSocketError
	if errors.As(err, &failure) && failure.bindKind != "" {
		return failure.bindKind
	}
	return "none"
}
func nativeBindSocketRole(err error) string {
	var failure nativeSocketError
	if errors.As(err, &failure) && failure.bindRole != "" {
		return failure.bindRole
	}
	return "none"
}

func nativeSendFailureKind(err error) string {
	var failure nativeSocketError
	if errors.As(err, &failure) && failure.sendKind != "" {
		return failure.sendKind
	}
	return "none"
}
func classifyNativeSend(n, want, control, wantControl int, err error) error {
	kind := "none"
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			kind = "timeout"
		} else {
			kind = nativeSendErrorKind(err)
		}
	} else if n != want {
		kind = "short_payload"
	} else if control != wantControl {
		kind = "short_control"
	}
	if kind == "none" {
		return nil
	}
	return nativeSocketError{stage: "socket_write", sendKind: kind}
}
