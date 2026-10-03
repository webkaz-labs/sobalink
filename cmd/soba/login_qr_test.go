package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

func TestLoginQRUsesLocalPixelsAndRestoresDisplay(t *testing.T) {
	const url = "https://login.tailscale.com/a/fictional"
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	bits := code.Bitmap()
	old := prepareSobaQRDisplay
	t.Cleanup(func() { prepareSobaQRDisplay = old })
	for _, format := range []string{"small", "large"} {
		restored := false
		prepareSobaQRDisplay = func(io.Writer) (func(), error) { return func() { restored = true }, nil }
		var out bytes.Buffer
		if err := renderLoginQR(&out, url, format); err != nil {
			t.Fatal(err)
		}
		if !restored || strings.Contains(out.String(), url) {
			t.Fatal("display was not restored or raw URL escaped the QR")
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		step := 2
		if format == "large" {
			step = 1
		}
		if len(lines) != (len(bits)+step-1)/step {
			t.Fatal("QR height changed")
		}
		for row, line := range lines {
			pixels := []rune(strings.TrimSuffix(strings.TrimPrefix(line, "\x1b[30;47m"), "\x1b[0m"))
			for x, top := range bits[row*step] {
				if step == 1 {
					want := ' '
					if top {
						want = '█'
					}
					if len(pixels) != 2*len(bits) || pixels[x*2] != want || pixels[x*2+1] != want {
						t.Fatal("QR pixel changed")
					}
				} else {
					bottom := row*step+1 < len(bits) && bits[row*step+1][x]
					want := ' '
					switch {
					case top && bottom:
						want = '█'
					case top:
						want = '▀'
					case bottom:
						want = '▄'
					}
					if len(pixels) != len(bits) || pixels[x] != want {
						t.Fatal("QR pixel changed")
					}
				}
			}
		}
	}
}

type failedQRWriter struct{}

func (failedQRWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestLoginQRWidthAndWriteFailureAreRecoverable(t *testing.T) {
	width, display := sobaLoginTerminalWidth, prepareSobaQRDisplay
	t.Cleanup(func() { sobaLoginTerminalWidth = width; prepareSobaQRDisplay = display })
	sobaLoginTerminalWidth = func(io.Writer) (int, error) { return 10, nil }
	var out bytes.Buffer
	if err := renderLoginQR(&out, "https://login.tailscale.com/a/fictional", "small"); err == nil || out.Len() != 0 {
		t.Fatal("narrow terminal received wrapped QR")
	}
	sobaLoginTerminalWidth = func(io.Writer) (int, error) { return 200, nil }
	restored := false
	prepareSobaQRDisplay = func(io.Writer) (func(), error) { return func() { restored = true }, nil }
	if err := renderLoginQR(failedQRWriter{}, "https://login.tailscale.com/a/fictional", "large"); err == nil || !restored {
		t.Fatal("write error did not restore terminal mode")
	}
}
