package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func preparePromptInput(in io.Reader) (func(), error) {
	f, ok := unwrapProcessInput(in).(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}, nil
	}
	fd := int(f.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("could not prepare UTF-8 terminal input: %w", err)
	}
	// Linux canonical erase otherwise removes a byte, not a complete UTF-8
	// character. Keep the terminal's normal line editing and signal handling.
	if original.Lflag&unix.ICANON == 0 || original.Iflag&unix.IUTF8 != 0 {
		return func() {}, nil
	}
	updated := *original
	updated.Iflag |= unix.IUTF8
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, &updated); err != nil {
		return nil, fmt.Errorf("could not prepare UTF-8 terminal input: %w", err)
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, original) }, nil
}
