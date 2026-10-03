package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

const receivedHistoryUncertain = "message was received and history was replaced, but durability could not be confirmed; check the receiver's history and private state, and retry only with the same message ID; do not resend with a new ID"

func messageHistoryError(err error, receivedUncertain bool) error {
	message := "message could not be saved; check free storage and private state permissions"
	if receivedUncertain {
		message = receivedHistoryUncertain
		// A failed reconciliation cannot erase the earlier published outcome.
		err = errors.Join(config.ErrAtomicCommitted, err)
	}
	return privateAtomicError(&localCommandError{"message_history_unavailable", message}, err)
}

// All history writers share this state, including cleanup and same-ID replay.
// Unpublished failures leave the previous publication's uncertainty intact.
func (c *Core) saveMessageHistoryLocked(history []Message) error {
	err := writeMessageHistoryWith(c.writeAtomic, filepath.Join(c.dir, "messages.json"), c.messageHistoryStorageBytesLocked(), history)
	if atomicPublished(err) {
		c.messageHistoryUncertain = err != nil
	}
	return err
}

func (c *Core) messageHistoryStorageBytesLocked() int64 {
	return policyOrDefault(c.capacity).Number("resources", "messageStorageBytes")
}

type historyCleanup struct {
	Version     int      `json:"version"`
	Revision    string   `json:"revision"`
	MessageIDs  []string `json:"messageIds"`
	Retained    int      `json:"retained"`
	Remove      int      `json:"remove"`
	Destructive bool     `json:"destructive"`
}

// Retention is applied only through this separately reviewed cleanup. Its
// revision covers the current data, policy and exact deletion candidates.
func (c *Core) historyCleanupLocked(now time.Time) (historyCleanup, []Message) {
	p := policyOrDefault(c.capacity)
	remaining := make([]Message, 0, len(c.messages))
	view := historyCleanup{Version: 1, MessageIDs: []string{}, Destructive: true}
	var encodedBytes int64 = 2
	var sizes []int64
	for _, message := range c.messages {
		tooOld := p.Logical["messageHistoryAgeSeconds"].Mode != "unlimited" && message.CreatedAt.Before(now.Add(-time.Duration(p.Number("logical", "messageHistoryAgeSeconds"))*time.Second))
		if tooOld {
			view.MessageIDs = append(view.MessageIDs, historyMessageKey(message))
			continue
		}
		encoded, _ := json.Marshal(message)
		if len(remaining) > 0 {
			encodedBytes++
		}
		encodedBytes += int64(len(encoded))
		sizes = append(sizes, int64(len(encoded)))
		remaining = append(remaining, message)
	}
	start := 0
	for start < len(remaining) && (int64(len(remaining)-start) > p.Number("logical", "messageHistoryEntries") || encodedBytes > p.Number("logical", "messageHistoryBytes")) {
		view.MessageIDs = append(view.MessageIDs, historyMessageKey(remaining[start]))
		encodedBytes -= sizes[start]
		start++
		if start < len(remaining) {
			encodedBytes--
		}
	}
	remaining = remaining[start:]
	view.Remove, view.Retained = len(view.MessageIDs), len(remaining)
	encoded, _ := json.Marshal(struct {
		Messages []Message
		Policy   any
		IDs      []string
	}{c.messages, p, view.MessageIDs})
	hash := sha256.Sum256(encoded)
	view.Revision = hex.EncodeToString(hash[:])
	return view, remaining
}

func (c *Core) messageHistoryCommand(name string, raw json.RawMessage) (any, error) {
	var in struct {
		ExpectedRevision string `json:"expectedRevision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	view, remaining := c.historyCleanupLocked(time.Now())
	if name == "message.history.preview" {
		return view, nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != view.Revision {
		return nil, &localCommandError{"history_revision_conflict", "message history or cleanup choices changed; preview cleanup again before deleting"}
	}
	if remaining == nil {
		remaining = []Message{}
	}
	saveErr := c.saveMessageHistoryLocked(remaining)
	if !atomicPublished(saveErr) {
		return nil, saveErr
	}
	c.messages = remaining
	return view, saveErr
}

func historyMessageKey(m Message) string { return m.Direction + ":" + m.PeerID + ":" + m.ID }

func (c *Core) listHistory(name string, raw json.RawMessage) (any, error) {
	var in struct {
		Cursor   string `json:"cursor"`
		Revision string `json:"revision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if name == "transfer.list" {
		rows := c.transferViews()
		for _, row := range rows {
			row["transferId"] = row["id"]
			row["id"] = row["direction"].(string) + ":" + row["id"].(string)
		}
		return c.pageRows(rows, in.Cursor, in.Revision)
	}
	c.mu.RLock()
	rows := make([]map[string]any, 0, len(c.messages))
	for _, m := range c.messages {
		rows = append(rows, map[string]any{"id": historyMessageKey(m), "messageId": m.ID, "peerId": m.PeerID, "text": m.Text, "direction": m.Direction, "createdAt": m.CreatedAt, "status": m.Status})
	}
	c.mu.RUnlock()
	return c.pageRows(rows, in.Cursor, in.Revision)
}
