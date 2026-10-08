//go:build !endpoint_following_acceptance

package core

// No state or callback exists outside the explicitly opted-in acceptance build.
func observeEndpointAcceptance(*Core, string, error) {}
