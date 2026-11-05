//go:build !product_activation_native || !web_activation_native || !managed_restart_native || !linux

package main

import (
	"io"
	"os"
)

func observeForegroundOwnersClose(error, error, io.Closer) {}

func observeForegroundWeb(string) {}

func observeForegroundExit(error, error, error, error) {}

func observeBackgroundExit(int, *os.ProcessState, error) {}
