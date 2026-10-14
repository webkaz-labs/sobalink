//go:build !linux && !darwin && !windows

package nearby

func nativeSendErrorKind(error) string { return "other" }

func nativeBindErrorKind(error) string { return "other" }
