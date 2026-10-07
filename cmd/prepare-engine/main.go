// prepare-engine materializes the exact checksum-verified dependency set.
package main

import (
	"fmt"
	"os"

	"github.com/webkaz-labs/sobalink/internal/lifecycleadaptation"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = lifecycleadaptation.PrepareAll(".")
	case len(os.Args) == 2 && os.Args[1] == "--verify":
		err = lifecycleadaptation.VerifyAll(".")
	default:
		err = fmt.Errorf("usage: prepare-engine [--verify]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Verified pinned Tailscale, WireGuard and gVisor source adaptations")
}
