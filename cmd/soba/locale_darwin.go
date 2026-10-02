package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func nativePreferredLanguage() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Read-only native preference fallback, used only when locale env is absent.
	data, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return "en"
	}
	return firstAppleLanguage(string(data))
}
func firstAppleLanguage(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "(")
	value = strings.TrimSpace(value)
	if i := strings.IndexAny(value, ",\n)"); i >= 0 {
		value = value[:i]
	}
	return strings.Trim(strings.TrimSpace(value), "\"")
}
