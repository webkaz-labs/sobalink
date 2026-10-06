// prepare-engine materializes only the checksum-verified, reviewed dependency.
package main

import (
	"fmt"
	"github.com/webkaz-labs/sobalink/internal/engineadaptation"
	"os"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		_, err = engineadaptation.Prepare(".")
	case len(os.Args) == 2 && os.Args[1] == "--verify":
		_, err = engineadaptation.Verify(".")
	default:
		err = fmt.Errorf("usage: prepare-engine [--verify]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Verified adapted Tailscale source:", engineadaptation.Version, engineadaptation.ManifestSHA256)
}
