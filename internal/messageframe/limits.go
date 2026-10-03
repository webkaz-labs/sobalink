// Package messageframe bounds the JSON envelopes around decoded message text.
// These are transport limits, not message-history retention settings.
package messageframe

import (
	"errors"
	"math"
)

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

// Bounds preserves the original envelopes while allowing a selected finite
// decoded-text budget. One spare byte is retained for oversize detection.
type Bounds struct {
	PeerRequestBytes, CommandBytes, ControlRequestBytes int64
}

// EncodedBytes checks multiplication and framing overhead before constructing
// a JSON byte budget. It never turns an overflowing limit into an unbounded read.
func EncodedBytes(decoded, expansion, overhead int64) (int64, error) {
	if decoded < 0 || expansion < 1 || overhead < 0 || overhead >= math.MaxInt64 || decoded > (math.MaxInt64-1-overhead)/expansion {
		return 0, errors.New("JSON envelope budget is outside the finite byte range")
	}
	return decoded*expansion + overhead, nil
}

func ForText(textBytes int64) (Bounds, error) {
	if textBytes < 1 || textBytes > math.MaxInt64-IDBytes-RequestIDBytes-CommandNameBytes {
		return Bounds{}, errors.New("message text budget must be a finite positive byte count")
	}
	peer, err := EncodedBytes(textBytes+IDBytes, 6, int64(len(peerShape)+1))
	if err != nil {
		return Bounds{}, err
	}
	command, err := EncodedBytes(textBytes+IDBytes+RequestIDBytes+CommandNameBytes, 6, int64(len(commandShape)+1))
	if err != nil {
		return Bounds{}, err
	}
	control, err := EncodedBytes(textBytes+IDBytes+RequestIDBytes+CommandNameBytes, 7, int64(2*(len(commandShape)+1)+len(controlShape)+1))
	if err != nil {
		return Bounds{}, err
	}
	return Bounds{peer, command, control}, nil
}
