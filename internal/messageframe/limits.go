// Package messageframe bounds the JSON envelopes around decoded message text.
// These are transport limits, not message-history retention settings.
package messageframe

const (
	TextBytes        = 16 << 10
	IDBytes          = 128
	RequestIDBytes   = 128
	CommandNameBytes = 64

	peerShape    = `{"id":"","text":""}`
	commandShape = `{"requestId":"","name":"","payload":{"peerId":"","text":""}}`
	controlShape = `{"command":""}`

	// A UTF-8 byte needs at most six JSON bytes (for example, & -> \u0026).
	// Include escaped IDs and an optional encoder newline as well.
	PeerRequestBytes = 6*(TextBytes+IDBytes) + len(peerShape) + 1
	CommandBytes     = 6*(TextBytes+IDBytes+RequestIDBytes+CommandNameBytes) + len(commandShape) + 1

	// IPC wraps command JSON in another JSON string. An original byte needs
	// at most seven bytes there (for example, & -> \\u0026). Doubling all
	// structural bytes also covers escaped syntax and the inner newline.
	ControlRequestBytes = 7*(TextBytes+IDBytes+RequestIDBytes+CommandNameBytes) + 2*(len(commandShape)+1) + len(controlShape) + 1
)
