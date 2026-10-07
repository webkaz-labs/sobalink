package main

import (
	"bytes"
	"context"
	"errors"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/sobalink/internal/webui"
	"io"
	"strings"
	"testing"
)

func TestDeviceCardCLIQRMatchesPublicBitmapAndRestoresTerminal(t *testing.T) {
	_, encoded := fictionalDeviceCard(t, "lan")
	code, err := qrcode.New(encoded, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	bits := code.Bitmap()
	oldWidth, oldPrepare := sobaLoginTerminalWidth, prepareSobaQRDisplay
	t.Cleanup(func() { sobaLoginTerminalWidth, prepareSobaQRDisplay = oldWidth, oldPrepare })
	sobaLoginTerminalWidth = func(io.Writer) (int, error) { return len(bits), nil }
	restored := false
	prepareSobaQRDisplay = func(io.Writer) (func(), error) { return func() { restored = true }, nil }
	var out bytes.Buffer
	if err := writeDeviceCardQR(&out, bits, false); err != nil {
		t.Fatal(err)
	}
	if !restored || strings.Contains(out.String(), encoded) {
		t.Fatal("restore or bitmap output failed")
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != (len(bits)+1)/2 {
		t.Fatal("QR height changed")
	}
	for y, line := range lines {
		pixels := []rune(strings.TrimSuffix(strings.TrimPrefix(line, "\x1b[30;47m"), "\x1b[0m"))
		if len(pixels) != len(bits) {
			t.Fatal("QR width changed")
		}
		for x, pixel := range pixels {
			top, bottom := bits[2*y][x], 2*y+1 < len(bits) && bits[2*y+1][x]
			want := ' '
			switch {
			case top && bottom:
				want = '█'
			case top:
				want = '▀'
			case bottom:
				want = '▄'
			}
			if pixel != want {
				t.Fatal("QR content changed")
			}
		}
	}
	restored = false
	if err := writeDeviceCardQR(failedQRWriter{}, bits, false); err == nil || !restored {
		t.Fatal("write error lost restore")
	}
}

func TestDeviceCardCLIQRTextFallbackAndJSON(t *testing.T) {
	oldWidth, oldPrepare := sobaLoginTerminalWidth, prepareSobaQRDisplay
	t.Cleanup(func() { sobaLoginTerminalWidth, prepareSobaQRDisplay = oldWidth, oldPrepare })
	for _, reason := range []string{"narrow", "width unavailable", "unsupported terminal"} {
		for _, ja := range []bool{false, true} {
			sobaLoginTerminalWidth = func(io.Writer) (int, error) {
				if reason == "narrow" {
					return 10, nil
				}
				if reason == "width unavailable" {
					return 0, errors.New("fictional-private-marker")
				}
				return 200, nil
			}
			prepareSobaQRDisplay = func(io.Writer) (func(), error) { return nil, errors.New("fictional-private-marker") }
			locale := "en"
			if ja {
				locale = "ja"
			}
			var calls []webui.Command
			var out bytes.Buffer
			if err := runWith(context.Background(), []string{"--locale", locale, "card", "export", "--mode", "lan", "--name", "Alias", "--qr"}, &out, nil, cardFixtureClient(t, &calls)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "soba-card1.") || strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "fictional-private-marker") {
				t.Fatal("fallback lost text or leaked terminal error")
			}
			want := "QR is not displayed"
			if ja {
				want = "QRを表示できません"
			}
			if !strings.Contains(out.String(), want) {
				t.Fatal("missing localized fallback")
			}
		}
	}
	prepareSobaQRDisplay = func(io.Writer) (func(), error) { t.Fatal("JSON tried terminal mode"); return nil, nil }
	var calls []webui.Command
	if err := runWith(context.Background(), []string{"card", "export", "--mode", "lan", "--name", "Alias", "--qr", "--json"}, io.Discard, nil, cardFixtureClient(t, &calls)); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCardCLIQRRejectsMalformedOrOversizedBitmaps(t *testing.T) {
	for _, bits := range [][][]bool{nil, {{true}}, make([][]bool, 186), make([][]bool, 29)} {
		if validDeviceCardBitmap(bits) {
			t.Fatal("unbounded or non-square QR accepted")
		}
	}
}
