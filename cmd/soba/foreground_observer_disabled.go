//go:build !product_activation_native || !web_activation_native || !managed_restart_native || !linux

package main

func observeForegroundWeb(string) {}
