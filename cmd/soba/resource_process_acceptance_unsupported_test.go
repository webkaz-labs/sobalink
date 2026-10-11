//go:build resource_process_native && directlan_activation_native && !(linux && (amd64 || arm64))

package main

import (
	"os"
	"testing"
)

func TestResourceProcessControllerRestartKeepsHistoryNoReplay(t *testing.T) {
	if os.Getenv("SOBALINK_RUN_RESOURCE_PROCESS_NATIVE") == "" {
		t.Skip("unperformed: Linux-only P1 source fixture")
	}
	t.Fatal("P1 native process fixture is unsupported on this target")
}
