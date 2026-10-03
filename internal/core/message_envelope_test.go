package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
)

func boundaryMessageTexts() map[string]string {
	return map[string]string{
		"newlines":    "x" + strings.Repeat("\n", messageframe.TextBytes-1),
		"html":        strings.Repeat("&", messageframe.TextBytes),
		"quotes":      strings.Repeat(`"`, messageframe.TextBytes),
		"backslashes": strings.Repeat(`\`, messageframe.TextBytes),
		"multibyte":   strings.Repeat("界", messageframe.TextBytes/len("界")) + "x",
		"controls":    "x" + strings.Repeat("\x01", messageframe.TextBytes-1),
	}
}

func TestMessageBoundaryRoundTripsAndPersists(t *testing.T) {
	for name, text := range boundaryMessageTexts() {
		t.Run(name, func(t *testing.T) {
			if len(text) != messageframe.TextBytes || !validText(text) {
				t.Fatal("fixture is not valid maximum decoded text")
			}
			p := newCorePair(t)
			trustPair(t, p)
			result := mustCommand(t, p.a, "message.send", map[string]string{"peerId": "peer-b", "text": text}).(Message)
			if result.Status != "sent" || result.Text != text {
				t.Fatalf("outgoing result lost text or acknowledgement: %s", result.Status)
			}
			for _, c := range []*Core{p.a, p.b} {
				loaded := &Core{dir: c.dir}
				if err := loaded.loadMessages(); err != nil {
					t.Fatal(err)
				}
				if len(loaded.messages) != 1 || loaded.messages[0].Text != text || loaded.messages[0].ID != result.ID {
					t.Fatal("persisted message differs from delivered text")
				}
				info, err := os.Stat(filepath.Join(c.dir, "messages.json"))
				if err != nil || info.Size() > int64(maxMessageHistoryBytes) {
					t.Fatalf("invalid history file: %v", err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
					t.Fatalf("history is not private: %s", info.Mode())
				}
				if name == "html" && info.Size() <= 64<<10 {
					t.Fatal("fixture no longer exercises the old history limit")
				}
				if name == "html" {
					var generic []Message
					if err := config.ReadJSON(filepath.Join(c.dir, "messages.json"), &generic); err == nil {
						t.Fatal("ordinary configuration reader lost its smaller bound")
					}
					if err := config.WriteJSON(filepath.Join(c.dir, "generic.json"), loaded.messages); err == nil {
						t.Fatal("ordinary configuration writer lost its smaller bound")
					}
				}
			}
		})
	}
}

func TestMessageDecodedPolicyRemainsBounded(t *testing.T) {
	for _, text := range []string{"", "\n\t ", "x\x00", "x\xff"} {
		if validText(text) {
			t.Fatal("invalid decoded message was accepted")
		}
	}
	raw, _ := json.Marshal(map[string]string{"peerId": "peer", "text": strings.Repeat("x", messageframe.TextBytes+1)})
	_, err := (&Core{}).sendMessageCommand(t.Context(), raw)
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "message_too_large" {
		t.Fatalf("oversized decoded text lost capacity error: %v", err)
	}
}

func TestMessagePersistenceFailureReportsDeliveryState(t *testing.T) {
	for _, receiverBlocked := range []bool{true, false} {
		p := newCorePair(t)
		trustPair(t, p)
		blocked := p.a
		wantCode := "message_history_unavailable"
		if receiverBlocked {
			blocked = p.b
			wantCode = "message_peer_storage_unavailable"
		}
		if err := os.Mkdir(filepath.Join(blocked.dir, "messages.json"), 0700); err != nil {
			t.Fatal(err)
		}
		result, err := command(p.a, randomID(), "message.send", map[string]string{"peerId": "peer-b", "text": "boundary failure fixture"})
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != wantCode {
			t.Fatalf("persistence failure lost recovery code: %v", err)
		}
		p.b.mu.RLock()
		received := len(p.b.messages)
		p.b.mu.RUnlock()
		if receiverBlocked && received != 0 || !receiverBlocked && received != 1 {
			t.Fatalf("unexpected receiver commit count: %d", received)
		}
		if !receiverBlocked && !strings.Contains(err.Error(), "peer acknowledged receipt") {
			t.Fatalf("local persistence failure hid confirmed delivery: %v", err)
		}
		if !receiverBlocked && (result.(Message).Status != "sent" || result.(Message).ID == "") {
			t.Fatal("local persistence failure lost the acknowledged message")
		}
	}
}

func TestMessageBoundaryRefusesInvalidOrForeignRequests(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	receive := func(body string) (int, []byte) {
		r := httptest.NewRequest("POST", "http://peer.invalid/v1/messages", strings.NewReader(body))
		r.RemoteAddr = netip.AddrPortFrom(p.na.ip, 32123).String()
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		p.b.peerHTTP(p.b.peerServer, w, r)
		return w.Code, w.Body.Bytes()
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"decoded oversized", `{"id":"too-large","text":"` + strings.Repeat("a", messageframe.TextBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"encoded oversized", `{"id":"padding","text":"ok"}` + strings.Repeat(" ", messageframe.PeerRequestBytes), http.StatusRequestEntityTooLarge},
		{"NUL", `{"id":"nul","text":"x\u0000"}`, http.StatusBadRequest},
		{"blank", `{"id":"blank","text":"\n\t "}`, http.StatusBadRequest},
		{"bad id", `{"id":"foreign/identity","text":"x"}`, http.StatusBadRequest},
		{"unknown field", `{"id":"unknown","text":"x","peerId":"peer-a"}`, http.StatusBadRequest},
		{"trailing JSON", `{"id":"trailing","text":"x"}{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := receive(tc.body)
			if status != tc.status {
				t.Fatalf("status %d, want %d: %s", status, tc.status, body)
			}
			if status == http.StatusRequestEntityTooLarge && !bytes.Contains(body, []byte(`"code":`)) {
				t.Fatal("capacity error has no stable code")
			}
		})
	}
	p.nb.mu.Lock()
	p.nb.who[p.na.ip] = "foreign-peer"
	p.nb.mu.Unlock()
	foreign, _ := json.Marshal(map[string]string{"id": "foreign", "text": strings.Repeat("&", messageframe.TextBytes)})
	if status, _ := receive(string(foreign)); status != http.StatusForbidden {
		t.Fatalf("foreign transport identity accepted: %d", status)
	}
	p.b.mu.RLock()
	defer p.b.mu.RUnlock()
	if len(p.b.messages) != 0 {
		t.Fatal("refused input was retained")
	}
}

func TestMessageBoundaryIncludesEscapedIdentifier(t *testing.T) {
	p := newCorePair(t)
	trustPair(t, p)
	body := `{"id":"` + strings.Repeat(`\u0061`, messageframe.IDBytes) + `","text":"` + strings.Repeat(`\u0026`, messageframe.TextBytes) + "\"}\n"
	if len(body) != messageframe.PeerRequestBytes {
		t.Fatalf("fixture is not the full legal peer envelope: %d", len(body))
	}
	r := httptest.NewRequest("POST", "http://peer.invalid/v1/messages", strings.NewReader(body))
	r.RemoteAddr = netip.AddrPortFrom(p.na.ip, 32123).String()
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.b.peerHTTP(p.b.peerServer, w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("maximum escaped identity and text were refused: %d %s", w.Code, w.Body.String())
	}
	loaded := &Core{dir: p.b.dir}
	if err := loaded.loadMessages(); err != nil || len(loaded.messages) != 1 || loaded.messages[0].ID != strings.Repeat("a", messageframe.IDBytes) {
		t.Fatalf("maximum identity was not retained: %v", err)
	}
}

func TestPeerJSONEnvelopeCountsTrailingWhitespace(t *testing.T) {
	for _, n := range []int{20, 21, 1024} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`+strings.Repeat(" ", n-2)))
		r.Header.Set("Content-Type", "application/json")
		var v struct{}
		err := readJSON(r, 20, &v)
		if n == 20 && err != nil || n > 20 && !errors.Is(err, errPeerRequestTooLarge) {
			t.Fatalf("%d bytes: %v", n, err)
		}
	}
}

func TestMessageHistoryPreservesRetentionAndLegacyFiles(t *testing.T) {
	newMessage := func(id, text string) Message {
		return Message{ID: id, PeerID: "peer", Direction: "incoming", Status: "received", Text: text, CreatedAt: time.Now().UTC()}
	}
	t.Run("maximum metadata", func(t *testing.T) {
		c := &Core{dir: t.TempDir()}
		m := newMessage(strings.Repeat("i", messageframe.IDBytes), strings.Repeat("&", messageframe.TextBytes))
		m.PeerID = strings.Repeat("p", messageframe.IDBytes)
		m.CreatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("fixture", 9*60*60))
		if err := c.appendMessageLocked(m); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(c.dir, "messages.json"))
		if err != nil || info.Size() != int64(maxMessageHistoryBytes) {
			t.Fatalf("maximum metadata fixture size differs from bound: %v %v", info, err)
		}
		if err := c.loadMessages(); err != nil || len(c.messages) != 1 || c.messages[0].ID != m.ID || c.messages[0].Text != m.Text {
			t.Fatalf("maximum valid history did not reload: %v", err)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		c := &Core{dir: t.TempDir()}
		history := []Message{newMessage("legacy", "saved")}
		if err := config.WriteJSON(filepath.Join(c.dir, "messages.json"), history); err != nil {
			t.Fatal(err)
		}
		if err := c.loadMessages(); err != nil || len(c.messages) != 1 || c.messages[0].Text != "saved" {
			t.Fatalf("legacy history unreadable: %v", err)
		}
	})
	t.Run("count", func(t *testing.T) {
		c := &Core{dir: t.TempDir()}
		for range 128 {
			c.messages = append(c.messages, newMessage(randomID(), "x"))
		}
		old := c.messages[0].ID
		if err := c.appendMessageLocked(newMessage("new", "x")); err != nil || len(c.messages) != 129 || c.messages[0].ID != old {
			t.Fatalf("count retention changed: %d %v", len(c.messages), err)
		}
	})
	t.Run("age", func(t *testing.T) {
		c := &Core{dir: t.TempDir(), messages: []Message{newMessage("old", "old")}}
		c.messages[0].CreatedAt = time.Now().Add(-31 * 24 * time.Hour)
		if err := c.appendMessageLocked(newMessage("new", "new")); err != nil || len(c.messages) != 2 || c.messages[0].ID != "old" {
			t.Fatalf("age retention changed: %v", err)
		}
	})
	t.Run("encoded target", func(t *testing.T) {
		c := &Core{dir: t.TempDir(), messages: []Message{newMessage("old", "old")}}
		if err := c.appendMessageLocked(newMessage("large", strings.Repeat("&", messageframe.TextBytes))); err != nil || len(c.messages) != 2 || c.messages[0].ID != "old" {
			t.Fatalf("single large message retention changed: %v", err)
		}
		if err := c.appendMessageLocked(newMessage("new", "new")); err != nil || len(c.messages) != 3 || c.messages[0].ID != "old" {
			t.Fatalf("encoded retention target changed: %v", err)
		}
	})
}

func TestMessageHistoryRejectsMalformedOrUnboundedMetadata(t *testing.T) {
	base := Message{ID: "message", PeerID: "peer", Direction: "incoming", Status: "received", Text: "text", CreatedAt: time.Now().UTC()}
	for _, field := range []string{"direction", "status", "text", "peer", "id"} {
		t.Run(field, func(t *testing.T) {
			m := base
			switch field {
			case "direction":
				m.Direction = strings.Repeat("x", 100)
			case "status":
				m.Status = "unknown"
			case "text":
				m.Text = strings.Repeat("x", 4<<20)
			case "peer":
				m.PeerID = strings.Repeat("p", messageframe.IDBytes+1)
			case "id":
				m.ID = "invalid/id"
			}
			c := &Core{dir: t.TempDir()}
			b, _ := json.Marshal([]Message{m})
			if err := os.WriteFile(filepath.Join(c.dir, "messages.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.loadMessages(); err == nil || len(c.messages) != 0 {
				t.Fatal("invalid history was loaded")
			}
		})
	}
	for _, data := range []string{"[] {}", `[{"unexpected":true}]`, strings.Repeat(" ", maxMessageHistoryBytes+1)} {
		c := &Core{dir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(c.dir, "messages.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.loadMessages(); err == nil {
			t.Fatal("malformed or oversized history was loaded")
		}
	}
	if runtime.GOOS != "windows" {
		c := &Core{dir: t.TempDir()}
		original := filepath.Join(c.dir, "real.json")
		if err := os.WriteFile(original, []byte("[]"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(original, filepath.Join(c.dir, "messages.json")); err != nil {
			t.Fatal(err)
		}
		if err := c.loadMessages(); err == nil {
			t.Fatal("history symlink was followed")
		}
		if err := c.appendMessageLocked(base); err == nil || len(c.messages) != 0 {
			t.Fatal("history symlink was overwritten")
		}
	}
}
