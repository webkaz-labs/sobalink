# Synthetic metadata vectors

`pair.json` and `update.json` contain canonical JSON without a final newline.
`vector.json` records the expected context, scope and envelope SHA-256 digests
and Ed25519 signature.

The fixed Ed25519 seeds are the public RFC 8032 examples beginning `9d61b19d`
and `4ccd089b`. Tunnel keys are X25519 public keys from 32 bytes of `01` and
`02`; pairing nonces are 32 bytes of `03` and `04`. Addresses are synthetic
loopback endpoints. The vectors were calculated separately with Python's
standard JSON/hash/base64 libraries and cryptography 50.0.0, then checked by
the Go implementation. These fixtures contain no production identity or data.

The tests cover metadata encodings and signatures only. They establish no
network, pairing, migration, transport or application behavior.
