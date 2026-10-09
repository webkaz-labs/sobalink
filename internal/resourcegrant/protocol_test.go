package resourcegrant

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func TestInspectionHelloVersionNegotiation(t *testing.T) {
	request := HelloRequest()
	version, err := ReadHelloRequest(bytes.NewReader(request[:]))
	if err != nil || version != ProtocolVersion {
		t.Fatal("valid hello rejected")
	}
	response, supported, err := HelloResponse(version)
	if err != nil || !supported || len(response) != HelloBytes || ReadHelloResponse(bytes.NewReader(response[:])) != nil {
		t.Fatal("supported negotiation failed")
	}
	request[5] = 2
	version, err = ReadHelloRequest(bytes.NewReader(request[:]))
	if err != nil {
		t.Fatal("well-formed other version rejected before negotiation")
	}
	response, supported, err = HelloResponse(version)
	if err != nil || supported || !errors.Is(ReadHelloResponse(bytes.NewReader(response[:])), ErrUnsupported) {
		t.Fatal("explicit unsupported response lost")
	}
	if _, _, err := HelloResponse(0); !errors.Is(err, ErrProtocol) {
		t.Fatal("zero version accepted")
	}
}
func TestInspectionHelloRejectsAmbiguousProtocol(t *testing.T) {
	for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		request := HelloRequest()
		if index == 5 {
			request[index] = 0
		} else {
			request[index] ^= 0x80
		}
		if _, err := ReadHelloRequest(bytes.NewReader(request[:])); !errors.Is(err, ErrProtocol) {
			t.Fatal("ambiguous request accepted")
		}
	}
	for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		response, _, _ := HelloResponse(ProtocolVersion)
		response[index] ^= 0x80
		if err := ReadHelloResponse(bytes.NewReader(response[:])); !errors.Is(err, ErrProtocol) {
			t.Fatal("ambiguous reply accepted or inferred unsupported")
		}
	}
}
func TestInspectionHelloTruncationAndSelectorBoundary(t *testing.T) {
	request := HelloRequest()
	response, _, _ := HelloResponse(ProtocolVersion)
	for n := 0; n < HelloBytes; n++ {
		if _, err := ReadHelloRequest(bytes.NewReader(request[:n])); !errors.Is(err, ErrTransport) {
			t.Fatal("truncated request misclassified")
		}
		if err := ReadHelloResponse(bytes.NewReader(response[:n])); !errors.Is(err, ErrTransport) {
			t.Fatal("truncated reply misclassified")
		}
	}
	suffix := []byte("synthetic-selector-bytes")
	for _, valid := range []bool{false, true} {
		input := request
		if !valid {
			input[0] = 'X'
		}
		reader := bytes.NewReader(append(input[:], suffix...))
		_, err := ReadHelloRequest(reader)
		if (err == nil) != valid || reader.Len() != len(suffix) {
			t.Fatal("hello consumed resource/grant selectors")
		}
	}
}
func TestInspectionFrameResponseBounds(t *testing.T) {
	record := fixtureRecord()
	expected := Inspection{ProtocolVersion: ProtocolVersion, Target: record.Target, Requested: resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(2)}, Effective: resource.Effective{TransferConcurrentFiles: 4, TransferConcurrentPerPeer: 2}}
	frame, err := InspectionFrame(expected)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := ReadInspection(bytes.NewReader(frame))
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("inspection frame round trip failed")
	}
	for _, size := range []uint32{0, MaxResponseBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := ReadInspection(bytes.NewReader(header[:])); !errors.Is(err, ErrProtocol) {
			t.Fatal("invalid response length accepted")
		}
	}
	if _, err := ReadInspection(bytes.NewReader(frame[:len(frame)-1])); !errors.Is(err, ErrTransport) {
		t.Fatal("truncated response accepted")
	}
	expected.Effective.TransferConcurrentFiles = 0
	if _, err := InspectionFrame(expected); err == nil {
		t.Fatal("invalid response encoded")
	}
}

type failingHelloReader struct{}

func (failingHelloReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-private-transport-detail")
}
func TestInspectionHelloRedactsTransportFailures(t *testing.T) {
	if _, err := ReadHelloRequest(failingHelloReader{}); err != ErrTransport {
		t.Fatal("request read exposed underlying error")
	}
	if err := ReadHelloResponse(failingHelloReader{}); err != ErrTransport {
		t.Fatal("response read exposed underlying error")
	}
}
