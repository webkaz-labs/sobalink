package messageframe_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func TestEncodedMessageEnvelopes(t *testing.T) {
	values := []string{"日本語", "😀", "\u2028", "\u2029"}
	for c := range 128 {
		values = append(values, string(rune(c)))
	}
	for _, value := range values {
		t.Run(fmt.Sprintf("%x", value), func(t *testing.T) {
			text := strings.Repeat(value, messageframe.TextBytes/len(value))
			text += strings.Repeat("x", messageframe.TextBytes-len(text))
			id := strings.Repeat("a", messageframe.IDBytes)
			encode := func(v any) []byte {
				t.Helper()
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			peer := encode(map[string]string{"id": id, "text": text})
			payload := encode(map[string]string{"peerId": id, "text": text})
			command := encode(webui.Command{
				RequestID: strings.Repeat(value, messageframe.RequestIDBytes/len(value)),
				Name:      strings.Repeat(value, messageframe.CommandNameBytes/len(value)),
				Payload:   payload,
			})
			outer := encode(control.Request{Command: string(command)})
			for name, sizes := range map[string][2]int{
				"peer":    {len(peer) + 1, messageframe.PeerRequestBytes},
				"command": {len(command) + 1, messageframe.CommandBytes},
				"control": {len(outer) + 1, messageframe.ControlRequestBytes},
			} {
				if sizes[0] > sizes[1] {
					t.Fatalf("%s encoded size %d exceeds bound %d", name, sizes[0], sizes[1])
				}
			}
			var decoded control.Request
			if err := json.Unmarshal(outer, &decoded); err != nil || decoded.Command != string(command) {
				t.Fatalf("outer command did not round trip: %v", err)
			}
		})
	}
}

func TestPeerIDBoundMatchesValidation(t *testing.T) {
	if !config.ValidPeerID(strings.Repeat("a", messageframe.IDBytes)) || config.ValidPeerID(strings.Repeat("a", messageframe.IDBytes+1)) {
		t.Fatal("JSON envelope ID bound differs from decoded identity validation")
	}
}

func TestDynamicMessageEnvelopes(t *testing.T) {
	defaults, err := messageframe.ForText(messageframe.TextBytes)
	if err != nil || defaults.PeerRequestBytes != int64(messageframe.PeerRequestBytes) || defaults.CommandBytes != int64(messageframe.CommandBytes) || defaults.ControlRequestBytes != int64(messageframe.ControlRequestBytes) {
		t.Fatalf("default framing changed: %+v %v", defaults, err)
	}
	for _, n := range []int64{1, 128 << 10, 1<<53 - 1} {
		bounds, err := messageframe.ForText(n)
		if err != nil || bounds.CommandBytes <= n || bounds.ControlRequestBytes < bounds.CommandBytes {
			t.Fatalf("invalid finite envelope for %d: %+v %v", n, bounds, err)
		}
	}
	for _, n := range []int64{-1, 0, 1<<63 - 1, (1<<63 - 1) / 7} {
		if _, err := messageframe.ForText(n); err == nil {
			t.Fatalf("accepted overflowing/invalid text budget %d", n)
		}
	}
	for _, args := range [][3]int64{{-1, 6, 0}, {1, 0, 0}, {1, 6, -1}, {1, 6, 1<<63 - 1}, {1 << 62, 6, 0}} {
		if _, err := messageframe.EncodedBytes(args[0], args[1], args[2]); err == nil {
			t.Fatalf("accepted invalid encoded budget %v", args)
		}
	}
}
