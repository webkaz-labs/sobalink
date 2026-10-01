// package-tool only prepares local artifacts; it never publishes a release.
package main

import (
	"fmt"
	"github.com/webkaz-labs/tsnet-bridge/internal/distribution"
	"os"
)

func main() {
	tool, err := distribution.New(".")
	if err == nil {
		err = tool.Run(os.Args[1:], os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
