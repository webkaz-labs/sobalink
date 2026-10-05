// Package directlan provides an explicitly selected, authenticated relayless
// LAN application tunnel. It opens only a numeric, unprivileged LAN listener
// and TCP pairing/UDP WireGuard connections to pinned peers inside the selected
// prefixes.
// There is no DNS, discovery, relay, internet fallback, kernel TUN, routing change,
// firewall management, or arbitrary OS application dial. Service streams are
// delivered to explicit userspace listeners or the supplied scoped dispatcher.
//
// Pairing uses TLS 1.3 with mutual Ed25519 proof of possession to bind separate
// WireGuard keys and endpoints. The public signing key, never a name or IP, is
// the identity. All application TCP and native UDP use WireGuard and an embedded
// gVisor userspace stack. No application payload is sent through TLS/TCP and no
// operating-system application socket, resolver or fallback is available.
//
// Identity and Peer records belong in the caller's protected state store.
// Pairing/revocation use a supplied atomic persistence callback; this package
// writes no files. Invitations are short-lived, recipient-bound, one-use secret
// capabilities intended only for the chosen private QR/text exchange channel.
package directlan
