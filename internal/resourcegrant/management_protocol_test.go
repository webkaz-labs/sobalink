package resourcegrant

import (
	"bytes"
	"errors"
	"testing"
)

func TestManagementHelloCompatibility(t *testing.T) {
	request := ManagementHelloRequest()
	version, err := ReadHelloRequest(bytes.NewReader(request[:]))
	if err != nil || version != ManagementProtocolVersion || len(request) != 8 {
		t.Fatal("management request is not a bounded compatible hello")
	}
	response, supported, err := ManagementHelloResponse(version)
	if err != nil || !supported || ReadManagementHelloResponse(bytes.NewReader(response[:])) != nil {
		t.Fatal("management negotiation failed")
	}
	old, oldSupported, oldErr := HelloResponse(ProtocolVersion)
	compat, compatSupported, compatErr := ManagementHelloResponse(ProtocolVersion)
	if old != compat || oldSupported != compatSupported || oldErr != compatErr {
		t.Fatal("v1 response bytes changed")
	}
	if ReadManagementHelloResponse(bytes.NewReader(old[:])) != ErrProtocol {
		t.Fatal("write client accepted inspection-only success")
	}
	oldReject, supported, err := HelloResponse(ManagementProtocolVersion)
	if err != nil || supported || ReadManagementHelloResponse(bytes.NewReader(oldReject[:])) != ErrUnsupported {
		t.Fatal("old listener did not explicitly reject management")
	}
	for _, version := range []byte{3, 255} {
		response, supported, err := ManagementHelloResponse(version)
		if err != nil || supported || ReadManagementHelloResponse(bytes.NewReader(response[:])) != ErrUnsupported {
			t.Fatal("unknown version accepted")
		}
	}
	if _, _, err := ManagementHelloResponse(0); err != ErrProtocol {
		t.Fatal("zero version accepted")
	}
}

func TestManagementHelloStrictResponse(t *testing.T) {
	response, _, _ := ManagementHelloResponse(ManagementProtocolVersion)
	for n := 0; n < HelloBytes; n++ {
		if ReadManagementHelloResponse(bytes.NewReader(response[:n])) != ErrTransport {
			t.Fatalf("truncated hello at %d accepted", n)
		}
	}
	for i := 0; i < HelloBytes; i++ {
		bad := response
		bad[i] = 255
		if ReadManagementHelloResponse(bytes.NewReader(bad[:])) != ErrProtocol {
			t.Fatalf("malformed byte %d accepted", i)
		}
	}
	if !errors.Is(ReadManagementHelloResponse(failingHelloReader{}), ErrTransport) {
		t.Fatal("transport error leaked")
	}
	reader := bytes.NewReader(append(response[:], []byte("private-selector")...))
	if ReadManagementHelloResponse(reader) != nil || reader.Len() != len("private-selector") {
		t.Fatal("hello consumed selector bytes")
	}
}

func TestManagementScopeDoesNotExpandInspectRecord(t *testing.T) {
	scope := ManagementScope{Actions: []string{Inspect, PreviewAction, ApplyAction, StatusAction}, Fields: []string{FilesField, PerPeerField}}
	if scope.Validate() != nil {
		t.Fatal("full scope rejected")
	}
	r := fixtureRecord()
	r.Actions = scope.Actions
	if r.Validate() == nil {
		t.Fatal("legacy inspection record gained management authority")
	}
	for i := range scope.Actions {
		bad := ManagementScope{Actions: append([]string(nil), scope.Actions...), Fields: scope.Fields}
		bad.Actions[i] = "*"
		if bad.Validate() == nil {
			t.Fatal("wildcard action accepted")
		}
		bad.Actions = append(scope.Actions[:i:i], scope.Actions[i+1:]...)
		if bad.Validate() == nil {
			t.Fatal("partial action set accepted")
		}
	}
	for _, fields := range [][]string{nil, {FilesField}, {PerPeerField, FilesField}, {FilesField, FilesField}, {FilesField, "*"}} {
		if (ManagementScope{Actions: scope.Actions, Fields: fields}).Validate() == nil {
			t.Fatal("non-exact fields accepted")
		}
	}
}
