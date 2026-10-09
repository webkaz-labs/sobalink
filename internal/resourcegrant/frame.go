package resourcegrant

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

// ReadInspectRequest reads exactly one bounded frame. Its caller owns the
// authenticated connection and read deadline and closes after one exchange.
// It must not answer or disclose a capability before current grant admission.
func ReadInspectRequest(reader io.Reader) (InspectRequest, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return InspectRequest{}, ErrTransport
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxRequestBytes {
		return InspectRequest{}, ErrInvalid
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return InspectRequest{}, ErrTransport
	}
	return DecodeInspectRequest(payload)
}

// InspectRequestFrame is an encoding helper, never an authentication proof.
func InspectRequestFrame(request InspectRequest) ([]byte, error) {
	if request.Validate() != nil {
		return nil, ErrInvalid
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > MaxRequestBytes {
		return nil, ErrInvalid
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

// InspectionFrame serializes only the allowlisted two-field response. The
// transport performs independent current grant/disclosure admission afterward.
func InspectionFrame(inspection Inspection) ([]byte, error) {
	if inspection.Validate() != nil {
		return nil, ErrInvalid
	}
	payload, err := json.Marshal(inspection)
	if err != nil || len(payload) > MaxResponseBytes {
		return nil, ErrInvalid
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

func ReadInspection(reader io.Reader) (Inspection, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Inspection{}, ErrTransport
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxResponseBytes {
		return Inspection{}, ErrProtocol
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return Inspection{}, ErrTransport
	}
	inspection, err := DecodeInspection(payload)
	if err != nil {
		return Inspection{}, ErrProtocol
	}
	return inspection, nil
}
