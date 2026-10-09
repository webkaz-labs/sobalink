package resourcegrant

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func managementSelectorFixture() ManagementSelector {
	return ManagementSelector{ProtocolVersion: ManagementProtocolVersion, Target: fixtureRecord().Target, GrantID: strings.Repeat("a", 32), GrantRevision: uint64(capacity.MaxJSONInteger)}
}
func managementSettingsFixture() resource.Settings {
	return resource.Settings{TransferConcurrentFiles: capacity.Default(), TransferConcurrentPerPeer: capacity.Limited(capacity.MaxJSONInteger)}
}
func managementRequestsFixture() []ManagementRequest {
	s := managementSelectorFixture()
	digest := strings.Repeat("f", 64)
	return []ManagementRequest{
		{ManagementSelector: s, Action: Inspect},
		{ManagementSelector: s, Action: PreviewAction, Preview: &ManagementPreviewRequest{Settings: managementSettingsFixture()}},
		{ManagementSelector: s, Action: ApplyAction, Apply: &ManagementApplyRequest{OperationID: digest, BaseRevision: digest, ReviewRevision: digest, Settings: managementSettingsFixture()}},
		{ManagementSelector: s, Action: StatusAction, Status: &ManagementStatusRequest{OperationID: digest}},
	}
}
func managementRepliesFixture() []ManagementReply {
	s := managementSelectorFixture()
	digest := strings.Repeat("f", 64)
	effective := resource.Effective{TransferConcurrentFiles: capacity.MaxJSONInteger, TransferConcurrentPerPeer: capacity.MaxJSONInteger}
	durable := false
	operation := &ManagementOperation{OperationID: digest, Outcome: resource.UnknownOutcome(), EvidenceDurable: &durable}
	return []ManagementReply{
		{ManagementSelector: s, Action: Inspect, Inspection: &ManagementInspection{Requested: managementSettingsFixture(), Effective: effective}},
		{ManagementSelector: s, Action: PreviewAction, Preview: &ManagementPreview{OperationID: digest, BaseRevision: digest, ReviewRevision: digest, Requested: managementSettingsFixture(), Effective: effective}},
		{ManagementSelector: s, Action: ApplyAction, Operation: operation},
		{ManagementSelector: s, Action: StatusAction, Operation: operation},
		{ManagementSelector: s, Action: StatusAction, Unavailable: &ManagementStatusRequest{OperationID: digest}},
	}
}
func TestManagementTypedRoundTripsAndBounds(t *testing.T) {
	for _, request := range managementRequestsFixture() {
		frame, err := ManagementRequestFrame(request)
		if err != nil || len(frame)-4 > MaxManagementRequestBytes {
			t.Fatalf("request %s: %v", request.Action, err)
		}
		got, err := ReadManagementRequest(bytes.NewReader(frame))
		if err != nil || !reflect.DeepEqual(got, request) {
			t.Fatalf("request round trip %s: %v", request.Action, err)
		}
	}
	for _, reply := range managementRepliesFixture() {
		frame, err := ManagementReplyFrame(reply)
		if err != nil || len(frame)-4 > MaxManagementResponseBytes {
			t.Fatalf("reply %s: %v", reply.Action, err)
		}
		got, err := ReadManagementReply(bytes.NewReader(frame))
		if err != nil || !reflect.DeepEqual(got, reply) {
			t.Fatalf("reply round trip %s: %v", reply.Action, err)
		}
		for _, private := range []string{"provider", "authority", "highWater", "journal", "actor", "relationship", "generation", "sequence"} {
			if bytes.Contains(frame[4:], []byte(`"`+private+`"`)) {
				t.Fatalf("private field %s disclosed", private)
			}
		}
	}
}

