package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/webkaz-labs/sobalink/internal/resource"
)

func transferRetryPayload(args []string, ja bool, out io.Writer) (map[string]any, error) {
	f := commandFlags(commandUsage["retry"], ja, out)
	peer := f.String("peer", "", text(ja, "selected peer", "選択した相手"))
	process := f.String("process", "", text(ja, "process generation from transfers", "transfers が示す起動世代"))
	direction := f.String("direction", "", text(ja, "incoming or outgoing", "incoming または outgoing"))
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return nil, f.Parse(args)
	}
	if len(args) == 0 || args[0] == "" {
		return nil, usageError(ja, commandUsage["retry"])
	}
	if err := parseFlags(f, args[1:], ja); err != nil {
		return nil, err
	}
	payload := map[string]any{"transferId": args[0]}
	selected := false
	f.Visit(func(_ *flag.Flag) { selected = true })
	if selected {
		if *peer == "" || !resource.ValidDigest(*process) || (*direction != "incoming" && *direction != "outgoing") {
			return nil, usageError(ja, commandUsage["retry"])
		}
		payload["expected"] = map[string]string{"peerId": *peer, "processId": *process, "direction": *direction}
	}
	return payload, nil
}

type transferActivityRow struct {
	ID        string `json:"id"`
	PeerID    string `json:"peerId"`
	Direction string `json:"direction"`
	Status    string `json:"status"`
	Retryable bool   `json:"retryable"`
}
type transferActivityGroup struct {
	PeerID     string            `json:"peerId"`
	PeerName   string            `json:"peerName"`
	Completed  int               `json:"completed"`
	Failed     int               `json:"failed"`
	InProgress int               `json:"inProgress"`
	Cancelled  int               `json:"cancelled"`
	Declined   int               `json:"declined"`
	Other      int               `json:"other"`
	Transfers  []json.RawMessage `json:"transfers"`
}

func transferActivityCommand(args []string, dir string, ja bool, out io.Writer, query stateQuery) error {
	f := commandFlags(commandUsage["transfers"], ja, out)
	peer := f.String("peer", "", text(ja, "show only this peer", "この相手だけ表示"))
	structured := f.Bool("json", false, text(ja, "stable machine output", "安定した機械向け出力"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	var state struct {
		ProcessID string            `json:"resourceCatalogProcessId"`
		Peers     []humanPeer       `json:"peers"`
		Transfers []json.RawMessage `json:"transfers"`
	}
	if err := query(&state); err != nil {
		return err
	}
	names := map[string]string{}
	for _, p := range state.Peers {
		names[p.ID] = p.Name
	}
	groups := map[string]*transferActivityGroup{}
	for _, raw := range state.Transfers {
		var row transferActivityRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if *peer != "" && row.PeerID != *peer {
			continue
		}
		group := groups[row.PeerID]
		if group == nil {
			group = &transferActivityGroup{PeerID: row.PeerID, PeerName: names[row.PeerID], Transfers: []json.RawMessage{}}
			groups[row.PeerID] = group
		}
		group.Transfers = append(group.Transfers, raw)
		switch row.Status {
		case "completed":
			group.Completed++
		case "failed":
			group.Failed++
		case "cancelled":
			group.Cancelled++
		case "declined":
			group.Declined++
		case "offered", "awaiting-acceptance", "queued", "transferring", "saving":
			group.InProgress++
		default:
			group.Other++
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make([]*transferActivityGroup, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, groups[key])
	}
	if *structured {
		return json.NewEncoder(out).Encode(struct {
			ProcessID string                   `json:"processId"`
			Groups    []*transferActivityGroup `json:"groups"`
		}{state.ProcessID, ordered})
	}
	if len(ordered) == 0 {
		fmt.Fprintln(out, text(ja, "No transfers for this selection.", "選択した相手の転送はありません。"))
		return nil
	}
	fmt.Fprintf(out, "%s: %s\n", text(ja, "Process generation", "起動世代"), state.ProcessID)
	for _, group := range ordered {
		fmt.Fprintf(out, "%s (%s): %s %d / %s %d / %s %d / %s %d / %s %d / %s %d\n", displayText(group.PeerName), displayText(group.PeerID), text(ja, "Completed", "完了"), group.Completed, text(ja, "Failed", "失敗"), group.Failed, text(ja, "In progress", "進行中"), group.InProgress, text(ja, "Cancelled", "取消"), group.Cancelled, text(ja, "Declined", "辞退"), group.Declined, text(ja, "Other", "その他"), group.Other)
		for _, raw := range group.Transfers {
			var row transferActivityRow
			_ = json.Unmarshal(raw, &row)
			fmt.Fprintf(out, "  %s · %s · %s\n", displayText(row.ID), transferActivityLabel(ja, row.Direction), transferActivityLabel(ja, row.Status))
			if row.Status == "failed" && row.Retryable && resource.ValidDigest(state.ProcessID) {
				fmt.Fprintf(out, "    %s\n", cliCommandExample(dir, "retry", row.ID, "--peer", row.PeerID, "--process", state.ProcessID, "--direction", row.Direction))
			}
		}
	}
	return nil
}

func transferActivityLabel(ja bool, value string) string {
	labels := map[string]string{"incoming": "受信", "outgoing": "送信", "offered": "提供済み", "awaiting-acceptance": "受入待ち", "queued": "待機中", "transferring": "転送中", "saving": "保存中", "completed": "完了", "failed": "失敗", "cancelled": "取消", "declined": "辞退"}
	if ja && labels[value] != "" {
		return labels[value]
	}
	return displayText(value)
}
