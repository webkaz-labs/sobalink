//go:build !windows && !darwin

package main

func nativePreferredLanguage() string { return "en" }