func TestManagementStrictRequestCodec(t *testing.T) {
	request := managementRequestsFixture()[2]
	data, _ := json.Marshal(request)
	cases := [][]byte{
		append(append([]byte(nil), data...), data...),
		[]byte(strings.Replace(string(data), `"action":"apply"`, `"action":"apply","action":"apply"`, 1)),
		[]byte(strings.Replace(string(data), `"action":"apply"`, `"Action":"apply"`, 1)),
		[]byte(strings.Replace(string(data), `"action":"apply"`, `"action":"resource.grant.apply"`, 1)),
		[]byte(strings.Replace(string(data), `"apply":{`, `"apply":null,"unused":{`, 1)),
		[]byte(strings.Replace(string(data), `"protocolVersion":2`, `"protocolVersion":1`, 1)),
		[]byte(strings.Replace(string(data), `"apply":{`, `"actor":"local-control","apply":{`, 1)),
		[]byte(strings.Replace(string(data), `"apply":{`, `"status":{"operationId":"`+strings.Repeat("a", 64)+`"},"apply":{`, 1)),
		[]byte(strings.Repeat(" ", MaxManagementRequestBytes+1)),
	}
	for i, bad := range cases {
		if got, err := DecodeManagementRequest(bad); err != ErrInvalid || !reflect.DeepEqual(got, ManagementRequest{}) {
			t.Fatalf("bad request %d accepted", i)
		}
	}
	for _, key := range []string{"protocolVersion", "target", "grantId", "grantRevision", "action", "apply"} {
		var object map[string]json.RawMessage
		json.Unmarshal(data, &object)
		delete(object, key)
		bad, _ := json.Marshal(object)
		if _, err := DecodeManagementRequest(bad); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	for _, key := range []string{"operationId", "baseRevision", "reviewRevision", "settings"} {
		var object map[string]json.RawMessage
		json.Unmarshal(data, &object)
		var payload map[string]json.RawMessage
		json.Unmarshal(object["apply"], &payload)
		delete(payload, key)
		object["apply"], _ = json.Marshal(payload)
		bad, _ := json.Marshal(object)
		if _, err := DecodeManagementRequest(bad); err == nil {
			t.Fatalf("missing apply %s accepted", key)
		}
	}
	request.Apply.OperationID = resource.OperationID(request.Target.ResourceID, strings.Repeat("c", 32), 1)
	if request.Validate() == nil {
		t.Fatal("local operation ID accepted")
	}
	request = managementRequestsFixture()[1]
	request.Preview.Settings.TransferConcurrentFiles = capacity.Unlimited()
	if request.Validate() == nil {
		t.Fatal("unsupported canonical choice accepted")
	}
}

func TestManagementReplyStrictUnionAndEvidence(t *testing.T) {
	for _, reply := range managementRepliesFixture() {
		data, _ := json.Marshal(reply)
		for _, field := range []string{"provider", "actor", "journal", "generation"} {
			bad := append([]byte(`{"`+field+`":"private",`), data[1:]...)
			if _, err := DecodeManagementReply(bad); err != ErrProtocol {
				t.Fatalf("extra %s accepted", field)
			}
		}
		reply.Action = "unknown"
		if reply.Validate() == nil {
			t.Fatal("unknown action accepted")
		}
	}
	reply := managementRepliesFixture()[2]
	reply.Operation.EvidenceDurable = nil
	if reply.Validate() == nil {
		t.Fatal("missing evidence flag accepted")
	}
	reply = managementRepliesFixture()[4]
	reply.Operation = managementRepliesFixture()[2].Operation
	if reply.Validate() == nil {
		t.Fatal("unavailable disclosed outcome")
	}
	reply = managementRepliesFixture()[4]
	reply.Action = ApplyAction
	if reply.Validate() == nil {
		t.Fatal("status-only unavailable accepted for apply")
	}
	reply = managementRepliesFixture()[0]
	reply.Preview = managementRepliesFixture()[1].Preview
	if reply.Validate() == nil {
		t.Fatal("multiple reply payloads accepted")
	}
}

func TestManagementFramesTruncationAndSingleMessage(t *testing.T) {
	request, _ := ManagementRequestFrame(managementRequestsFixture()[2])
	reply, _ := ManagementReplyFrame(managementRepliesFixture()[1])
	for n := 0; n < len(request); n++ {
		if _, err := ReadManagementRequest(bytes.NewReader(request[:n])); err != ErrTransport {
			t.Fatalf("truncated request %d accepted: %v", n, err)
		}
	}
	for n := 0; n < len(reply); n++ {
		if _, err := ReadManagementReply(bytes.NewReader(reply[:n])); err != ErrTransport {
			t.Fatalf("truncated reply %d accepted: %v", n, err)
		}
	}
	for _, length := range []uint32{0, MaxManagementRequestBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], length)
		if _, err := ReadManagementRequest(bytes.NewReader(header[:])); err != ErrInvalid {
			t.Fatal("unbounded request allocation")
		}
	}
	for _, length := range []uint32{0, MaxManagementResponseBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], length)
		if _, err := ReadManagementReply(bytes.NewReader(header[:])); err != ErrProtocol {
			t.Fatal("unbounded reply allocation")
		}
	}
	reader := bytes.NewReader(append(append([]byte(nil), request...), request...))
	if _, err := ReadManagementRequest(reader); err != nil || reader.Len() != len(request) {
		t.Fatal("read more than one message")
	}
}

