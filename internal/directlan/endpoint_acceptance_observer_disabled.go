//go:build !endpoint_following_acceptance

package directlan

// Compiles away outside the explicit acceptance build. It never changes state.
func observeAcceptanceSessionBirth(*peerState) {}
