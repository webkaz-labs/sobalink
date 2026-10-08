//go:build product_activation_native && web_activation_native && managed_restart_native && linux

package main

import "os"

// Test-only observation, not a registration/authority setter. No code, token,
// receipt or Core pointer crosses this seam. Ordinary builds compile a no-op.
var observeForegroundWebURL func(string)

func observeForegroundWeb(url string) {
	if os.Getenv("SOBA_PRODUCT_ACTIVATION_ACCEPTANCE") == "1" && observeForegroundWebURL != nil {
		observeForegroundWebURL(url)
	}
}
