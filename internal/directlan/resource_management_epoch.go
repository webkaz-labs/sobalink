package directlan

import "github.com/webkaz-labs/sobalink/internal/resource"

// managementEpochSlot is one bounded current slot, not a history. Its owner
// is the existing peerState under Node.mu. It neither authenticates the
// tuple nor authorizes a request.
// Pointers are compared only in memory; none are formatted or hashed.
type managementEpochSlot struct {
	identity managedAuthentication
	digest   string
}

// selectManagementEpoch is pure bookkeeping. The concrete transport must
// first verify current authentication and supply a fresh random candidate made
// outside its locks, then recheck the selected slot at final admission. Repeated
// TCP captures of the same exact tuple retain the existing digest. Replacing any
// tuple component replaces the only slot; no previous epoch remains accessible.
func selectManagementEpoch(existing managementEpochSlot, identity managedAuthentication, candidate string) (managementEpochSlot, bool) {
	if identity.peer == nil || identity.generation == nil || identity.policy == nil || identity.registration == 0 || !resource.ValidDigest(identity.binding) {
		return managementEpochSlot{}, false
	}
	if existing.identity == identity && resource.ValidDigest(existing.digest) {
		return existing, true
	}
	if !resource.ValidDigest(candidate) || candidate == existing.digest {
		return managementEpochSlot{}, false
	}
	return managementEpochSlot{identity: identity, digest: candidate}, true
}
