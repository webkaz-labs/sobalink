package directlan

import "github.com/webkaz-labs/sobalink/internal/endpointmeta"

// SignEndpointUpdate is a narrow cryptographic helper, not export authority.
// Core must derive/review the statement and durably save its issued proof before
// releasing bytes. No private key, Node or network operation crosses this seam.
func (i Identity) SignEndpointUpdate(body endpointmeta.UpdateBody) (endpointmeta.Envelope, error) {
	key, err := i.private()
	if err != nil {
		return endpointmeta.Envelope{}, err
	}
	return endpointmeta.Sign(body, key)
}
