package transfer

import (
	"context"
	"errors"
	"os"
)

// ReceiveRecoveryView deliberately contains neither private paths nor tokens.
// A nil byte count means unknown, never an empty inventory.
type ReceiveRecoveryView struct {
	State         string   `json:"state"`
	Code          string   `json:"code"`
	ReservedBytes *int64   `json:"reservedBytes"`
	Applied       bool     `json:"applied"`
	Review        []string `json:"review"`
}

func (m *Manager) recoveryViewLocked(applied bool) ReceiveRecoveryView {
	view := ReceiveRecoveryView{State: "ready", Code: m.recoveryCode, Applied: applied, Review: []string{"previous_default_destinations", "previous_peer_destinations", "previous_manual_destinations", "unfinished_staging", "previously_saved_output", "untracked_partials_resolved"}}
	if m.recoveryCode != "" {
		view.State = "blocked"
	} else {
		n := m.reserved
		view.ReservedBytes = &n
	}
	return view
}

func (m *Manager) ReceiveRecovery() ReceiveRecoveryView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoveryViewLocked(false)
}

// ConfirmReceiveRecovery is local authority only. Review can initialize a
// missing legacy index after the caller has reviewed and resolved untracked
// partials; it cannot discard an existing damaged index or records.
func (m *Manager) ConfirmReceiveRecovery(ctx context.Context, reviewed bool) (ReceiveRecoveryView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.recoveryViewLocked(false), ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return m.recoveryViewLocked(false), err
	}
	if !reviewed || m.recoveryCode == "" {
		return m.recoveryViewLocked(false), nil
	}
	if m.accountingStore == nil {
		return m.recoveryViewLocked(false), ErrReceiveRecovery
	}
	if m.active != 0 || len(m.batches) != 0 {
		return m.recoveryViewLocked(false), ErrBusy
	}
	if m.policySavePending {
		if err := m.savePoliciesLocked(m.policies); err != nil {
			return m.recoveryViewLocked(false), ErrReceiveRecovery
		}
		m.policySavePending = false
		if err := ctx.Err(); err != nil {
			return m.recoveryViewLocked(false), err
		}
	}
	state, err := m.accountingStore.LoadReceiveAccounting()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return m.recoveryViewLocked(false), ctxErr
	}
	initializeLegacy := m.recoveryCode == "legacy_review_required" && errors.Is(err, os.ErrNotExist)
	if initializeLegacy {
		state = ReceiveAccounting{Version: 1}
	} else if err != nil {
		return m.recoveryViewLocked(false), ErrReceiveRecovery
	}
	next, retained, err := inventoryReceive(ctx, state, m.accountingLimits)
	if err != nil {
		return m.recoveryViewLocked(false), ErrReceiveRecovery
	}
	if initializeLegacy || len(next.Roots) != len(state.Roots) {
		if err := ctx.Err(); err != nil {
			return m.recoveryViewLocked(false), err
		}
		if err := m.accountingStore.SaveReceiveAccounting(next); err != nil {
			return m.recoveryViewLocked(false), ErrReceiveRecovery
		}
		if err := ctx.Err(); err != nil {
			return m.recoveryViewLocked(false), err
		}
	}
	if err := ctx.Err(); err != nil {
		return m.recoveryViewLocked(false), err
	}
	m.accounting, m.retained, m.reserved, m.recoveryCode = next, retained, retained, ""
	return m.recoveryViewLocked(true), nil
}

// UpdateAccountingLimits changes future private storage/inventory budgets.
// Existing retained bytes remain reserved, even below a newly lowered quota.
func (m *Manager) UpdateAccountingLimits(next AccountingLimits) error {
	next = accountingLimits(next)
	if next.MaxBytes < 1 || next.MaxEntries < 1 || next.MaxDepth < 1 || next.MaxPathBytes < 1 {
		return ErrLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.accountingLimits = next
	if file, ok := m.accountingStore.(FileReceiveAccountingStore); ok {
		file.Limits = next
		m.accountingStore = file
	}
	return nil
}
