package resourcegrant

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestInspectFrameBoundsAndTruncation(t *testing.T) {
	record := fixtureRecord()
	request := InspectRequest{ProtocolVersion: ProtocolVersion, Target: record.Target, GrantID: record.ID, GrantRevision: record.Revision}
	frame, err := InspectRequestFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadInspectRequest(bytes.NewReader(frame))
	if err != nil || got != request {
		t.Fatal("request frame did not round trip")
	}
	for _, length := range []int{0, 1, 3, 4, len(frame) - 1} {
		if _, err := ReadInspectRequest(bytes.NewReader(frame[:length])); err == nil {
			t.Fatal("truncated frame accepted")
		}
	}
	for _, size := range []uint32{0, MaxRequestBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := ReadInspectRequest(bytes.NewReader(header[:])); err == nil {
			t.Fatal("invalid length accepted")
		}
	}
	request.ProtocolVersion++
	if _, err := InspectRequestFrame(request); err == nil {
		t.Fatal("unsupported request encoded")
	}
}
