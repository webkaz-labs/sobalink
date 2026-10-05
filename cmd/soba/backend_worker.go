package main

import (
	"context"
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/backendworker"
	"github.com/webkaz-labs/sobalink/internal/core"
	"os"
)

// The internal mode uses inherited anonymous owner pipes only. It never exposes
// a TCP/HTTP administration endpoint or receives authentication keys in argv.
func runBackendWorkerProcess() (int, bool) {
	if len(os.Args) < 2 || os.Args[1] != "__network-worker" {
		return 0, false
	}
	if len(os.Args) != 6 {
		return 2, true
	}
	in, e := os.Stdin.Stat()
	if e != nil || in.Mode()&os.ModeNamedPipe == 0 {
		return 2, true
	}
	out, e := os.Stdout.Stat()
	if e != nil || out.Mode()&os.ModeNamedPipe == 0 {
		return 2, true
	}
	var limits backendworker.Limits
	if json.Unmarshal([]byte(os.Args[5]), &limits) != nil || limits.Validate() != nil {
		return 2, true
	}
	if e = core.RunNetworkWorker(context.Background(), os.Args[3], os.Args[2], os.Args[4], os.Stdin, os.Stdout, limits); e != nil {
		return 1, true
	}
	return 0, true
}
