package main

import "os/exec"

// CommandContext terminates the direct child on cancellation. A child that
// submits work elsewhere still needs that application's cancellation API.
func configureWorkflowProcess(command *exec.Cmd) {}
