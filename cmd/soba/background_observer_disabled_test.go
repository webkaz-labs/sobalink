//go:build !product_activation_native || !web_activation_native || !managed_restart_native || !linux

package main

import (
	"errors"
	"testing"
)

func TestBackgroundExitObserverNormalBuildIsInert(t *testing.T) {
	// Even an opt-in marker cannot enable a seam excluded by the normal build.
	t.Setenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE", "1")
	observeBackgroundExit(-1, nil, errors.New("synthetic unavailable wait status"))
}
