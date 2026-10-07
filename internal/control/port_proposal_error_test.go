package control

import (
	"context"
	"encoding/json"
	"testing"
)

type portProposalWireError struct{ code, message string }

func (e portProposalWireError) Error() string     { return e.message }
func (e portProposalWireError) ErrorCode() string { return e.code }

func TestPortProposalErrorMemoryRoundTrip(t *testing.T) {
	for _, tc := range []portProposalWireError{
		{"listener_probe_timeout", "the port check deadline expired; no configuration changed"},
		{"listener_probe_canceled", "the port check was canceled; no configuration changed"},
		{"listener_conflict", "saved but not started: the local port is already in use"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			s, _, client, _ := memoryServer(func(context.Context, string) (any, error) { return nil, tc })
			t.Cleanup(func() {
				client.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			})
			if err := json.NewEncoder(client).Encode(Request{Command: "service.ports"}); err != nil {
				t.Fatal(err)
			}
			assertRemoteCode(t, readResponse(client, nil), tc.code, tc.message)
		})
	}
}
