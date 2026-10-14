package core

import (
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type transferAcceptanceError struct{ cause error }

func (e *transferAcceptanceError) ErrorCode() string {
	if e.cause == transfer.ErrAcceptBusy {
		return "transfer_accept_busy"
	}
	return "transfer_accept_unconfirmed"
}
func (e *transferAcceptanceError) Unwrap() error { return e.cause }
func (e *transferAcceptanceError) Error() string {
	if e.cause == transfer.ErrAcceptBusy {
		return "Another local storage operation is busy. This attempt saved no new acceptance; earlier acceptance may exist. Wait briefly, then explicitly accept the same transfer into the same directory."
	}
	return "Acceptance could not be confirmed. Before another action, review the original transfer and the receiver's storage state. Earlier acceptance may exist."
}

func transferAcceptCommandError(err error) error {
	// An arbitrary busy or joined/uncertain error cannot authorize a retry.
	// Preserve every cause and the normal command-cache behavior.
	// These existing exact preflight errors already have safe machine codes.
	// A wrapper/join may carry uncertainty, so it is never whitelisted here.
	if err == diskspace.ErrLow || err == diskspace.ErrUnknown {
		return err
	}
	if err != nil {
		return &transferAcceptanceError{cause: err}
	}
	return err
}
