package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func flushEditorInput(input *os.File) error {
	return unix.IoctlSetInt(int(input.Fd()), unix.TCFLSH, unix.TCIFLUSH)
}
