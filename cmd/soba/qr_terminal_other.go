//go:build !windows

package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

func prepareQRDisplay(out io.Writer) (func(), error) {
	// Plain terminals cannot honor the explicit contrast the QR depends on.
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) && os.Getenv("TERM") == "dumb" {
		return nil, errors.New("Terminal cannot display QR colors. Use the private link instead.")
	}
	return func() {}, nil
}
