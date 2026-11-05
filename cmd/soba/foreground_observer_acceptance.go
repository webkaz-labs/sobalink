//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import (
	"github.com/webkaz-labs/sobalink/internal/core"
	"io"
	"os"
)

// Only fixed booleans and closed categories cross this test-only seam. The
// original errors remain in shutdownManagedOwners and its returned join.
var observeForegroundOwnersCloseResult func(ipcFailed, coreFailed bool, ipcClass, coreFirstComponent, coreFirstClass string)

func observeForegroundOwnersClose(ipcErr, coreErr error, app io.Closer) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") != "1" || observeForegroundOwnersCloseResult == nil {
		return
	}
	component, class := "none", "none"
	if coreErr != nil {
		component, class = "other", core.ProductCloseErrorClass(coreErr)
		if owner, ok := app.(*core.Core); ok && owner != nil {
			component, class = owner.ProductCloseFailure()
		}
	}
	observeForegroundOwnersCloseResult(ipcErr != nil, coreErr != nil, core.ProductCloseErrorClass(ipcErr), component, class)
}

// Only booleans cross this test-only seam. The original component errors and
// joined return value stay with the foreground owner; no error is formatted.
var observeForegroundExitResult func(body, ownersClose, lockClose, acknowledgement bool)

func observeForegroundExit(body, ownersClose, lockClose, acknowledgement error) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") == "1" && observeForegroundExitResult != nil {
		observeForegroundExitResult(body != nil, ownersClose != nil, lockClose != nil, acknowledgement != nil)
	}
}

// Test-only observation, not a registration/authority setter. No code, token,
// receipt or Core pointer crosses this seam. Ordinary builds compile a no-op.
var observeForegroundWebURL func(string)

func observeForegroundWeb(url string) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") == "1" && observeForegroundWebURL != nil {
		observeForegroundWebURL(url)
	}
}

// Installed once by the explicitly opted-in fixture before it launches children.
// This reports an actual completed parent wait, never a readiness observation.
var observeBackgroundProcessExit func(int, *os.ProcessState, error)

func observeBackgroundExit(pid int, state *os.ProcessState, err error) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") == "1" && observeBackgroundProcessExit != nil {
		observeBackgroundProcessExit(pid, state, err)
	}
}
