//go:build lanlink_integration && directlan_integration

package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// Read-only failure diagnostics deliberately project only readiness, canonical
// codes and counts. IDs are used solely to select the original peer/delivery;
// no raw reply, path, name, credential, lease or payload is printed.
func logOutboxResumeDiagnostics(t *testing.T, p *routeCoreProcess, peerID, requestID, deliveryID, role string) {
	t.Helper()
	var phase outboxProcessPhase
	phaseReply := p.invokeResult(t, "fixture.outbox-phase", map[string]string{"requestId": requestID, "deliveryId": deliveryID})
	if phaseReply.Error != "" || json.Unmarshal(phaseReply.Value, &phase) != nil {
		t.Logf("outbox resume %s phase=unavailable", role)
	} else {
		t.Logf("outbox resume %s phase context-done=%t next-known=%t history-observed=%t", role, phase.ContextDone, phase.NextKnown, phase.History.Observed)
		h := phase.History
		t.Logf("outbox resume %s history maintenance-entered=%d busy=%d completed=%d pump-entered=%d owner-admitted=%d busy=%d refused=%d gated=%d last-gate=%q", role, h.MaintenanceEntered, h.MaintenanceBusy, h.MaintenanceCompleted, h.PumpEntered, h.OwnerAdmitted, h.OwnerBusy, h.OwnerRefused, h.Gated, outboxDiagnosticToken(h.LastGate))
		t.Logf("outbox resume %s history planned=%d run-admission-refused=%d finish-entered=%d owner-admitted=%d busy=%d refused=%d saved=%d save-failed=%d no-row=%d", role, h.Planned, h.RunAdmissionRefused, h.FinishEntered, h.FinishOwnerAdmitted, h.FinishOwnerBusy, h.FinishOwnerRefused, h.FinishSaved, h.FinishSaveFailed, h.FinishNoRow)
		t.Logf("outbox resume %s phase op-busy=%t refresh-busy=%t coordinator=%t coordinator-busy=%t selected=%t run=%t run-busy=%t run-state=%q running=%t next-pending=%t", role, phase.OpBusy, phase.RefreshBusy, phase.Coordinator, phase.CoordinatorBusy, phase.Selected, phase.RunPresent, phase.RunBusy, outboxDiagnosticToken(phase.RunState), phase.Running, phase.NextPending)
		t.Logf("outbox resume %s phase owner-check=%q owner-admitted=%t namespace-check=%q core-busy=%t active=%d queue=%d selected-queued=%t", role, outboxDiagnosticToken(phase.OwnerCheck), phase.OwnerAdmitted, outboxDiagnosticToken(phase.NamespaceCheck), phase.CoreBusy, phase.Active, phase.Queue, phase.SelectedQueued)
	}
	var snapshot struct {
		OutboxReady bool `json:"outboxReady"`
		Self        struct {
			Status    string `json:"status"`
			ErrorCode string `json:"errorCode"`
		} `json:"self"`
		Peers []struct {
			ID        string `json:"id"`
			Online    bool   `json:"online"`
			Verified  bool   `json:"verified"`
			Trusted   bool   `json:"trusted"`
			Discovery struct {
				State string `json:"state"`
				Code  string `json:"code"`
			} `json:"discovery"`
		} `json:"peers"`
		Transfers []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"transfers"`
	}
	reply := p.invokeResult(t, "fixture.snapshot", struct{}{})
	if reply.Error != "" || json.Unmarshal(reply.Value, &snapshot) != nil {
		t.Logf("outbox resume %s snapshot=unavailable", role)
	} else {
		t.Logf("outbox resume %s ready=%t network=%q code=%q peers=%d transfers=%d", role, snapshot.OutboxReady, outboxDiagnosticToken(snapshot.Self.Status), outboxDiagnosticToken(snapshot.Self.ErrorCode), len(snapshot.Peers), len(snapshot.Transfers))
		matched := 0
		for _, peer := range snapshot.Peers {
			if peer.ID == peerID {
				matched++
				if matched == 1 {
					t.Logf("outbox resume %s selected-peer online=%t verified=%t trusted=%t discovery=%q code=%q", role, peer.Online, peer.Verified, peer.Trusted, outboxDiagnosticToken(peer.Discovery.State), outboxDiagnosticToken(peer.Discovery.Code))
				}
			}
		}
		transfers, status := 0, ""
		for _, row := range snapshot.Transfers {
			if row.ID == deliveryID {
				transfers++
				status = row.Status
			}
		}
		t.Logf("outbox resume %s selected-peers=%d selected-transfers=%d transfer-status=%q", role, matched, transfers, outboxDiagnosticToken(status))
	}
	var list struct {
		Status     string `json:"status"`
		ReasonCode string `json:"reasonCode"`
		Items      []struct {
			RequestID       string `json:"requestId"`
			Status          string `json:"status"`
			ReasonCode      string `json:"reasonCode"`
			DeliveryOutcome string `json:"deliveryOutcome"`
			Children        []struct {
				DeliveryID      string `json:"deliveryId"`
				Status          string `json:"status"`
				ReasonCode      string `json:"reasonCode"`
				DeliveryOutcome string `json:"deliveryOutcome"`
			} `json:"children"`
		} `json:"items"`
	}
	reply = p.invokeResult(t, "outbox.list", struct{}{})
	if reply.Error != "" || json.Unmarshal(reply.Value, &list) != nil {
		t.Logf("outbox resume %s list=unavailable", role)
		return
	}
	t.Logf("outbox resume %s list-status=%q reason=%q items=%d", role, outboxDiagnosticToken(list.Status), outboxDiagnosticToken(list.ReasonCode), len(list.Items))
	for _, row := range list.Items {
		if row.RequestID != requestID {
			continue
		}
		t.Logf("outbox resume %s original status=%q reason=%q outcome=%q children=%d", role, outboxDiagnosticToken(row.Status), outboxDiagnosticToken(row.ReasonCode), outboxDiagnosticToken(row.DeliveryOutcome), len(row.Children))
		for _, child := range row.Children {
			if child.DeliveryID == deliveryID {
				t.Logf("outbox resume %s original-child status=%q reason=%q outcome=%q", role, outboxDiagnosticToken(child.Status), outboxDiagnosticToken(child.ReasonCode), outboxDiagnosticToken(child.DeliveryOutcome))
				break
			}
		}
		break
	}
}

func outboxDiagnosticToken(value string) string {
	if value == "" {
		return ""
	}
	// A closed allowlist prevents even a malformed code field from carrying
	// private identifiers or free-form runtime errors into native CI output.
	const allowed = `none profile_guard namespace_check clock_guard context_done retirement persist_failed
ok refused not_checked ready online offline starting stopping stopped unavailable error reconnecting
Running Starting Stopped NoState NeedsLogin NeedsMachineAuth InUse OtherUser
pending confirmed unconfirmed unsupported limited stale prepared discarded queued
waiting_for_peer sending paused awaiting_receipt cancel_requested cancelled expired completed
blocked_recovery blocked_permission not_attempted unknown partial accepted receiving
rejected declined failed transferring awaiting-acceptance enqueued busy
outbox_capacity outbox_clock_rollback outbox_clock_skew outbox_disabled outbox_expired
outbox_identity_changed outbox_identity_unavailable outbox_invalid outbox_not_found
outbox_paused outbox_payload_corrupt outbox_payload_missing outbox_payload_recovery_required
outbox_peer_offline outbox_peer_upgrade_required outbox_permission_changed outbox_preparing
outbox_receipt_mismatch outbox_receipt_pending outbox_receipt_regressed outbox_receiver_approval
outbox_receiver_declined outbox_recovery_required outbox_retired outbox_stale
outbox_storage_busy outbox_storage_uncertain durable_capacity durable_clock durable_conflict
durable_expired durable_permission durable_recovery_required durable_unknown_lease
durable_unsupported_storage discovery_unconfirmed discovery_unsupported discovery_capacity
direct_lan_address_unavailable direct_lan_address_unknown direct_lan_backend_pending
direct_lan_start_failed direct_lan_recovery_required direct_lan_unavailable direct_lan_failed
direct_lan_capacity direct_lan_identity direct_lan_policy direct_lan_pair_state
direct_lan_remote_paired_local_save direct_lan_pair_uncertain direct_lan_invitation_invalid`
	for _, token := range strings.Fields(allowed) {
		if value == token {
			return token
		}
	}
	return "other"
}

func TestOutboxDiagnosticTokensExcludeFreeFormValues(t *testing.T) {
	for _, value := range []string{"C:\\synthetic\\profile", "/synthetic/profile", strings.Repeat("a", 64), "synthetic content", "outbox_peer_offline\nsynthetic content"} {
		if outboxDiagnosticToken(value) != "other" {
			t.Fatal("noncanonical diagnostic value was retained")
		}
	}
	for _, value := range []string{"", "online", "offline", "ready", "starting", "reconnecting", "unavailable", "confirmed", "unconfirmed", "unsupported", "limited", "pending", "stale", "sending", "transferring", "declined", "awaiting-acceptance", "not_attempted", "outbox_storage_uncertain", "durable_permission"} {
		if outboxDiagnosticToken(value) != value {
			t.Fatal("canonical diagnostic value was lost")
		}
	}
}
