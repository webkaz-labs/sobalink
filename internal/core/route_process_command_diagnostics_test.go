//go:build lanlink_integration

package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Keep the production command and its outcome unchanged. Only the failing
// acceptance reply gains the existing closed transfer classifier; no raw error,
// path, identity or lease is copied into the subprocess diagnostic stream.
func routeProcessCommandErrorCode(name string, err error) string {
	if name == "transfer.accept" && err != nil && networkErrorCode(err) == "" {
		return routeProcessAcceptCause(err)
	}
	return networkErrorCode(err)
}

// The public nonretryable wrapper intentionally has one conservative code.
// Preserve a separate fixed underlying cause only in failing test replies.
func routeProcessAcceptCause(err error) string {
	if err == nil {
		return ""
	}
	code := transfer.DurableErrorCode(err)
	if code == "durable_recovery_required" || code == "receive_recovery_required" {
		return code
	}
	if errors.Is(err, config.ErrAtomicCommitted) {
		return "atomic_publication_uncertain"
	}
	if code != "transfer_failed" && code != "busy" {
		return code
	}
	for _, known := range []struct {
		err  error
		code string
	}{
		{os.ErrPermission, "filesystem_permission"},
		{os.ErrNotExist, "filesystem_not_found"},
		{os.ErrExist, "filesystem_exists"},
		{context.Canceled, "context_canceled"},
		{context.DeadlineExceeded, "context_deadline"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	return code
}

func routeProcessAcceptCauseToken(code string) string {
	switch code {
	case "", "durable_conflict", "durable_unknown_lease", "durable_expired",
		"durable_clock", "durable_capacity", "durable_evidence_capacity",
		"durable_unsupported_storage", "durable_permission", "durable_recovery_required",
		"receive_recovery_required", "invalid_manifest", "limit_exceeded",
		"unknown_peer", "peer_changed", "peer_paused", "not_found", "destination_conflict",
		"invalid_state", "disk_space_low", "disk_space_unknown", "busy", "integrity_mismatch",
		"cancelled", "closed", "unsafe_path", "transfer_failed", "atomic_publication_uncertain",
		"filesystem_permission", "filesystem_not_found", "filesystem_exists", "context_canceled", "context_deadline":
		return code
	default:
		return "other"
	}
}

func TestRouteProcessAcceptErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, ""},
		{"busy", transfer.ErrBusy, "busy"},
		{"wrapped-busy", fmt.Errorf("synthetic-private-value: %w", transfer.ErrBusy), "busy"},
		{"recovery", transfer.ErrDurableRecovery, "durable_recovery_required"},
		{"recovery-with-busy", errors.Join(transfer.ErrDurableRecovery, transfer.ErrBusy), "durable_recovery_required"},
		{"uncertain-with-busy", errors.Join(transfer.ErrDurableRecovery, config.ErrAtomicCommitted, transfer.ErrBusy), "durable_recovery_required"},
		{"permission", transfer.ErrDurablePermission, "durable_permission"},
		{"peer", transfer.ErrPeerChanged, "peer_changed"},
		{"paused", transfer.ErrPeerPaused, "peer_paused"},
		{"expired", transfer.ErrDurableExpired, "durable_expired"},
		{"clock", transfer.ErrDurableClock, "durable_clock"},
		{"state", transfer.ErrState, "invalid_state"},
		{"missing", transfer.ErrNotFound, "not_found"},
		{"space", diskspace.ErrLow, "disk_space_low"},
		{"space-unknown", diskspace.ErrUnknown, "disk_space_unknown"},
		{"storage", transfer.ErrDurableStorage, "durable_unsupported_storage"},
		{"unsafe", transfer.ErrUnsafePath, "unsafe_path"},
		{"raw", errors.New("synthetic-private-value busy /synthetic/profile"), "transfer_failed"},
		{"path", &os.PathError{Op: "open", Path: "/synthetic/private-profile", Err: os.ErrPermission}, "filesystem_permission"},
		{"missing-path", &os.PathError{Op: "open", Path: "C:\\synthetic\\private-profile", Err: os.ErrNotExist}, "filesystem_not_found"},
		{"existing-path", os.ErrExist, "filesystem_exists"},
		{"uncertain-raw", errors.Join(config.ErrAtomicCommitted, os.ErrPermission), "atomic_publication_uncertain"},
		{"uncertain-raw-with-busy", errors.Join(config.ErrAtomicCommitted, transfer.ErrBusy), "atomic_publication_uncertain"},
		{"permission-with-busy", errors.Join(os.ErrPermission, transfer.ErrBusy), "filesystem_permission"},
		{"canceled", context.Canceled, "context_canceled"},
		{"deadline", context.DeadlineExceeded, "context_deadline"},
		{"coded", &localCommandError{"existing_code", "synthetic-private-value"}, "existing_code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeProcessCommandErrorCode("transfer.accept", tc.err); got != tc.want {
				t.Fatal("acceptance diagnostic lost a closed classification")
			}
			if tc.name != "coded" && routeProcessAcceptCause(transferAcceptCommandError(tc.err)) != tc.want {
				t.Fatal("public acceptance wrapper erased the fixed underlying diagnostic")
			}
			if tc.name != "coded" && routeProcessAcceptCauseToken(tc.want) != tc.want {
				t.Fatal("fixed acceptance cause did not survive parent filtering")
			}
		})
	}
	for _, name := range []string{"transfer.cancel", "outbox.commit", "fixture.snapshot", "transfer.accept-extra"} {
		if routeProcessCommandErrorCode(name, transfer.ErrBusy) != "" {
			t.Fatal("acceptance diagnostic changed another command")
		}
	}
	for _, disk := range []error{diskspace.ErrLow, diskspace.ErrUnknown} {
		wrapped := transferAcceptCommandError(errors.Join(disk, config.ErrAtomicCommitted))
		if routeProcessCommandErrorCode("transfer.accept", wrapped) != "transfer_accept_unconfirmed" || routeProcessAcceptCause(wrapped) != "atomic_publication_uncertain" {
			t.Fatal("nested disk-space code masked public or diagnostic uncertainty")
		}
	}
	for _, value := range []string{"/synthetic/private", "C:\\synthetic\\private", "busy\nsynthetic", "transfer_accept_busy", "arbitrary_cause"} {
		if routeProcessAcceptCauseToken(value) != "other" {
			t.Fatal("free-form cause escaped parent filtering")
		}
	}
}
