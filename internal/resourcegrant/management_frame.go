package resourcegrant

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

// These frame helpers consume one bounded message only. The authenticated
// transport owns deadlines, admission, matching the reply to the request and
// closing after one exchange. A frame is never an authorization proof.
func ManagementRequestFrame(request ManagementRequest) ([]byte, error) {
	if request.Validate() != nil {
		return nil, ErrInvalid
	}
	return managementFrame(request, MaxManagementRequestBytes, ErrInvalid)
}
func ManagementReplyFrame(reply ManagementReply) ([]byte, error) {
	if reply.Validate() != nil {
		return nil, ErrProtocol
	}
	return managementFrame(reply, MaxManagementResponseBytes, ErrProtocol)
}
func ReadManagementRequest(reader io.Reader) (ManagementRequest, error) {
	payload, err := readManagementPayload(reader, MaxManagementRequestBytes, ErrInvalid)
	if err != nil {
		return ManagementRequest{}, err
	}
	return DecodeManagementRequest(payload)
}
func ReadManagementReply(reader io.Reader) (ManagementReply, error) {
	payload, err := readManagementPayload(reader, MaxManagementResponseBytes, ErrProtocol)
	if err != nil {
		return ManagementReply{}, err
	}
	return DecodeManagementReply(payload)
}
func managementFrame(value any, limit int, invalid error) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 || len(payload) > limit {
		return nil, invalid
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}
func readManagementPayload(reader io.Reader, limit uint32, invalid error) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, ErrTransport
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > limit {
		return nil, invalid
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, ErrTransport
	}
	return payload, nil
}
