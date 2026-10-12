package main

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/webkaz-labs/sobalink/internal/core"
)

// Synthetic profiles use the current format and independent durable epochs.
// This constructs fixture metadata only; it does not mutate a user profile.
func syntheticDurableProfile() *core.DurableProfile {
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		panic(err)
	}
	return &core.DurableProfile{Version: 1, Epoch: hex.EncodeToString(epoch[:]), Phase: "initializing"}
}
