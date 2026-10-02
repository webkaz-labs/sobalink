//go:build !linux && !darwin && !windows

package main

import "os"

func flushEditorInput(*os.File) error { return nil }
