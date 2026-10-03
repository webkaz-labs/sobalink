package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func flushEditorInput(input *os.File) error {
	return unix.IoctlSetPointerInt(int(input.Fd()), unix.TIOCFLUSH, 1)
}
