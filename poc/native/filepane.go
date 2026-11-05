package main

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// filePane owns drafts across flyout dismissal. Workers only publish events;
// rendering drains them on the UI thread. An uncertain mutation is never retried.
type filePane struct {
	announced                  string
	busy                       bool
	events                     chan filePaneEvent
	review                     fileReview
	receipt                    fileReceipt
	actionID                   string
	process, peer, preparation string
	uncertain                  bool
	notice                     string
	incoming                   *exchangeTransfer
	destination                string
	decline                    bool
	acted                      map[string]bool
}
type filePaneEvent struct {
	kind        string
	review      fileReview
	receipt     fileReceipt
	state       fileDispatchState
	destination string
	err         error
}

func fileLabel(p string) string { return path.Base(strings.ReplaceAll(p, "\\", "/")) }
func (p *filePane) launch(ctx context.Context, wake func(), fn func() filePaneEvent) {
	if p.busy || ctx.Err() != nil {
		return
	}
	if p.events == nil {
		p.events = make(chan filePaneEvent, 1)
	}
	p.busy = true
	go func() {
		e := fn()
		select {
		case p.events <- e:
			wake()
		case <-ctx.Done():
		}
	}()
}
func (p *filePane) drain() {
	if p.events == nil {
		return
	}
	select {
	case e := <-p.events:
		p.busy = false
		if e.err != nil {
			p.notice = "failed"
			if e.kind != "pick" && e.kind != "destination" && e.kind != "status" {
				p.uncertain = true
				p.notice = "uncertain"
			}
			return
		}
		switch e.kind {
		case "prepare":
			p.review = e.review
			p.notice = "review"
		case "pick":
			p.notice = ""
		case "commit":
			p.receipt = e.receipt
			p.review = fileReview{}
			p.notice = "submitted"
		case "discard":
			p.review = fileReview{}
			p.preparation = ""
			p.notice = "discarded"
		case "destination":
			p.destination = e.destination
		case "incoming":
			p.incoming = nil
			p.destination = ""
			p.notice = "submitted"
		case "status":
			if e.state.Status == "committed" {
				p.receipt = fileReceipt{Version: e.state.Version, ProcessID: e.state.ProcessID, PreparationID: e.state.PreparationID, DispatchID: e.state.DispatchID, Children: append([]fileChild(nil), e.state.Children...), Status: e.state.Status}
				p.review = fileReview{}
				p.notice = "submitted"
			} else {
				p.notice = "prepared"
			}
		}
	default:
	}
}
func pendingOffer(t exchangeTransfer) bool {
	return t.ID != "" && t.Direction == "incoming" && t.Status == "awaiting-acceptance"
}
func sameOffer(a, b exchangeTransfer) bool {
	return a.ID == b.ID && a.PeerID == b.PeerID && pendingOffer(b) && a.TotalBytes == b.TotalBytes && reflect.DeepEqual(a.Entries, b.Entries)
}
func (p *filePane) view(c *ui.Context, ctx context.Context, ja, demo, enabled bool, client *exchangeClient, processID, peerID string, transfers []exchangeTransfer) {
	p.drain()
	t := func(en, jp string) string {
		if ja {
			return jp
		}
		return en
	}
	announcement := p.notice
	if p.busy {
		announcement = "busy"
	}
	if announcement != p.announced {
		p.announced = announcement
		if announcement != "" {
			c.Announce(t("Action status changed. Review the status below.", "操作状況が変わりました。下の結果を確認してください。"))
		}
	}
	wake := c.Services().Invalidate
	allowed := enabled && peerID != "" && (demo || client != nil && processID != "")
	ui.Column(c).FillWidth().Gap(7).Children(func() {
		ui.Text(c, t("Files", "ファイル")).Bold()
		if !enabled {
			ui.Text(c, t("Read only. File actions are disabled.", "読み取り専用です。ファイル操作は無効です。")).FontSize(12)
		}
		if enabled {
			if ui.Button(c, t("Choose files…", "ファイルを選択…")).Disabled(!allowed || p.busy || p.review.PreparationID != "" || p.uncertain || p.incoming != nil).Clicked() {
				p.process, p.peer, p.preparation = processID, peerID, newExchangeRequestID()
				proc, peer, prep := p.process, p.peer, p.preparation
				p.actionID = prep
				parent := mygo.FocusedWindow()
				p.launch(ctx, wake, func() filePaneEvent {
					if demo {
						return filePaneEvent{kind: "prepare", review: fileReview{Version: 1, ProcessID: proc, PreparationID: prep, ReviewRevision: "demo-review", PeerIDs: []string{peer}, Entries: []fileEntry{{ID: "demo-file", Path: "sample.txt", Kind: "file", Size: 256}}, TotalBytes: 256, Status: "prepared"}}
					}
					paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: t("Choose files to review", "送信を確認するファイルを選択"), Multiple: true})
					if err != nil || len(paths) == 0 {
						return filePaneEvent{kind: "pick", err: err}
					}
					review, err := client.PrepareFiles(ctx, prep, proc, []string{peer}, paths)
					return filePaneEvent{kind: "prepare", review: review, err: err}
				})
			}
		}
		if p.review.PreparationID != "" {
			ui.Text(c, t("Review before sending", "送信前の確認")).Bold()
			ui.Text(c, t("Recipient: ", "送信先: ")+p.peer).FontSize(12)
			for _, f := range p.review.Entries {
				ui.Text(c, fmt.Sprintf("%s · %d B", f.Path, f.Size)).FontSize(12)
			}
			ui.Text(c, fmt.Sprintf(t("Total: %d bytes", "合計: %d バイト"), p.review.TotalBytes)).FontSize(12)
			if peerID != p.peer || processID != p.process {
				ui.Text(c, t("Selection changed. Return to the reviewed recipient, or discard.", "選択が変わりました。確認した送信先に戻るか、破棄してください。")).FontSize(12)
			}
			ui.Row(c).Gap(6).Children(func() {
				if ui.Button(c, t("Confirm send", "送信を確定")).Disabled(!allowed || p.busy || p.uncertain || peerID != p.peer || processID != p.process).Clicked() {
					review := p.review
					p.actionID = newExchangeRequestID()
					id := p.actionID
					p.launch(ctx, wake, func() filePaneEvent {
						if demo {
							return filePaneEvent{kind: "commit", receipt: fileReceipt{Version: 1, ProcessID: review.ProcessID, PreparationID: review.PreparationID, DispatchID: review.PreparationID, Children: []fileChild{{PeerID: review.PeerIDs[0], TransferID: "demo-transfer"}}, Status: "committed"}}
						}
						receipt, err := client.CommitFiles(ctx, id, review, true)
						return filePaneEvent{kind: "commit", receipt: receipt, err: err}
					})
				}
				if ui.Button(c, t("Discard", "破棄")).Disabled(!enabled || p.busy || p.uncertain || (!demo && client == nil)).Clicked() {
					proc, prep := p.process, p.preparation
					p.actionID = newExchangeRequestID()
					id := p.actionID
					p.launch(ctx, wake, func() filePaneEvent {
						if demo {
							return filePaneEvent{kind: "discard"}
						}
						return filePaneEvent{kind: "discard", err: client.DiscardFiles(ctx, id, proc, prep)}
					})
				}
			})
		}
		if p.uncertain && p.preparation != "" && (demo || client != nil) {
			if ui.Button(c, t("Check staging status", "準備状態を確認")).Disabled(p.busy).Clicked() {
				proc, prep := p.process, p.preparation
				p.launch(ctx, wake, func() filePaneEvent {
					if demo {
						return filePaneEvent{kind: "status", state: fileDispatchState{fileReview: fileReview{Status: "prepared"}}}
					}
					s, err := client.FileStatus(ctx, newExchangeRequestID(), proc, prep)
					return filePaneEvent{kind: "status", state: s, err: err}
				})
			}
		}
		if p.incoming != nil {
			offer := *p.incoming
			current := false
			for _, v := range transfers {
				if sameOffer(offer, v) {
					current = true
					break
				}
			}
			ui.Text(c, t("Review incoming offer", "受信オファーを確認")).Bold()
			ui.Text(c, t("Transfer: ", "転送: ")+offer.ID).FontSize(12)
			ui.Text(c, t("From: ", "送信元: ")+offer.PeerID).FontSize(12)
			for _, f := range offer.Entries {
				ui.Text(c, fmt.Sprintf("%s · %d B", f.Path, f.Size)).FontSize(12)
			}
			ui.Text(c, fmt.Sprintf(t("Total: %d bytes", "合計: %d バイト"), offer.TotalBytes)).FontSize(12)
			if !p.decline {
				ui.Text(c, t("Destination: ", "保存先: ")+p.destination).FontSize(12)
				if ui.Button(c, t("Choose destination…", "保存先を選択…")).Disabled(p.busy || !allowed || !current).Clicked() {
					parent := mygo.FocusedWindow()
					p.launch(ctx, wake, func() filePaneEvent {
						if demo {
							return filePaneEvent{kind: "destination", destination: "Demo Downloads"}
						}
						paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: t("Choose destination", "保存先を選択"), Directory: true})
						dest := ""
						if len(paths) > 0 {
							dest = paths[0]
						}
						return filePaneEvent{kind: "destination", destination: dest, err: err}
					})
				}
			}
			label := t("Confirm accept", "受信を確定")
			if p.decline {
				label = t("Confirm decline", "辞退を確定")
			}
			if ui.Button(c, label).Disabled(!allowed || p.busy || !current || p.acted[offer.ID] || offer.PeerID != peerID || !p.decline && p.destination == "").Clicked() {
				if p.acted == nil {
					p.acted = map[string]bool{}
				}
				p.acted[offer.ID] = true
				dest, decline := p.destination, p.decline
				p.actionID = newExchangeRequestID()
				id := p.actionID
				p.launch(ctx, wake, func() filePaneEvent {
					if demo {
						return filePaneEvent{kind: "incoming"}
					}
					var err error
					if decline {
						err = client.DeclineFiles(ctx, id, offer.ID, true)
					} else {
						err = client.AcceptFiles(ctx, id, offer.ID, dest, true)
					}
					return filePaneEvent{kind: "incoming", err: err}
				})
			}
			if ui.Button(c, t("Back", "戻る")).Disabled(p.busy).Clicked() {
				p.incoming = nil
				p.destination = ""
			}
		}
		if p.busy {
			ui.Text(c, t("Working…", "処理中…")).FontSize(12)
		}
		switch p.notice {
		case "uncertain":
			ui.Text(c, t("Outcome unknown. Do not resend. Check status or refresh history.", "結果が不明です。再送せず、状態または履歴を確認してください。")).FontSize(12)
		case "failed":
			ui.Text(c, t("Could not complete this step. No automatic retry.", "この操作を完了できませんでした。自動再試行はしません。")).FontSize(12)
		case "submitted":
			if demo {
				ui.Text(c, t("Simulation complete. No file was sent or received.", "シミュレーション完了です。実際のファイル送受信は行っていません。")).FontSize(12)
			} else {
				ui.Text(c, t("Command accepted. Refresh history to verify progress; delivery is not confirmed.", "操作を受け付けました。履歴を更新して進行状況を確認してください。配信完了は未確認です。")).FontSize(12)
			}
		case "prepared":
			ui.Text(c, t("Staging is prepared. An earlier uncertain action is still locked; inspect in Web UI or CLI.", "準備済みです。結果不明の操作はロックしたままです。Web UI / CLI で確認してください。")).FontSize(12)
		case "discarded":
			ui.Text(c, t("Staging discarded.", "送信準備を破棄しました。")).FontSize(12)
		}
		if p.uncertain && p.actionID != "" {
			ui.Text(c, t("Request: ", "リクエスト: ")+p.actionID).FontSize(12)
		}
		if len(p.receipt.Children) > 0 {
			ui.Text(c, t("Last dispatch recipients", "直近の送信先別状況")).Bold()
			for _, child := range p.receipt.Children {
				status := t("Not yet in refreshed history", "更新した履歴に未反映")
				if demo {
					status = t("Simulated", "シミュレーション")
				}
				for _, v := range transfers {
					if v.Direction == "outgoing" && v.ID == child.TransferID && v.PeerID == child.PeerID {
						status = v.Status
						break
					}
				}
				ui.Text(c, child.PeerID+" · "+child.TransferID+" · "+status).FontSize(12)
			}
		}
		n := 0
		for _, v := range transfers {
			if peerID != "" && v.PeerID != peerID {
				continue
			}
			n++
			ui.Column(c.Key(v.Direction + ":" + v.ID)).FillWidth().Gap(4).Children(func() {
				ui.Text(c, fmt.Sprintf("%s · %s · %d/%d B", v.ID, exchangeStatus(v.Status, ja), v.CompletedBytes, v.TotalBytes)).FontSize(12)
				for _, f := range v.Entries {
					ui.Text(c, fileLabel(f.Path)).FontSize(12)
				}
				if enabled && pendingOffer(v) && !p.acted[v.ID] {
					ui.Row(c).Gap(6).Children(func() {
						for _, decline := range []bool{false, true} {
							label := t("Review accept", "受信を確認")
							if decline {
								label = t("Review decline", "辞退を確認")
							}
							if ui.Button(c, label).Disabled(!allowed || p.busy || p.review.PreparationID != "" || p.uncertain).Clicked() {
								copy := v
								copy.Entries = append([]exchangeTransferEntry(nil), v.Entries...)
								p.incoming = &copy
								p.preparation = ""
								p.destination = ""
								p.decline = decline
							}
						}
					})
				}
			})
		}
		if n == 0 {
			ui.Text(c, t("No file transfers.", "ファイル転送はありません。")).FontSize(12)
		}
	})
}
