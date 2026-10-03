package main

import (
	"errors"
	"fmt"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/core"
	"golang.org/x/term"
	"io"
	"os"
	"strings"
)

var sobaLoginTerminalWidth = func(out io.Writer) (int, error) {
	if f, ok := out.(*os.File); ok {
		w, _, err := term.GetSize(int(f.Fd()))
		return w, err
	}
	return 0, nil
}
var prepareSobaQRDisplay = prepareQRDisplay

func renderLoginQR(out io.Writer, url, format string) error {
	if !core.ValidAuthURL(url) {
		return errors.New("unexpected login URL; refusing QR generation")
	}
	q, e := qrcode.New(url, qrcode.Medium)
	if e != nil {
		return errors.New("QR could not be generated; use the private link")
	}
	// Explicit contrast avoids dependence on a light/dark terminal theme. The QR
	// is generated entirely in memory, never sent to an image service or saved.
	bits := q.Bitmap()
	columns := len(bits)
	if format == "large" {
		columns *= 2
	}
	if width, err := sobaLoginTerminalWidth(out); err == nil && width > 0 && columns > width {
		return fmt.Errorf("QR needs %d columns; terminal has %d. Widen it or use the private link", columns, width)
	}
	restore, e := prepareSobaQRDisplay(out)
	if e != nil {
		return e
	}
	defer restore()
	var b strings.Builder
	step := 2
	if format == "large" {
		step = 1
	}
	for y := 0; y < len(bits); y += step {
		b.WriteString("\x1b[30;47m")
		for x := range bits[y] {
			top := bits[y][x]
			if step == 1 {
				if top {
					b.WriteString("██")
				} else {
					b.WriteString("  ")
				}
				continue
			}
			bottom := y+1 < len(bits) && bits[y+1][x]
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	n, e := io.WriteString(out, b.String())
	if e == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return e
}
