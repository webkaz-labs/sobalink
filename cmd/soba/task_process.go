package main

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// argv is never interpreted by a shell. Readers and writers are passed through
// untouched so task input and output are not consumed by CLI presentation.
func runWorkflowProcess(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin, command.Stdout, command.Stderr = in, out, out
	command.WaitDelay = 2 * time.Second
	configureWorkflowProcess(command)
	return command.Run()
}
