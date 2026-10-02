package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func prepareQRDisplay(out io.Writer) (func(), error) {
	f, ok := unwrapLocaleWriter(out).(*os.File)
	if !ok {
		return func() {}, nil
	}
	handle := windows.Handle(f.Fd())
	return prepareQRConsole(
		func(mode *uint32) error { return windows.GetConsoleMode(handle, mode) },
		func(mode uint32) error { return windows.SetConsoleMode(handle, mode) },
	)
}

func prepareQRConsole(read func(*uint32) error, write func(uint32) error) (func(), error) {
	var original uint32
	if read(&original) != nil {
		return nil, errors.New("Terminal cannot display QR colors. Use the private link instead.")
	}
	mode := original | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if mode == original {
		return func() {}, nil
	}
	if write(mode) != nil {
		return nil, errors.New("Terminal cannot display QR colors. Use the private link instead.")
	}
	// Scope ANSI interpretation to the QR. Do not alter code pages: os.File
	// already uses the Unicode console APIs for real Windows console handles.
	return func() { _ = write(original) }, nil
}
