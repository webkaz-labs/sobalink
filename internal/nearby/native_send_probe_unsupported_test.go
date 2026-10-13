//go:build !windows

package nearby

import "time"

func runPlatformSendProbe(Scope, time.Time) *nativeSendProbeResult { return nil }
