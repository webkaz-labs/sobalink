//go:build !linux

package main

import "io"

// Windows console input is Unicode through os.File. Other systems retain their
// existing terminal configuration; piped input is always decoded as UTF-8.
func preparePromptInput(io.Reader) (func(), error) { return func() {}, nil }
