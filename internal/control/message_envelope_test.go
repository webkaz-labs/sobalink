package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestMemoryControlEscapedMessageBoundary(t *testing.T) {
	for _, value := range []string{"&", "<", ">", "\x01", "\n", `"`, `\`, "\u2028", "\u2029", "界"} {
		t.Run(strings.ReplaceAll(value, "\n", "newline"), func(t *testing.T) {
			text := strings.Repeat(value, (messageframe.TextBytes-1)/len(value)) + "x"
			text += strings.Repeat("x", messageframe.TextBytes-len(text))
			payload, _ := json.Marshal(map[string]string{"peerId": strings.Repeat("p", messageframe.IDBytes), "text": text})
			command, _ := json.Marshal(webui.Command{RequestID: strings.Repeat("&", messageframe.RequestIDBytes), Name: "message.send", Payload: payload})
			s, _, client, _ := memoryServer(func(_ context.Context, got string) (any, error) {
				if got != string(command) {
					t.Error("message changed across IPC")
				}
				return true, nil
			})
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(5 * time.Second))
			if err := json.NewEncoder(client).Encode(Request{Command: string(command)}); err != nil {
				t.Fatal(err)
			}
			var accepted bool
			if err := readResponse(client, &accepted); err != nil || !accepted {
				t.Fatalf("valid message rejected across IPC: %v", err)
			}
		})
	}
}

func TestMemoryControlMessageEnvelopeRejectsExcess(t *testing.T) {
	for _, command := range []string{
		strings.Repeat("x", messageframe.CommandBytes+1),
		strings.Repeat("&", messageframe.CommandBytes),
	} {
		s, _, client, _ := memoryServer(func(context.Context, string) (any, error) {
			t.Error("oversized command reached handler")
			return nil, nil
		})
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		written := make(chan struct{})
		go func() { defer close(written); _ = json.NewEncoder(client).Encode(Request{Command: command}) }()
		err := readResponse(client, nil)
		assertRemoteCode(t, err, "request_too_large", "local command exceeds its JSON envelope limit; shorten the text or reduce the command payload")
		_ = client.Close()
		<-written
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		// This must fail before dialing, including where sockets are unavailable.
		assertRemoteCode(t, Call(t.Context(), "unused", command, nil), "request_too_large", "local command exceeds its JSON envelope limit; shorten the text or reduce the command payload")
	}
}

func TestMemoryControlDeliveryWarningSurvivesErrorEncoding(t *testing.T) {
	warning := "peer acknowledged receipt, but local message history could not be saved; check free storage and private state permissions; do not resend the delivered message"
	s, _, client, _ := memoryServer(func(context.Context, string) (any, error) {
		return map[string]string{"id": "delivered-fixture", "status": "sent"}, &RemoteError{Code: "message_history_unavailable", Message: warning}
	})
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(client).Encode(Request{Command: `{"requestId":"delivered","name":"message.send","payload":{"peerId":"peer","text":"fixture"}}`}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(client).Decode(&response); err != nil || response.Code != "message_history_unavailable" || response.Error != warning {
		t.Fatalf("delivery warning did not reach IPC client: %#v %v", response, err)
	}
	if len(response.Data) != 0 {
		t.Fatal("error response unexpectedly exposed generic result data")
	}
}
