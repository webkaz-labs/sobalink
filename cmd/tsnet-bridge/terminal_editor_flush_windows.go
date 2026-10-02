package main

import (
	"golang.org/x/sys/windows"
	"os"
)

func flushEditorInput(input *os.File) error {
	return windows.FlushConsoleInputBuffer(windows.Handle(input.Fd()))
}
