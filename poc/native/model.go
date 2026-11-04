package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
)

type peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
	Trusted  bool   `json:"trusted"`
	Autosave struct {
		Paused bool `json:"paused"`
	} `json:"autosave"`
	Path string `json:"path"`
}
type snapshot struct {
	Self struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"self"`
	ResourceCatalogProcessID string             `json:"resourceCatalogProcessId"`
	Messages                 []exchangeMessage  `json:"messages"`
	Transfers                []exchangeTransfer `json:"transfers"`
	Peers                    []peer             `json:"peers"`
	Services                 []struct {
		ID string `json:"id"`
	} `json:"services"`
}
type result struct {
	state snapshot
	err   error
}
type loader func(context.Context) (snapshot, error)

func ipcLoader(dir string) loader {
	return func(ctx context.Context) (snapshot, error) {
		var state snapshot
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		err := control.Call(ctx, dir, "status", &state)
		if err == nil && state.Self.Status == "" {
			err = errors.New("unsupported snapshot")
		}
		return state, err
	}
}
func demoSnapshot() snapshot {
	var s snapshot
	s.Self.Name = "Demo device"
	s.Self.Status = "running"
	s.Peers = []peer{{ID: "demo-alpha", Name: "Demo laptop", Online: true, Trusted: true, Path: "direct"}, {ID: "demo-beta", Name: "Demo desktop", Online: true, Path: "relay"}, {ID: "demo-gamma", Name: "Demo tablet", Path: "unknown"}}
	s.ResourceCatalogProcessID = "demo-process"
	s.Transfers = []exchangeTransfer{{ID: "demo-offer", PeerID: "demo-alpha", Direction: "incoming", Status: "awaiting-acceptance", TotalBytes: 256, Entries: []exchangeTransferEntry{{ID: "demo-entry", Path: "sample.txt", Kind: "file", Size: 256}}}}
	s.Messages = []exchangeMessage{{ID: "demo-message", PeerID: "demo-alpha", Direction: "incoming", Text: "テスト用のメッセージです。", Status: "received"}}
	return s
}
func locale(override string, getenv func(string) string) (bool, error) {
	if override != "auto" && override != "ja" && override != "en" {
		return false, errors.New("locale must be auto, ja or en")
	}
	if override != "auto" {
		return override == "ja", nil
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := getenv(key); value != "" {
			return strings.HasPrefix(strings.ToLower(value), "ja"), nil
		}
	}
	return false, nil
}
