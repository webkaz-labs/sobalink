package resourcegrant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

func TestRemoteManagementLocalConfirmation(t *testing.T) {
	for _, request := range managementRequestsFixture() {
		input := RemoteManagementInput{PeerKey: strings.Repeat("a", 64), Request: request, Confirm: request.Action == ApplyAction}
		if input.Validate() != nil {
			t.Fatal("valid input rejected")
		}
		input.Confirm = !input.Confirm
		if input.Validate() == nil {
			t.Fatal("confirmation/action mismatch accepted")
		}
		input.Confirm = request.Action == ApplyAction
		input.PeerKey = ""
		if input.Validate() == nil {
			t.Fatal("missing peer accepted")
		}
	}
}

func TestRemoteManagementLocalStrictPrivateBoundary(t *testing.T) {
	request := managementRequestsFixture()[2]
	input := RemoteManagementInput{PeerKey: strings.Repeat("a", 64), Request: request, Confirm: true}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RemoteManagementInput
	if resource.Decode(data, 4096, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("local roundtrip")
	}
	for _, extra := range []string{`"actor":"local-control"`, `"command":"resource.apply"`, `"relationship":{}`, `"generation":"fake"`} {
		bad := append(append([]byte{}, data[:len(data)-1]...), []byte(","+extra+"}")...)
		if resource.Decode(bad, 4096, &decoded) == nil {
			t.Fatal("extra authority field accepted")
		}
	}
	wire, err := ManagementRequestFrame(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "confirm") || strings.Contains(string(wire), "peerKey") {
		t.Fatal("local consent leaked into protocol")
	}
}
