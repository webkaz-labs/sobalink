package main

import "syscall"

func nativePreferredLanguage() string {
	// GetUserDefaultUILanguage is read-only and does not alter system locale.
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	if err := proc.Find(); err != nil {
		return "en"
	}
	language, _, _ := proc.Call()
	if language&0x3ff == 0x11 {
		return "ja"
	}
	return "en"
}
