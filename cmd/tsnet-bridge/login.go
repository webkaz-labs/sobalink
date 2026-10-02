package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"golang.org/x/term"
)

type loginOptions struct {
	NoBrowser, QR bool
	QRFormat      string
	Timeout       time.Duration
}

var launchLoginBrowser = openBrowser
var prepareLoginQRDisplay = prepareQRDisplay

func parseLoginOptions(args []string, out io.Writer) (loginOptions, error) {
	opts := loginOptions{Timeout: 5 * time.Minute, QRFormat: "small"}
	f := flags("login", out)
	f.BoolVar(&opts.NoBrowser, "no-browser", false, "show a private link instead of opening a browser")
	link := f.Bool("link", false, "show a private copyable link only")
	f.BoolVar(&opts.QR, "qr", false, "show a locally generated private QR for a trusted phone; no local browser")
	f.StringVar(&opts.QRFormat, "qr-format", "small", "small or large terminal QR")
	f.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "maximum local wait; does not set the server link lifetime")
	if e := f.Parse(args); e != nil {
		return opts, e
	}
	if f.NArg() != 0 {
		return opts, errors.New("usage: login [--qr|--link|--no-browser] [--qr-format small|large] [--timeout 5m]")
	}
	if opts.QRFormat != "small" && opts.QRFormat != "large" {
		return opts, errors.New("qr-format must be small or large")
	}
	if opts.Timeout <= 0 || opts.Timeout > 30*time.Minute {
		return opts, errors.New("login timeout must be positive and at most 30m")
	}
	opts.NoBrowser = opts.NoBrowser || *link || opts.QR
	return opts, nil
}
func privateTerminal(out io.Writer) bool {
	out = unwrapLocaleWriter(out)
	if f, ok := out.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return true
}
func checkPrivateTerminal(out io.Writer) error {
	if !privateTerminal(out) {
		return errors.New("QR sign-in must be shown in a private terminal, not redirected to a file or pipe; use --link deliberately if needed")
	}
	return nil
}

var loginTerminalWidth = func(out io.Writer) (int, error) {
	out = unwrapLocaleWriter(out)
	if f, ok := out.(*os.File); ok {
		width, _, err := term.GetSize(int(f.Fd()))
		return width, err
	}
	return 0, nil
}

func renderLoginQR(out io.Writer, url, format string) error {
	if !validAuthURL(url) {
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
	if width, err := loginTerminalWidth(out); err == nil && width > 0 && columns > width {
		return fmt.Errorf("QR needs %d columns; terminal has %d. Widen it or use the private link", columns, width)
	}
	restore, e := prepareLoginQRDisplay(out)
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
func loginWithOptions(ctx context.Context, dir string, out io.Writer, opts loginOptions) error {
	if opts.QR {
		if e := checkPrivateTerminal(out); e != nil {
			return e
		}
	}
	displayPrivate := opts.NoBrowser || privateTerminal(out)
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	var s app.Status
	if e := call(ctx, dir, "status", &s); e != nil {
		return e
	}
	if s.Backend == "Running" {
		fmt.Fprintln(out, "Sign-in complete. This node is already connected.")
		return printStatus(out, s, false, dir)
	}
	if s.State == "approval-required" || s.Backend == "NeedsMachineAuth" {
		fmt.Fprintln(out, "Sign-in is already complete; this node needs approval in the tailnet admin console.")
		return printStatus(out, s, false, dir)
	}
	if e := call(ctx, dir, "login", nil); e != nil {
		return e
	}
	fmt.Fprintln(out, "Waiting for sign-in. Choose the intended account and tailnet. Ctrl+C stops this wait, not the running node.")
	fmt.Fprintln(out, "Browser: login  |  Trusted phone: login --qr  |  Private link: login --link")
	last := ""
	lastState := ""
	cleared := false
	for {
		if e := ctx.Err(); e != nil {
			return loginWaitError(e)
		}
		var auth struct {
			URL string `json:"url"`
		}
		if e := call(ctx, dir, "auth-url", &auth); e != nil {
			if ctx.Err() != nil {
				return loginWaitError(ctx.Err())
			}
			return e
		}
		if auth.URL != "" && auth.URL != last {
			if !validAuthURL(auth.URL) {
				return errors.New("unexpected login URL; refusing to open or encode it")
			}
			if last != "" {
				fmt.Fprintln(out, "The sign-in link changed. Use only the new link/QR below.")
			}
			last = auth.URL
			cleared = false
			fmt.Fprintln(out, "Private sign-in link and QR authorize this node. Do not share, screenshot or record this terminal.")
			if displayPrivate {
				fmt.Fprintln(out, "Private sign-in URL (do not share):", auth.URL)
			} else {
				fmt.Fprintln(out, "Private sign-in link omitted from redirected output. Use a private terminal or deliberately choose login --link.")
			}
			if opts.QR {
				fmt.Fprintln(out, "Scan with a trusted phone, then confirm the intended account, tailnet and node in its browser.")
				if e := renderLoginQR(out, auth.URL, opts.QRFormat); e != nil {
					fmt.Fprintln(out, e)
					fmt.Fprintln(out, "Use the private link above.")
				}
			}
			if !opts.NoBrowser {
				if e := launchLoginBrowser(ctx, auth.URL); e != nil {
					fmt.Fprintln(out, "Browser could not be opened. Open the private link, or retry login --qr on a private terminal.")
				}
			}
		} else if auth.URL == "" && last != "" && !cleared {
			fmt.Fprintln(out, "The sign-in link is no longer available. Checking completion; if still waiting, rerun login to request the current sign-in link.")
			cleared = true
		}
		if e := call(ctx, dir, "status", &s); e != nil {
			if ctx.Err() != nil {
				return loginWaitError(ctx.Err())
			}
			return e
		}
		if s.Backend == "Running" {
			fmt.Fprintln(out, "Sign-in complete. This bridge node is connected; application checks are separate.")
			return printStatus(out, s, false, dir)
		}
		if s.State == "approval-required" || s.Backend == "NeedsMachineAuth" {
			fmt.Fprintln(out, "Account sign-in finished; this node still needs approval in the tailnet admin console.")
			return printStatus(out, s, false, dir)
		}
		if s.State != "" && s.State != lastState {
			fmt.Fprintln(out, "Sign-in status:", s.State)
			lastState = s.State
		}
		if e := pause(ctx, time.Second); e != nil {
			return loginWaitError(e)
		}
	}
}
func loginWaitError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("sign-in wait canceled; the node may still be running. Use stop to stop it. This does not revoke an already displayed link")
	}
	return errors.New("sign-in wait timed out; the link's server expiry is not known. If it expired, rerun login to request the current sign-in link; use stop to stop the node")
}
