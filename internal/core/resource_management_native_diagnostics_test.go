package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Test-only failure output contains no response, selector, path, identity or
// raw error. Keep the original nil-value/exact-code assertion at the call site.
func resourceManagementNativeDenialSummary(expected string, valuePresent bool, err, fixtureErr error, elapsed time.Duration) string {
	code := func(value string) string {
		switch value {
		case "resource_management_remote_unsupported", "resource_management_remote_unavailable", "resource_grant_invalid":
			return value
		default:
			return "other"
		}
	}
	actual := "none"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		actual = "deadline"
	case errors.Is(err, context.Canceled):
		actual = "cancelled"
	case err != nil:
		actual = code(networkErrorCode(err))
	}
	fixture := "active"
	switch {
	case errors.Is(fixtureErr, context.DeadlineExceeded):
		fixture = "deadline"
	case errors.Is(fixtureErr, context.Canceled):
		fixture = "cancelled"
	case fixtureErr != nil:
		fixture = "other"
	}
	return fmt.Sprintf("denied management request failed: expected=%s actual=%s value_present=%t elapsed_ms=%d fixture_context=%s", code(expected), actual, valuePresent, elapsed.Milliseconds(), fixture)
}

func TestResourceManagementNativeDenialDiagnostics(t *testing.T) {
	const unsupported = "resource_management_remote_unsupported"
	const unavailable = "resource_management_remote_unavailable"
	want := "denied management request failed: expected=" + unsupported + " actual=" + unavailable + " value_present=false elapsed_ms=15001 fixture_context=active"
	if got := resourceManagementNativeDenialSummary(unsupported, false, &localCommandError{unavailable, "synthetic private error"}, nil, 15001*time.Millisecond); got != want {
		t.Fatal("denial diagnostic lost its exact closed result or timing")
	}
	for _, tc := range []struct {
		err         error
		actual      string
		fixtureErr  error
		fixtureCode string
	}{
		{nil, "none", nil, "active"},
		{context.DeadlineExceeded, "deadline", context.DeadlineExceeded, "deadline"},
		{context.Canceled, "cancelled", context.Canceled, "cancelled"},
		{&localCommandError{unsupported, "synthetic private error"}, unsupported, context.DeadlineExceeded, "deadline"},
		{&localCommandError{"resource_grant_invalid", "synthetic private error"}, "resource_grant_invalid", context.Canceled, "cancelled"},
		{resourceManagementNativeUnformattableError{}, "other", resourceManagementNativeUnformattableError{}, "other"},
	} {
		got := resourceManagementNativeDenialSummary(unsupported, true, tc.err, tc.fixtureErr, time.Millisecond)
		if !strings.Contains(got, "actual="+tc.actual+" value_present=true elapsed_ms=1 fixture_context="+tc.fixtureCode) || strings.Contains(got, "synthetic") {
			t.Fatal("denial diagnostic changed presence/context evidence or retained error text")
		}
	}
	for _, raw := range []string{"synthetic/private", unsupported + "\nsynthetic/private", "", strings.Repeat("a", 64)} {
		got := resourceManagementNativeDenialSummary(raw, false, &localCommandError{raw, "synthetic private error"}, nil, 0)
		if got != "denied management request failed: expected=other actual=other value_present=false elapsed_ms=0 fixture_context=active" {
			t.Fatal("denial diagnostic retained a noncanonical code")
		}
	}
}

type resourceManagementNativeUnformattableError struct{}

func (resourceManagementNativeUnformattableError) Error() string {
	panic("denial diagnostic formatted raw error")
}
