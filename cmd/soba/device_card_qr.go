package main

import (
	"fmt"
	"io"
	"strings"
)

// Card QR rendering deliberately does not call renderLoginQR, whose URL
// validation must remain exclusive to authentication links.
func validDeviceCardBitmap(bits [][]bool) bool {
	// QR versions 1–40 plus the encoder's four-module quiet zone per side.
	if len(bits) < 29 || len(bits) > 185 || (len(bits)-29)%4 != 0 {
		return false
	}
	for _, row := range bits {
		if len(row) != len(bits) {
			return false
		}
	}
	return true
}

func writeDeviceCardQR(out io.Writer, bits [][]bool, ja bool) error {
	fallback := func() error {
		_, err := fmt.Fprintln(out, text(ja, "QR is not displayed: widen the terminal or use the card text above.", "QRを表示できません。端末の幅を広げるか、上のカード文字列を使ってください。"))
		return err
	}
	if !validDeviceCardBitmap(bits) {
		return fallback()
	}
	if width, err := sobaLoginTerminalWidth(out); err != nil || (width > 0 && len(bits) > width) {
		return fallback()
	}
	restore, err := prepareSobaQRDisplay(out)
	if err != nil {
		return fallback()
	}
	defer restore()
	var b strings.Builder
	for y := 0; y < len(bits); y += 2 {
		b.WriteString("\x1b[30;47m")
		for x, top := range bits[y] {
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
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
