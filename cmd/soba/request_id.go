package main

import "crypto/rand"

// Request identifiers must remain distinct when a platform clock has coarse
// resolution, and across independent CLI processes. They are idempotency labels,
// not timestamps; caller retries retain the already constructed command.
func newCLIRequestID(prefix string) string {
	return prefix + "-" + rand.Text()
}
