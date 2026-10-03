package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

type deliveredHistoryError struct{}

func (deliveredHistoryError) Error() string {
	return "peer acknowledged receipt, but local message history could not be saved; check free storage and private state permissions; do not resend the delivered message"
}
func (deliveredHistoryError) ErrorCode() string { return "message_history_unavailable" }

type deliveredMessageBackend struct{ testBackend }

func (*deliveredMessageBackend) Command(context.Context, Command) (any, error) {
	return map[string]string{"id": "delivered-fixture", "status": "sent"}, deliveredHistoryError{}
}

func TestManagementDeliveryWarningSurvivesErrorEncoding(t *testing.T) {
	s, _ := testServer(t)
	s.backend = &deliveredMessageBackend{}
	cookie, csrf := signIn(t, s)
	w := serve(s, request(s, "POST", "/api/command", `{"requestId":"delivered","name":"message.send","payload":{"peerId":"peer","text":"fixture"}}`, cookie, csrf))
	var response struct {
		Code, Error string
		Result      json.RawMessage
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusBadRequest || response.Code != "message_history_unavailable" || response.Error != (deliveredHistoryError{}).Error() {
		t.Fatalf("delivery warning did not reach Web client: %d %s", w.Code, w.Body.String())
	}
	if len(response.Result) != 0 {
		t.Fatal("error response unexpectedly exposed generic result data")
	}
}

func TestManagementAcceptsEscapedMessageBoundary(t *testing.T) {
	for _, value := range []string{"&", "<", ">", "\x01", "\n", `"`, `\`, "\u2028", "\u2029", "界"} {
		t.Run(strings.ReplaceAll(value, "\n", "newline"), func(t *testing.T) {
			s, backend := testServer(t)
			cookie, csrf := signIn(t, s)
			text := strings.Repeat(value, (messageframe.TextBytes-1)/len(value)) + "x"
			text += strings.Repeat("x", messageframe.TextBytes-len(text))
			payload, err := json.Marshal(map[string]string{"peerId": strings.Repeat("p", messageframe.IDBytes), "text": text})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(Command{RequestID: strings.Repeat("&", messageframe.RequestIDBytes), Name: "message.send", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			w := serve(s, request(s, "POST", "/api/command", string(body), cookie, csrf))
			if w.Code != http.StatusOK || backend.commands.Load() != 1 {
				t.Fatalf("valid escaped message rejected: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestManagementMessageEnvelopeRejectsExcessAndMalformedData(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"oversized value", `{"requestId":"test","name":"message.send","payload":{"text":"` + strings.Repeat("x", messageframe.CommandBytes) + `"}}`, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized trailing whitespace", `{"requestId":"test","name":"message.send","payload":{}}` + strings.Repeat(" ", messageframe.CommandBytes), http.StatusRequestEntityTooLarge, "request_too_large"},
		{"unknown field", `{"requestId":"test","name":"message.send","payload":{},"extra":true}`, http.StatusBadRequest, "invalid"},
		{"trailing value", `{"requestId":"test","name":"message.send","payload":{}} {}`, http.StatusBadRequest, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, backend := testServer(t)
			cookie, csrf := signIn(t, s)
			w := serve(s, request(s, "POST", "/api/command", tc.body, cookie, csrf))
			var result struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != tc.status || result.Code != tc.code || backend.commands.Load() != 0 {
				t.Fatalf("invalid request reached backend or lost error: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
