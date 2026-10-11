//go:build resource_process_native && !(linux && (amd64 || arm64))

package resourceacceptance

func processEntrySupported() bool                         { return false }
func processEntryStandardFiles() bool                     { return false }
func processEntryVerifyNative(*processEntryVerifier) bool { return false }
