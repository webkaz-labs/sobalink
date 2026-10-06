package lanlink

import (
	"github.com/webkaz-labs/sobalink/internal/lanpolicy"
	"net/netip"
)

// nil is only the validated legacy/trusted mode. A malformed strict policy
// produces an empty non-nil slice, which the engine rejects before setup.
func destinationPrefixes(policy lanpolicy.Config) []netip.Prefix {
	if !policy.Strict() {
		return nil
	}
	prefixes, err := policy.Parsed()
	if err != nil {
		return []netip.Prefix{}
	}
	return prefixes
}
