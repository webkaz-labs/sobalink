//go:build !resource_process_native

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	if code, ok := runUpgradeHandoffProcess(); ok {
		return code
	}
	if code, ok := runBackendWorkerProcess(); ok {
		return code
	}
	out, errorOut, closeOutput, err := backgroundCommandOutput(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		writeCommandError(os.Stderr, err)
		return 1
	}
	defer closeOutput()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], out); e != nil {
		writeCommandError(errorOut, e)
		return 1
	}
	return 0
}
