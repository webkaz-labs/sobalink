package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/messageframe"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

// This adapter never discovers a profile, enables trust, retries, or logs payloads.
// The UI must preserve request IDs and mark transport errors as uncertain. A
// successful command means acceptance only; delivery comes from refreshed state.
type exchangeCaller func(context.Context, string, string, any) error
type exchangeClient struct {
	dir     string
	enabled bool
	call    exchangeCaller
}

func newExchangeClient(dir string, enabled bool, call exchangeCaller) *exchangeClient {
	if call == nil {
		call = control.Call
	}
	return &exchangeClient{dir: dir, enabled: enabled, call: call}
}
func newExchangeRequestID() string { return "native-" + rand.Text() }

var errExchangeMessageSize = errors.New("message exceeds the native UI UTF-8 byte limit")

var errExchangeReadOnly = errors.New("sending is disabled")
var errExchangeReview = errors.New("explicit current review is required")
var errExchangeResponse = errors.New("unexpected response; inspect status before another action")

func (c *exchangeClient) command(ctx context.Context, id, name string, payload, out any, mutation bool) error {
	if mutation && !c.enabled {
		return errExchangeReadOnly
	}
	if c.dir == "" || id == "" || len(id) > 128 {
		return errExchangeReview
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	wire, err := json.Marshal(webui.Command{RequestID: id, Name: name, Payload: raw})
	if err != nil {
		return err
	}
	return c.call(ctx, c.dir, string(wire), out)
}

type exchangeMessage struct {
	ID        string `json:"id"`
	PeerID    string `json:"peerId"`
	Direction string `json:"direction"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

func (c *exchangeClient) SendMessage(ctx context.Context, id, peerID, text string) (exchangeMessage, error) {
	var out exchangeMessage
	if peerID == "" || strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		return out, errExchangeReview
	}
	// This UI uses the shared default text budget; Core may be configured
	// differently. Reject before IPC rather than interpreting remote errors
	// as proof that a request was never delivered.
	if len(text) > messageframe.TextBytes {
		return out, errExchangeMessageSize
	}
	err := c.command(ctx, id, "message.send", map[string]string{"peerId": peerID, "text": text}, &out, true)
	if err == nil && (out.ID == "" || out.PeerID != peerID || out.Text != text || out.Direction != "outgoing" || out.Status != "sent") {
		err = errExchangeResponse
	}
	return out, err
}

type fileEntry struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type fileReview struct {
	Version        int         `json:"version"`
	ProcessID      string      `json:"processId"`
	PreparationID  string      `json:"preparationId"`
	ReviewRevision string      `json:"reviewRevision"`
	PeerIDs        []string    `json:"peerIds"`
	Entries        []fileEntry `json:"entries"`
	TotalBytes     int64       `json:"totalBytes"`
	Status         string      `json:"status"`
}
type fileChild struct {
	PeerID     string `json:"peerId"`
	TransferID string `json:"transferId"`
}
type fileReceipt struct {
	Version       int         `json:"version"`
	ProcessID     string      `json:"processId"`
	PreparationID string      `json:"preparationId"`
	DispatchID    string      `json:"dispatchId"`
	Children      []fileChild `json:"children"`
	Status        string      `json:"status"`
}

// Status may be a preparation or a committed receipt, so retain both contracts.
type fileDispatchState struct {
	fileReview
	DispatchID string      `json:"dispatchId"`
	Children   []fileChild `json:"children"`
}

func (c *exchangeClient) PrepareFiles(ctx context.Context, id, processID string, peerIDs, paths []string) (fileReview, error) {
	var out fileReview
	if processID == "" || len(peerIDs) == 0 || len(paths) == 0 {
		return out, errExchangeReview
	}
	for _, v := range append(slices.Clone(peerIDs), paths...) {
		if strings.TrimSpace(v) == "" {
			return out, errExchangeReview
		}
	}
	err := c.command(ctx, id, "transfer.dispatch.prepare", struct {
		ProcessID string   `json:"processId"`
		PeerIDs   []string `json:"peerIds"`
		Paths     []string `json:"paths"`
	}{processID, slices.Clone(peerIDs), slices.Clone(paths)}, &out, true)
	if err == nil && (out.Version != 1 || out.ProcessID != processID || out.PreparationID != id || out.Status != "prepared" || out.ReviewRevision == "" || !slices.Equal(out.PeerIDs, peerIDs) || len(out.Entries) == 0 || out.TotalBytes < 0) {
		err = errExchangeResponse
	}
	return out, err
}
func (c *exchangeClient) CommitFiles(ctx context.Context, id string, review fileReview, confirm bool) (fileReceipt, error) {
	var out fileReceipt
	if !confirm || review.Status != "prepared" || review.Version != 1 || review.ProcessID == "" || review.PreparationID == "" || review.ReviewRevision == "" || len(review.PeerIDs) == 0 || len(review.Entries) == 0 {
		return out, errExchangeReview
	}
	err := c.command(ctx, id, "transfer.dispatch.commit", map[string]any{"processId": review.ProcessID, "preparationId": review.PreparationID, "reviewRevision": review.ReviewRevision, "confirm": true}, &out, true)
	if err == nil {
		if out.Version != 1 || out.ProcessID != review.ProcessID || out.PreparationID != review.PreparationID || out.DispatchID != review.PreparationID || out.Status != "committed" || len(out.Children) != len(review.PeerIDs) {
			return out, errExchangeResponse
		}
		for i, child := range out.Children {
			if child.PeerID != review.PeerIDs[i] || child.TransferID == "" {
				return out, errExchangeResponse
			}
		}
	}
	return out, err
}
func (c *exchangeClient) FileStatus(ctx context.Context, id, processID, preparationID string) (fileDispatchState, error) {
	var out fileDispatchState
	if processID == "" || preparationID == "" {
		return out, errExchangeReview
	}
	err := c.command(ctx, id, "transfer.dispatch.status", map[string]string{"processId": processID, "preparationId": preparationID}, &out, false)
	if err == nil && (out.Version != 1 || out.ProcessID != processID || out.PreparationID != preparationID || (out.Status != "prepared" && out.Status != "committed")) {
		err = errExchangeResponse
	}
	return out, err
}
func (c *exchangeClient) DiscardFiles(ctx context.Context, id, processID, preparationID string) error {
	if processID == "" || preparationID == "" {
		return errExchangeReview
	}
	var out fileDispatchState
	err := c.command(ctx, id, "transfer.dispatch.discard", map[string]string{"processId": processID, "preparationId": preparationID}, &out, true)
	if err == nil && (out.Version != 1 || out.ProcessID != processID || out.PreparationID != preparationID || out.Status != "discarded") {
		return errExchangeResponse
	}
	return err
}
func (c *exchangeClient) AcceptFiles(ctx context.Context, id, transferID, destination string, confirm bool) error {
	// Require an explicit destination even if Core has a default. Never change
	// autosave policy, open a received file, or claim receipt from this result.
	if !confirm || transferID == "" || strings.TrimSpace(destination) == "" {
		return errExchangeReview
	}
	return c.command(ctx, id, "transfer.accept", map[string]string{"transferId": transferID, "destination": destination}, nil, true)
}
func (c *exchangeClient) DeclineFiles(ctx context.Context, id, transferID string, confirm bool) error {
	if !confirm || transferID == "" {
		return errExchangeReview
	}
	return c.command(ctx, id, "transfer.decline", map[string]string{"transferId": transferID}, nil, true)
}

type exchangeTransferEntry struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	StoredPath string `json:"storedPath"`
}
type exchangeTransfer struct {
	ID             string                  `json:"id"`
	PeerID         string                  `json:"peerId"`
	Direction      string                  `json:"direction"`
	Status         string                  `json:"status"`
	Entries        []exchangeTransferEntry `json:"entries"`
	TotalBytes     int64                   `json:"totalBytes"`
	CompletedBytes int64                   `json:"completedBytes"`
	CreatedAt      string                  `json:"createdAt"`
}
