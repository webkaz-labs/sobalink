//go:build resource_process_native

package main

import (
	"os"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance"
)

func main() { os.Exit(mainExitCode()) }

func mainExitCode() int {
	if !resourceacceptance.BeginProcessEntry() {
		return 72
	}
	return resourceacceptance.FinishProcessEntry(processEntryOrdinaryExitCode())
}

// The actual entry guard precedes every helper, output and signal effect. The
// closed verified argv excludes helpers; no substituted product result is used.
func processEntryOrdinaryExitCode() int {
	if code, ok := runUpgradeHandoffProcess(); ok {
		return code
	}
	if code, ok := runBackendWorkerProcess(); ok {
		return code
	}
	out, errorOut, closeOutput, err := backgroundCommandOutput(os.Args[1:], resourceacceptance.ProcessEntryOutput(), os.Stderr)
	if err != nil {
		writeCommandError(os.Stderr, err)
		return 1
	}
	defer closeOutput()
	ctx := resourceacceptance.ProcessEntrySignalContext()
	if err := run(ctx, os.Args[1:], out); err != nil {
		writeCommandError(errorOut, err)
		return 1
	}
	return 0
}