func TestManagementNullUnionArmsRejected(t *testing.T) {
	request := managementRequestsFixture()[0]
	data, _ := json.Marshal(request)
	for _, arm := range []string{"preview", "apply", "status"} {
		bad := append([]byte(`{"`+arm+`":null,`), data[1:]...)
		if _, err := DecodeManagementRequest(bad); err != ErrInvalid {
			t.Fatalf("null %s request arm accepted", arm)
		}
	}
	reply := managementRepliesFixture()[0]
	data, _ = json.Marshal(reply)
	for _, arm := range []string{"preview", "operation", "unavailable"} {
		bad := append([]byte(`{"`+arm+`":null,`), data[1:]...)
		if _, err := DecodeManagementReply(bad); err != ErrProtocol {
			t.Fatalf("null %s reply arm accepted", arm)
		}
	}
}

func TestManagementCanonicalMaximumFitsFrame(t *testing.T) {
	request := managementRequestsFixture()[2]
	request.Apply.Settings.TransferConcurrentFiles = capacity.Limited(capacity.MaxJSONInteger)
	frame, err := ManagementRequestFrame(request)
	if err != nil || len(frame)-4 > MaxManagementRequestBytes {
		t.Fatal("maximal request does not fit")
	}
	reply := managementRepliesFixture()[1]
	reply.Preview.Requested.TransferConcurrentFiles = capacity.Limited(capacity.MaxJSONInteger)
	frame, err = ManagementReplyFrame(reply)
	if err != nil || len(frame)-4 > MaxManagementResponseBytes {
		t.Fatal("maximal preview does not fit")
	}
	reply = managementRepliesFixture()[2]
	reply.Operation.Outcome = resource.Outcome{Status: "saved_not_applied", Configuration: "durable", Accounting: "not_required", Transfer: "failed"}
	frame, err = ManagementReplyFrame(reply)
	if err != nil || len(frame)-4 > MaxManagementResponseBytes {
		t.Fatal("maximal outcome does not fit")
	}
}

func TestManagementCannotDecodeAsInspectGrant(t *testing.T) {
	envelope := fixtureEnvelope()
	envelope.Records[0].Actions = []string{Inspect, PreviewAction, ApplyAction, StatusAction}
	data, _ := json.Marshal(envelope)
	if _, err := Decode(data); err != ErrInvalid {
		t.Fatal("v2 inspect envelope accepted management")
	}
	envelope.Version = 3
	data, _ = json.Marshal(envelope)
	if _, err := Decode(data); err != ErrInvalid {
		t.Fatal("legacy decoder activated v3 management")
	}
}
