//go:build !product_activation_native || !web_activation_native || !managed_restart_native || !linux

package main

import "os"

func observeForegroundWeb(string) {}

func observeForegroundExit(error, error, error, error) {}

func observeBackgroundExit(int, *os.ProcessState, error) {}
